package store

import (
	"errors"
	"strings"
	"time"

	"agent-sprout/internal/agent"
)

// Долговременная память — справочник знаний.
//
// Третий слой модели памяти и единственный, который живёт вне чатов: правило
// «железобетон делаем по СП 63» не принадлежит ни одному разговору, оно должно
// подставляться во все задачи, где уместно. Поэтому справочник глобальный,
// заполняется руками и не чистится ни очисткой истории, ни закрытием задачи.

// ErrTitleTaken -- знание с таким заголовком уже есть.
//
// Заголовки уникальны не для красоты: именно по ним диспетчер выбирает, что
// подставить в задачу, и два одинаковых заголовка делают выбор бессмысленным.
var ErrTitleTaken = errors.New("знание с таким заголовком уже есть")

// Knowledge -- одна запись долговременной памяти.
type Knowledge struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Item -- запись в том виде, в каком её видит агент: без служебных дат.
func (k Knowledge) Item() agent.KnowledgeItem {
	return agent.KnowledgeItem{ID: k.ID, Title: k.Title, Text: k.Text}
}

// Knowledge возвращает весь справочник в порядке создания.
func (s *Store) Knowledge() []Knowledge {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Knowledge, len(s.knowledge))
	copy(list, s.knowledge)
	return list
}

// KnowledgeItems -- справочник для агента.
func (s *Store) KnowledgeItems() []agent.KnowledgeItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]agent.KnowledgeItem, 0, len(s.knowledge))
	for _, record := range s.knowledge {
		items = append(items, record.Item())
	}
	return items
}

// AddKnowledge заводит новую запись.
func (s *Store) AddKnowledge(title, text string) (Knowledge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.titleTaken(title, "") {
		return Knowledge{}, ErrTitleTaken
	}

	now := s.now()
	record := Knowledge{
		ID:        newID(),
		Title:     title,
		Text:      text,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.knowledge = append(s.knowledge, record)

	if err := s.persist(); err != nil {
		return Knowledge{}, err
	}
	return record, nil
}

// UpdateKnowledge правит заголовок и текст. Пустые значения означают «не менять».
func (s *Store) UpdateKnowledge(id, title, text string) (Knowledge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := -1
	for i, record := range s.knowledge {
		if record.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return Knowledge{}, ErrNotFound
	}

	if title != "" {
		if s.titleTaken(title, id) {
			return Knowledge{}, ErrTitleTaken
		}
		s.knowledge[index].Title = title
	}
	if text != "" {
		s.knowledge[index].Text = text
	}
	s.knowledge[index].UpdatedAt = s.now()

	if err := s.persist(); err != nil {
		return Knowledge{}, err
	}
	return s.knowledge[index], nil
}

// DeleteKnowledge убирает запись из справочника.
//
// Задачи, в которых это знание уже использовалось, не трогаются: снимок памяти
// хранит только ссылку, и в истории она просто перестанет разрешаться. Переписывать
// прошлое ради удалённой записи было бы хуже, чем показать пробел.
func (s *Store) DeleteKnowledge(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, record := range s.knowledge {
		if record.ID == id {
			s.knowledge = append(s.knowledge[:i], s.knowledge[i+1:]...)
			return s.persist()
		}
	}
	return ErrNotFound
}

// titleTaken -- занят ли заголовок кем-то, кроме записи exclude.
func (s *Store) titleTaken(title, exclude string) bool {
	for _, record := range s.knowledge {
		if record.ID != exclude && strings.EqualFold(strings.TrimSpace(record.Title), strings.TrimSpace(title)) {
			return true
		}
	}
	return false
}
