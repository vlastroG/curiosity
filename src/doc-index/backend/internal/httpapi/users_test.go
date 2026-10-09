package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"doc-index/internal/chat"
	"doc-index/internal/httpapi"
	"doc-index/internal/store"
)

// У каждого пользователя свои чаты: чужой не виден в списке и недоступен по номеру.
func TestChatsPerUser(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	chats, err := chat.Open(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	defaults := chat.Settings{CompressAfter: 12}
	h := httpapi.New(httpapi.Config{Store: st, Chats: &chat.Service{Store: chats}, ChatDefaults: defaults, StaticDir: t.TempDir()})
	srv := httptest.NewServer(httpapi.Auth([]httpapi.User{{Name: "twain", Password: "s3cret"}, {Name: "huck", Password: "raft"}}, h))
	defer srv.Close()

	do := func(user, method, path, body string, out any) int {
		t.Helper()
		pass := map[string]string{"twain": "s3cret", "huck": "raft"}[user]
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.SetBasicAuth(user, pass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	list := func(user string) []int64 {
		t.Helper()
		var out struct{ Chats []chat.Chat }
		do(user, http.MethodGet, "/api/chats", "", &out)
		ids := []int64{}
		for _, c := range out.Chats {
			ids = append(ids, c.ID)
		}
		return ids
	}

	var mine chatResp
	if code := do("twain", http.MethodPost, "/api/chats", "", &mine); code != http.StatusCreated {
		t.Fatalf("создание: %d", code)
	}
	path := fmt.Sprintf("/api/chats/%d", mine.Chat.ID)

	if got := list("twain"); len(got) != 1 || got[0] != mine.Chat.ID {
		t.Errorf("свои чаты: %v", got)
	}
	if got := list("huck"); len(got) != 0 {
		t.Errorf("чужие чаты в списке: %v", got)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, path, ""},
		{http.MethodPatch, path, `{"title":"чужое"}`},
		{http.MethodPut, path + "/state", `{"topic":"чужое","theses":[],"open":[]}`},
		{http.MethodPost, path + "/messages", `{"text":"привет"}`},
		{http.MethodDelete, path, ""},
	} {
		code := do("huck", c.method, c.path, c.body, nil)
		// сообщение без модели -- 503 раньше проверки чата; главное -- не 2xx
		if code < 400 {
			t.Errorf("%s %s чужого чата: %d", c.method, c.path, code)
		}
	}
	var after chatResp
	if code := do("twain", http.MethodGet, path, "", &after); code != http.StatusOK || after.Chat.Title != chat.DefaultTitle {
		t.Errorf("свой чат после попыток чужого: %d %q", code, after.Chat.Title)
	}
}
