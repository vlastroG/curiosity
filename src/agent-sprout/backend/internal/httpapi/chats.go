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
// модели с ценами, пресеты system prompt и значения по умолчанию. Так константы
// живут в одном месте -- на бэкенде.
func (d Deps) handleCatalog(w http.ResponseWriter, r *http.Request) {
	models := make([]catalogModel, 0, len(agent.Models))
	for _, model := range agent.Models {
		models = append(models, catalogModel{Model: model, Available: d.Agent.Available(model)})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models":  models,
		"presets": agent.Presets,
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
			updated := body.Config.apply(chat.Config)
			if err := updated.Validate(); err != nil {
				invalid = err
				return err
			}
			// не присваиваем напрямую: снятая галочка фактов должна стереть
			// накопленную память, и это правило живёт в одном месте
			chat.ApplyConfig(updated)
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
	Model            *string  `json:"model"`
	SystemPrompt     *string  `json:"systemPrompt"`
	Temperature      *float64 `json:"temperature"`
	MaxTokens        *int     `json:"maxTokens"`
	TopP             *float64 `json:"topP"`
	FrequencyPenalty *float64 `json:"frequencyPenalty"`
	PresencePenalty  *float64 `json:"presencePenalty"`
	ResponseFormat   *string  `json:"responseFormat"`
	MaxWords         *int     `json:"maxWords"`
	HistoryDepth     *int     `json:"historyDepth"`
	SummarizeHistory *bool    `json:"summarizeHistory"`
	StickyFacts      *bool    `json:"stickyFacts"`
	JudgeEnabled     *bool    `json:"judgeEnabled"`
	MaxInputChars    *int     `json:"maxInputChars"`
}

func (p configPatch) apply(cfg agent.Config) agent.Config {
	if p.Model != nil {
		cfg.Model = *p.Model
	}
	if p.SystemPrompt != nil {
		cfg.SystemPrompt = *p.SystemPrompt
	}
	if p.Temperature != nil {
		cfg.Temperature = *p.Temperature
	}
	if p.MaxTokens != nil {
		cfg.MaxTokens = *p.MaxTokens
	}
	if p.TopP != nil {
		cfg.TopP = *p.TopP
	}
	if p.FrequencyPenalty != nil {
		cfg.FrequencyPenalty = *p.FrequencyPenalty
	}
	if p.PresencePenalty != nil {
		cfg.PresencePenalty = *p.PresencePenalty
	}
	if p.ResponseFormat != nil {
		cfg.ResponseFormat = *p.ResponseFormat
	}
	if p.MaxWords != nil {
		cfg.MaxWords = *p.MaxWords
	}
	if p.HistoryDepth != nil {
		cfg.HistoryDepth = *p.HistoryDepth
	}
	if p.SummarizeHistory != nil {
		cfg.SummarizeHistory = *p.SummarizeHistory
	}
	if p.StickyFacts != nil {
		cfg.StickyFacts = *p.StickyFacts
	}
	if p.JudgeEnabled != nil {
		cfg.JudgeEnabled = *p.JudgeEnabled
	}
	if p.MaxInputChars != nil {
		cfg.MaxInputChars = *p.MaxInputChars
	}
	return cfg
}
