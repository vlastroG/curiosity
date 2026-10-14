package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// User -- логин и пароль интерфейса.
type User struct{ Name, Password string }

// ParseUsers собирает пользователей: list -- "логин:пароль,логин:пароль"
// (DOCINDEX_USERS), а если он пуст -- один пользователь name с паролем password
// (DOCINDEX_USER, DOCINDEX_PASSWORD; пустой пароль -- без защиты). Первый
// пользователь получает чаты, созданные до разделения, и чаты из CLI.
func ParseUsers(list, name, password string) ([]User, error) {
	if strings.TrimSpace(list) == "" {
		if name == "" {
			return nil, errors.New("пустой логин (DOCINDEX_USER)")
		}
		return []User{{name, password}}, nil
	}
	var users []User
	seen := map[string]bool{}
	for _, entry := range strings.Split(list, ",") {
		n, p, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || n == "" || p == "" {
			return nil, fmt.Errorf("DOCINDEX_USERS: нужно логин:пароль, получено %q", n)
		}
		if seen[n] {
			return nil, fmt.Errorf("DOCINDEX_USERS: логин %q дважды", n)
		}
		seen[n] = true
		users = append(users, User{n, p})
	}
	return users, nil
}

type userKey struct{}

// UserFrom -- логин пользователя запроса ("" -- запрос мимо Auth, как в тестах).
func UserFrom(ctx context.Context) string {
	name, _ := ctx.Value(userKey{}).(string)
	return name
}

// Auth закрывает интерфейс и API паролем (HTTP Basic) и кладёт логин в запрос: у
// каждого пользователя свои чаты. Браузер спрашивает пароль один раз и дальше
// подставляет сам, в том числе в fetch и EventSource. /health открыт -- для
// проверок живости. Один пользователь без пароля -- без защиты, все запросы от него.
func Auth(users []User, next http.Handler) http.Handler {
	if len(users) == 1 && users[0].Password == "" {
		name := users[0].Name
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, name)))
		})
	}
	// сравниваем хеши всех пользователей: время не зависит от длины, префикса и того, чей логин
	type cred struct{ name, pass [32]byte }
	creds := make([]cred, len(users))
	for i, u := range users {
		creds[i] = cred{sha256.Sum256([]byte(u.Name)), sha256.Sum256([]byte(u.Password))}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		gotName, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		match := 0
		for _, c := range creds {
			match |= subtle.ConstantTimeCompare(gotName[:], c.name[:]) & subtle.ConstantTimeCompare(gotPass[:], c.pass[:])
		}
		if !ok || match != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Twain Expert", charset="UTF-8"`)
			http.Error(w, "нужен пароль", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}
