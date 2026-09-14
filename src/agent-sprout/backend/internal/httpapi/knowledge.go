package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"agent-sprout/internal/store"
)

// Долговременная память: справочник знаний.
//
// Единственный слой памяти со своим набором маршрутов — потому что единственный,
// который живёт вне чатов и заполняется руками, а не выводится из разговора.

const (
	maxKnowledgeTitle = 80
	maxKnowledgeText  = 20000
)

type knowledgeRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (d Deps) handleListKnowledge(w http.ResponseWriter, r *http.Request) {
	// заготовки отдаются рядом со справочником, но в него не попадают сами:
	// что лежит в долговременной памяти, решает пользователь
	writeJSON(w, http.StatusOK, map[string]any{
		"knowledge": d.Store.Knowledge(),
		"presets":   store.KnowledgePresets,
	})
}

func (d Deps) handleCreateKnowledge(w http.ResponseWriter, r *http.Request) {
	var body knowledgeRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	title, text, err := validKnowledge(body, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	record, err := d.Store.AddKnowledge(title, text)
	if errors.Is(err, store.ErrTitleTaken) {
		// отдельный код, чтобы форма показала ошибку прямо у поля заголовка
		writeError(w, http.StatusConflict, codeTitleTaken, err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"item": record})
}

func (d Deps) handleUpdateKnowledge(w http.ResponseWriter, r *http.Request) {
	var body knowledgeRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	title, text, err := validKnowledge(body, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	record, err := d.Store.UpdateKnowledge(r.PathValue("id"), title, text)
	if errors.Is(err, store.ErrTitleTaken) {
		writeError(w, http.StatusConflict, codeTitleTaken, err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"item": record})
}

func (d Deps) handleDeleteKnowledge(w http.ResponseWriter, r *http.Request) {
	if err := d.Store.DeleteKnowledge(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCancelTask прерывает активную задачу чата.
//
// Нужен правилу «один вид работ за раз»: если агент отказывается говорить о втором
// виде работ, у человека должен быть способ закрыть первый, не бросая чат целиком.
func (d Deps) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	var cancelled bool

	chat, err := d.Store.Update(r.PathValue("id"), func(chat *store.Chat) error {
		cancelled = chat.CancelTask(d.Store.Now())
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !cancelled {
		writeError(w, http.StatusConflict, codeNoActiveTask, "в этом чате нет активной задачи")
		return
	}

	writeJSON(w, http.StatusOK, d.chatPayload(chat))
}

// validKnowledge проверяет тело запроса. При правке пустые поля означают «не менять».
func validKnowledge(body knowledgeRequest, required bool) (string, string, error) {
	title := strings.TrimSpace(body.Title)
	text := strings.TrimSpace(body.Text)

	if required && (title == "" || text == "") {
		return "", "", errors.New("у знания должны быть и заголовок, и текст")
	}
	if utf8.RuneCountInString(title) > maxKnowledgeTitle {
		return "", "", fmt.Errorf("заголовок длиннее %d символов не поместится в список", maxKnowledgeTitle)
	}
	if utf8.RuneCountInString(text) > maxKnowledgeText {
		return "", "", fmt.Errorf("текст знания длиннее %d символов съест окно контекста", maxKnowledgeText)
	}

	return title, text, nil
}
