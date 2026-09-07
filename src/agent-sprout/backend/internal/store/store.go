package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agent-sprout/internal/agent"
)

// ErrNotFound -- чата с таким id нет.
var ErrNotFound = errors.New("чат не найден")

// Store -- потокобезопасное хранилище чатов с записью снапшота на диск.
type Store struct {
	mu    sync.RWMutex
	chats map[string]*Chat
	// order хранит порядок создания: map порядка не даёт, а список чатов
	// должен быть стабильным между запросами
	order []string
	path  string
	now   func() time.Time
}

// snapshot -- формат файла на диске.
type snapshot struct {
	Chats []Chat `json:"chats"`
}

// Open поднимает хранилище из файла. Отсутствующий файл -- не ошибка: это первый запуск.
// Пустой path отключает запись на диск (используется в тестах).
func Open(path string) (*Store, error) {
	s := &Store{
		chats: map[string]*Chat{},
		path:  path,
		now:   time.Now,
	}

	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}

	var loaded snapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("разбор %s: %w", path, err)
	}

	for i := range loaded.Chats {
		chat := loaded.Chats[i]
		s.chats[chat.ID] = &chat
		s.order = append(s.order, chat.ID)
	}

	return s, nil
}

// List возвращает сводки по всем чатам в порядке создания.
func (s *Store) List() []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]Summary, 0, len(s.order))
	for _, id := range s.order {
		if chat, ok := s.chats[id]; ok {
			list = append(list, chat.summary())
		}
	}
	return list
}

// Get возвращает копию чата целиком.
func (s *Store) Get(id string) (Chat, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	chat, ok := s.chats[id]
	if !ok {
		return Chat{}, ErrNotFound
	}
	return chat.clone(), nil
}

// Create заводит новый чат с уже проверенными настройками.
func (s *Store) Create(title string, cfg agent.Config) (Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	chat := &Chat{
		ID:        newID(),
		Title:     title,
		Config:    cfg,
		Messages:  []Message{},
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.chats[chat.ID] = chat
	s.order = append(s.order, chat.ID)

	if err := s.persist(); err != nil {
		return Chat{}, err
	}
	return chat.clone(), nil
}

// Update меняет чат под замком и сразу сохраняет снапшот.
//
// Мутатор -- единственный способ изменить чат: так все изменения проходят через одну
// точку, и запись на диск невозможно забыть.
func (s *Store) Update(id string, mutate func(*Chat) error) (Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chat, ok := s.chats[id]
	if !ok {
		return Chat{}, ErrNotFound
	}

	if err := mutate(chat); err != nil {
		return Chat{}, err
	}
	chat.UpdatedAt = s.now()

	if err := s.persist(); err != nil {
		return Chat{}, err
	}
	return chat.clone(), nil
}

// Append добавляет сообщения в конец ленты, проставляя им id и время.
func (s *Store) Append(id string, messages ...Message) (Chat, error) {
	return s.Update(id, func(chat *Chat) error {
		now := s.now()
		for _, message := range messages {
			message.ID = newID()
			message.CreatedAt = now
			chat.Messages = append(chat.Messages, message)
		}
		return nil
	})
}

// ClearMessages очищает историю, сохраняя настройки чата.
func (s *Store) ClearMessages(id string) (Chat, error) {
	return s.Update(id, func(chat *Chat) error {
		chat.Messages = []Message{}
		return nil
	})
}

// Delete удаляет чат вместе с историей.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.chats[id]; !ok {
		return ErrNotFound
	}
	delete(s.chats, id)

	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}

	return s.persist()
}

// persist переписывает снапшот целиком. Вызывается только под уже взятым замком.
//
// Запись идёт во временный файл рядом и потом переименовывается: при падении посреди
// записи на диске останется предыдущая целая версия, а не обрезанный файл.
func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}

	all := snapshot{Chats: make([]Chat, 0, len(s.order))}
	for _, id := range s.order {
		if chat, ok := s.chats[id]; ok {
			all.Chats = append(all.Chats, *chat)
		}
	}

	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("сериализация снапшота: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("создание %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "chats-*.tmp")
	if err != nil {
		return fmt.Errorf("временный файл в %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("запись снапшота: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("закрытие снапшота: %w", err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("замена %s: %w", s.path, err)
	}
	return nil
}

// newID -- 16 случайных байт в hex. Коллизий на этих объёмах не бывает,
// а порядок сортировки нам даёт отдельный список order.
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand на поддерживаемых платформах не возвращает ошибку;
		// если это случилось, продолжать работу нельзя
		panic(fmt.Sprintf("генератор случайных чисел недоступен: %v", err))
	}
	return hex.EncodeToString(buf)
}
