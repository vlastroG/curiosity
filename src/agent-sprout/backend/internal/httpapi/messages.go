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
//
// Метрики хода раскладываются на два сообщения: вход (prompt_tokens и деньги за него)
// достаётся вопросу, который этот вызов породил, выход -- ответу модели. Так под каждым
// пузырём стоит своё число, и складывать соседние больше не нужно.
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

	// окно истории и числа прошлого вызова берутся до добавления нового вопроса:
	// сам вопрос агент получает отдельно
	window := chat.Window()
	last := chat.LastTurn()

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
		History:  window.Messages,
		Summary:  window.Summary,
		Facts:    chat.Facts,
		Config:   chat.Config,
		Last:     last,
	})

	if runErr != nil {
		status, code := classify(runErr)

		// отказ политики и сбой провайдера остаются в ленте: пользователь должен видеть,
		// что стало с его вопросом, а не пустоту
		kind := store.KindFailed
		switch code {
		case codeInputPolicy, codeOutputPolicy:
			kind = store.KindBlocked
		case codeOverflow:
			kind = store.KindOverflow
		}

		// вход оплачен только если вызов состоялся: входная политика режет до него
		var input *store.InputMeta
		if out.Calls > 0 {
			input = store.InputFrom(out)
		}

		// сжатие могло удаться до того, как упал основной вызов: работа уже оплачена,
		// и повтор не должен платить за неё второй раз
		chat, err = d.Store.FinishTurn(chatID, store.Turn{
			Boundary: store.CompactionMessage(out.Compaction, out.Model),
			Input:    input,
			Facts:    updatedFacts(out),
			Answer: store.Message{
				Role:    llm.RoleAssistant,
				Kind:    kind,
				Content: runErr.Error(),
				Meta:    store.MetaFrom(out),
			},
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeErrorWithChat(w, status, code, runErr.Error(), d.chatPayload(chat))
		return
	}

	chat, err = d.Store.FinishTurn(chatID, store.Turn{
		Boundary: store.CompactionMessage(out.Compaction, out.Model),
		Input:    store.InputFrom(out),
		Facts:    updatedFacts(out),
		Answer: store.Message{
			Role:    llm.RoleAssistant,
			Kind:    store.KindAnswer,
			Content: out.Answer,
			Meta:    store.MetaFrom(out),
		},
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	payload := d.chatPayload(chat)
	payload["message"] = chat.Messages[len(chat.Messages)-1]
	writeJSON(w, http.StatusOK, payload)
}

// updatedFacts достаёт обновлённую память из результата хода.
//
// nil означает «память не трогать»: факты выключены либо их обновление не удалось.
func updatedFacts(out agent.RunOutput) []agent.Fact {
	if out.Facts == nil {
		return nil
	}
	return out.Facts.Facts
}
