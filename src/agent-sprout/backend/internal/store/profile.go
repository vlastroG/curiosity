package store

import (
	"strings"
	"time"

	"agent-sprout/internal/agent"
)

// Персонализация — профиль пользователя.
//
// Второй объект, который живёт вне чатов, и по тем же причинам, что справочник знаний:
// «объясняй простыми словами» и «у меня нет болгарки» не принадлежат какому-то одному
// разговору. Профиль один на всё приложение: пользователь тут ровно один.

// Profile -- предпочтения пользователя. Каждое поле -- просто текст.
type Profile struct {
	About  string `json:"about"`
	Style  string `json:"style"`
	Format string `json:"format"`
	Limits string `json:"limits"`
	// UpdatedAt пусто, пока профиль ни разу не сохраняли
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Agent -- профиль в том виде, в каком его видит агент: без служебных дат.
func (p Profile) Agent() agent.Profile {
	return agent.Profile{About: p.About, Style: p.Style, Format: p.Format, Limits: p.Limits}
}

// Profile возвращает текущий профиль.
func (s *Store) Profile() Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.profile
}

// SetProfile заменяет профиль целиком.
//
// Именно целиком, а не по полям: профиль редактируется одной формой с одной кнопкой,
// и частичное обновление означало бы, что очистить поле нельзя.
func (s *Store) SetProfile(profile Profile) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	profile.About = strings.TrimSpace(profile.About)
	profile.Style = strings.TrimSpace(profile.Style)
	profile.Format = strings.TrimSpace(profile.Format)
	profile.Limits = strings.TrimSpace(profile.Limits)
	profile.UpdatedAt = &now

	s.profile = profile
	if err := s.persist(); err != nil {
		return Profile{}, err
	}
	return s.profile, nil
}
