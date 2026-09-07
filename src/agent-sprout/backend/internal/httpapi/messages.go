package httpapi

import (
	"net/http"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/llm"
	"agent-sprout/internal/store"
)

type postMessageRequest struct {
	Content string `json:"content"`
}

// handlePostMessage -- главный маршрут: вопрос пользователя проходит через агента
// и превращается в ответ с метриками.
//
// Порядок важен. Вопрос попадает в ленту до вызова агента, поэтому даже отклонённый
// входной политикой запрос остаётся в истории вместе с объяснением, почему он отклонён.
func (d Deps) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	var body postMessageRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	chatID := r.PathValue("id")
	chat, err := d.Store.Get(chatID)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// история берётся до добавления нового вопроса: сам вопрос агент получает отдельно
	history := chat.History()

	chat, err = d.Store.Append(chatID, store.Message{
		Role:    llm.RoleUser,
		Kind:    store.KindQuestion,
		Content: body.Content,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	out, runErr := d.Agent.Run(r.Context(), agent.RunInput{
		Question: body.Content,
		History:  history,
		Config:   chat.Config,
	})

	if runErr != nil {
		status, code := classify(runErr)

		// отказ политики и сбой провайдера остаются в ленте: пользователь должен видеть,
		// что стало с его вопросом, а не пустоту
		kind := store.KindFailed
		if code == codeInputPolicy || code == codeOutputPolicy {
			kind = store.KindBlocked
		}

		meta := store.MetaFrom(out)
		chat, err = d.Store.Append(chatID, store.Message{
			Role:    llm.RoleAssistant,
			Kind:    kind,
			Content: runErr.Error(),
			Meta:    meta,
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeErrorWithChat(w, status, code, runErr.Error(), chat)
		return
	}

	chat, err = d.Store.Append(chatID, store.Message{
		Role:    llm.RoleAssistant,
		Kind:    store.KindAnswer,
		Content: out.Answer,
		Meta:    store.MetaFrom(out),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"chat":    chat,
		"message": chat.Messages[len(chat.Messages)-1],
	})
}
