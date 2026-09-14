package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/store"
)

// catalogModel -- модель каталога плюс признак доступности: без ключа провайдера
// вариант показывается, но выбрать его нельзя.
type catalogModel struct {
	agent.Model
	Available bool `json:"available"`
}

// handleCatalog отдаёт справочник, по которому интерфейс строит панель настроек:
// модели с ценами и потолками вывода плюс значения по умолчанию. Так константы
// живут в одном месте -- на бэкенде.
func (d Deps) handleCatalog(w http.ResponseWriter, r *http.Request) {
	models := make([]catalogModel, 0, len(agent.Models))
	for _, model := range agent.Models {
		models = append(models, catalogModel{Model: model, Available: d.Agent.Available(model)})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models": models,
		"defaults": map[string]any{
			"config": agent.DefaultConfig(d.DefaultModel),
		},
	})
}

// chatPayload -- чат вместе с состоянием окна контекста.
//
// Отдаются всегда парой: интерфейсу нужно знать не только историю, но и сколько
// места в окне модели осталось под следующий вопрос.
func (d Deps) chatPayload(chat store.Chat) map[string]any {
	return map[string]any{
		"chat":    chat,
		"context": agent.ContextFor(chat.Config, chat.LastTurn()),
	}
}

func (d Deps) handleListChats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"chats": d.Store.List()})
}

// createChatRequest -- тело POST /api/chats. Настройки необязательны: без них
// берутся значения по умолчанию.
type createChatRequest struct {
	Title  string       `json:"title"`
	Config *configPatch `json:"config"`
}

func (d Deps) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	var body createChatRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	cfg := agent.DefaultConfig(d.DefaultModel)
	if body.Config != nil {
		cfg = body.Config.apply(cfg)
		// бюджет вывода всегда от модели, а не от модели по умолчанию: у бесплатной
		// потолок в двадцать раз ниже, и чужое значение её не прошло бы
		cfg.MaxTokens = agent.MaxTokensFor(cfg.Model)
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "Новый чат"
	}

	chat, err := d.Store.Create(title, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, d.chatPayload(chat))
}

func (d Deps) handleGetChat(w http.ResponseWriter, r *http.Request) {
	chat, err := d.Store.Get(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d.chatPayload(chat))
}

// patchChatRequest -- тело PATCH /api/chats/{id}. Оба поля необязательны:
// можно переименовать чат, не трогая настройки, и наоборот.
type patchChatRequest struct {
	Title  *string      `json:"title"`
	Config *configPatch `json:"config"`
}

func (d Deps) handlePatchChat(w http.ResponseWriter, r *http.Request) {
	var body patchChatRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	var invalid error
	chat, err := d.Store.Update(r.PathValue("id"), func(chat *store.Chat) error {
		if body.Title != nil {
			title := strings.TrimSpace(*body.Title)
			if title == "" {
				invalid = errors.New("название чата не может быть пустым")
				return invalid
			}
			chat.Title = title
		}
		if body.Config != nil {
			// не присваиваем напрямую: ApplyConfig достраивает выводимые настройки
			// и проверяет результат -- иначе смена модели ломалась бы на проверке
			// старого бюджета вывода
			if err := chat.ApplyConfig(body.Config.apply(chat.Config)); err != nil {
				invalid = err
				return err
			}
		}
		return nil
	})

	if invalid != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, invalid.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, d.chatPayload(chat))
}

func (d Deps) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	if err := d.Store.Delete(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkpointRequest -- тело POST /api/chats/{id}/checkpoint.
type checkpointRequest struct {
	Tag string `json:"tag"`
}

// maxTagLength -- потолок длины тэга. Тэг живёт в строке списка чатов,
// и простыня там всё сломает.
const maxTagLength = 40

// handleCheckpoint делает ветку диалога: копию чата с его историей, настройками
// и памятью. Активным для интерфейса остаётся исходный чат -- ветка создаётся,
// чтобы к ней вернуться, а не чтобы немедленно в неё уйти.
func (d Deps) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	var body checkpointRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	tag := strings.TrimSpace(body.Tag)
	if tag == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тэг ветки не может быть пустым")
		return
	}
	if utf8.RuneCountInString(tag) > maxTagLength {
		writeError(w, http.StatusBadRequest, codeBadRequest,
			fmt.Sprintf("тэг длиннее %d символов не поместится в список чатов", maxTagLength))
		return
	}

	branch, err := d.Store.Clone(r.PathValue("id"), tag)
	if errors.Is(err, store.ErrTagTaken) {
		// отдельный код, чтобы форма чекпоинта показала ошибку прямо у поля
		writeError(w, http.StatusConflict, codeTagTaken, err.Error())
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, d.chatPayload(branch))
}

func (d Deps) handleClearMessages(w http.ResponseWriter, r *http.Request) {
	chat, err := d.Store.ClearMessages(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d.chatPayload(chat))
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
}

// configPatch -- частичное обновление настроек: приходит только то, что поменяли
// в интерфейсе, остальное берётся из текущей конфигурации чата. Указатели нужны,
// чтобы отличить "поле не прислали" от "прислали ноль".
type configPatch struct {
	Model       *string  `json:"model"`
	Temperature *float64 `json:"temperature"`
	// maxTokens здесь нет намеренно: бюджет вывода выводится из модели,
	// см. Chat.ApplyConfig
	HistoryDepth  *int `json:"historyDepth"`
	MaxInputChars *int `json:"maxInputChars"`
}

func (p configPatch) apply(cfg agent.Config) agent.Config {
	if p.Model != nil {
		cfg.Model = *p.Model
	}
	if p.Temperature != nil {
		cfg.Temperature = *p.Temperature
	}
	if p.HistoryDepth != nil {
		cfg.HistoryDepth = *p.HistoryDepth
	}
	if p.MaxInputChars != nil {
		cfg.MaxInputChars = *p.MaxInputChars
	}
	return cfg
}
