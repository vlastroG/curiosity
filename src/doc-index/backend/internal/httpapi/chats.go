package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"doc-index/internal/chat"
	"doc-index/internal/rag"
	"doc-index/internal/search"
)

// chatID -- номер чата из пути; чужой чат -- как несуществующий.
func (a *API) chatID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, chat.ErrNotFound
	}
	if err := a.Chats.Store.Owns(r.Context(), id, UserFrom(r.Context())); err != nil {
		return 0, err
	}
	return id, nil
}

// chatError -- 404 для несуществующего чата, иначе 400.
func chatError(w http.ResponseWriter, err error) {
	if errors.Is(err, chat.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("неверный запрос"))
		return false
	}
	return true
}

func (a *API) listChats(w http.ResponseWriter, r *http.Request) {
	list, err := a.Chats.Store.List(r.Context(), UserFrom(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": list})
}

// createChat -- новый чат пользователя с настройками по умолчанию.
func (a *API) createChat(w http.ResponseWriter, r *http.Request) {
	c, err := a.Chats.Store.Create(r.Context(), UserFrom(r.Context()), a.ChatDefaults)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"chat": c})
}

func (a *API) getChat(w http.ResponseWriter, r *http.Request) {
	id, err := a.chatID(r)
	if err != nil {
		chatError(w, err)
		return
	}
	c, err := a.Chats.Store.Get(r.Context(), id)
	if err != nil {
		chatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": c})
}

// patchChat -- название и/или настройки.
func (a *API) patchChat(w http.ResponseWriter, r *http.Request) {
	id, err := a.chatID(r)
	if err != nil {
		chatError(w, err)
		return
	}
	var req struct {
		Title    *string        `json:"title"`
		Settings *chat.Settings `json:"settings"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Settings != nil {
		if err := a.Chats.Store.SetSettings(r.Context(), id, *req.Settings); err != nil {
			chatError(w, err)
			return
		}
	}
	if req.Title != nil {
		if err := a.Chats.Store.Rename(r.Context(), id, *req.Title); err != nil {
			chatError(w, err)
			return
		}
	}
	a.getChat(w, r)
}

func (a *API) deleteChat(w http.ResponseWriter, r *http.Request) {
	id, err := a.chatID(r)
	if err != nil {
		chatError(w, err)
		return
	}
	if err := a.Chats.Store.Delete(r.Context(), id); err != nil {
		chatError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// putState -- память задачи целиком, как её поправил пользователь.
func (a *API) putState(w http.ResponseWriter, r *http.Request) {
	id, err := a.chatID(r)
	if err != nil {
		chatError(w, err)
		return
	}
	var st rag.TaskState
	if !decode(w, r, &st) {
		return
	}
	if err := a.Chats.Store.SetState(r.Context(), id, st); err != nil {
		chatError(w, err)
		return
	}
	a.getChat(w, r)
}

// sendMessage -- реплика пользователя; в ответ -- чат целиком.
func (a *API) sendMessage(w http.ResponseWriter, r *http.Request) {
	if a.Agent == nil || a.LLMError != nil {
		msg := "модель ответов не настроена"
		if a.LLMError != nil {
			msg = a.LLMError.Error()
		}
		writeError(w, http.StatusServiceUnavailable, errors.New(msg))
		return
	}
	id, err := a.chatID(r)
	if err != nil {
		chatError(w, err)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := a.Searcher.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(a.Searcher.Chunks(a.Agent.Variant)) == 0 {
		writeError(w, http.StatusConflict, search.ErrEmptyIndex)
		return
	}
	c, err := a.Chats.Send(r.Context(), id, req.Text)
	if err != nil {
		chatError(w, err)
		return
	}
	// только GPU: эмбеддинги и реранкер загружаются первыми запросами
	for _, model := range []string{a.Searcher.Variants[0].Model, a.Agent.Reranker.Name()} {
		if err := a.requireGPU(r.Context(), model); err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": c})
}
