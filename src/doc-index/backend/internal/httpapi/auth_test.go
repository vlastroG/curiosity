package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// whoami отвечает логином пользователя запроса.
var whoami = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(UserFrom(r.Context()))) })

func TestAuth(t *testing.T) {
	srv := httptest.NewServer(Auth([]User{{"twain", "s3cret"}, {"huck", "raft"}}, whoami))
	defer srv.Close()

	for _, c := range []struct {
		name, path, user, pass string
		want                   int
		who                    string
	}{
		{"без пароля", "/api/chats", "", "", http.StatusUnauthorized, ""},
		{"неверный пароль", "/api/chats", "twain", "nope", http.StatusUnauthorized, ""},
		{"чужой пароль", "/api/chats", "twain", "raft", http.StatusUnauthorized, ""},
		{"неверный логин", "/", "mark", "s3cret", http.StatusUnauthorized, ""},
		{"первый", "/api/chats", "twain", "s3cret", http.StatusOK, "twain"},
		{"второй", "/api/chats", "huck", "raft", http.StatusOK, "huck"},
		{"health открыт", "/health", "", "", http.StatusOK, ""},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+c.path, nil)
		if c.user != "" || c.pass != "" {
			req.SetBasicAuth(c.user, c.pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, ждали %d", c.name, resp.StatusCode, c.want)
		}
		if c.want == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: нет WWW-Authenticate", c.name)
		}
		if c.want == http.StatusOK && string(body) != c.who {
			t.Errorf("%s: пользователь %q, ждали %q", c.name, body, c.who)
		}
	}

	rec := httptest.NewRecorder()
	Auth([]User{{"twain", ""}}, whoami).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "twain" {
		t.Errorf("без пароля -- без защиты, от первого пользователя: %d %q", rec.Code, rec.Body.String())
	}
}

func TestParseUsers(t *testing.T) {
	users, err := ParseUsers(" twain:s3cret , huck:a:b ", "ignored", "ignored")
	if err != nil || len(users) != 2 || users[0] != (User{"twain", "s3cret"}) || users[1] != (User{"huck", "a:b"}) {
		t.Errorf("список: %v %+v", err, users)
	}
	if users, err := ParseUsers("", "twain", ""); err != nil || len(users) != 1 || users[0] != (User{"twain", ""}) {
		t.Errorf("один без пароля: %v %+v", err, users)
	}
	for _, bad := range []string{"twain", "twain:", ":pw", "twain:a,twain:b", "twain:a,"} {
		if _, err := ParseUsers(bad, "twain", ""); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
}
