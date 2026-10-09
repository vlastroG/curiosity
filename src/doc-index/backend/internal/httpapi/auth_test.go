package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(BasicAuth("twain", "s3cret", ok))
	defer srv.Close()

	get := func(path, user, pass string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if user != "" || pass != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	for _, c := range []struct {
		name, path, user, pass string
		want                   int
	}{
		{"без пароля", "/api/chats", "", "", http.StatusUnauthorized},
		{"неверный пароль", "/api/chats", "twain", "nope", http.StatusUnauthorized},
		{"неверный логин", "/", "mark", "s3cret", http.StatusUnauthorized},
		{"верно", "/api/chats", "twain", "s3cret", http.StatusOK},
		{"health открыт", "/health", "", "", http.StatusOK},
	} {
		resp := get(c.path, c.user, c.pass)
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, ждали %d", c.name, resp.StatusCode, c.want)
		}
		if c.want == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: нет WWW-Authenticate", c.name)
		}
	}

	rec := httptest.NewRecorder()
	BasicAuth("twain", "", ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("пустой пароль -- без защиты: %d", rec.Code)
	}
}
