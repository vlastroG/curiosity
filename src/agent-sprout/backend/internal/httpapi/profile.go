package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"agent-sprout/internal/store"
)

// Персонализация: профиль пользователя.
//
// Маршрутов два, и оба без идентификатора: профиль в приложении ровно один,
// как и пользователь у него.

const (
	// maxProfileField -- потолок на одно поле профиля
	maxProfileField = 2000
	// maxProfileTotal -- потолок на профиль целиком. Он уезжает в КАЖДЫЙ запрос,
	// поэтому простыня здесь -- молчаливый налог на каждый ход, а не разовая трата
	maxProfileTotal = 4000
)

type profileRequest struct {
	About  string `json:"about"`
	City   string `json:"city"`
	Style  string `json:"style"`
	Format string `json:"format"`
	Limits string `json:"limits"`
}

func (d Deps) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	// заготовки отдаются рядом с профилем, но сами в него не попадают:
	// что о себе рассказать, решает пользователь
	writeJSON(w, http.StatusOK, map[string]any{
		"profile": d.Store.Profile(),
		"presets": store.ProfilePresets,
	})
}

func (d Deps) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	var body profileRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, "тело запроса не разобралось: "+err.Error())
		return
	}

	profile := store.Profile{
		About:  strings.TrimSpace(body.About),
		City:   strings.TrimSpace(body.City),
		Style:  strings.TrimSpace(body.Style),
		Format: strings.TrimSpace(body.Format),
		Limits: strings.TrimSpace(body.Limits),
	}
	if err := validProfile(profile); err != nil {
		writeError(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}

	saved, err := d.Store.SetProfile(profile)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"profile": saved,
		"presets": store.ProfilePresets,
	})
}

func validProfile(p store.Profile) error {
	fields := map[string]string{
		"«о себе»":      p.About,
		"«город»":       p.City,
		"«стиль»":       p.Style,
		"«формат»":      p.Format,
		"«ограничения»": p.Limits,
	}

	total := 0
	for name, value := range fields {
		length := utf8.RuneCountInString(value)
		if length > maxProfileField {
			return fmt.Errorf("поле %s длиннее %d символов", name, maxProfileField)
		}
		total += length
	}
	if total > maxProfileTotal {
		return fmt.Errorf("профиль длиннее %d символов: он уезжает в каждый запрос, "+
			"поэтому держите его коротким", maxProfileTotal)
	}
	return nil
}
