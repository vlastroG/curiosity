package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// BasicAuth закрывает интерфейс и API паролем (HTTP Basic). Браузер спрашивает его
// один раз и дальше подставляет сам, в том числе в fetch и EventSource. /health
// открыт -- для проверок живости. Пустой пароль -- без защиты.
func BasicAuth(user, password string, next http.Handler) http.Handler {
	if password == "" {
		return next
	}
	// сравниваем хеши: время сравнения не зависит от длины и совпавшего префикса
	wantUser, wantPass := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		gotUser, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		if !ok || subtle.ConstantTimeCompare(gotUser[:], wantUser[:])&subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Twain Expert", charset="UTF-8"`)
			http.Error(w, "нужен пароль", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
