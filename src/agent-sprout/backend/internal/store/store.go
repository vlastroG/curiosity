package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-sprout/internal/agent"
)

// ErrNotFound -- чата с таким id нет.
var ErrNotFound = errors.New("чат не найден")

// ErrTagTaken -- такой тэг ветки уже занят. Тэги должны быть уникальными:
// по ним ветки различают глазами.
var ErrTagTaken = errors.New("такой тэг уже занят")

// Store -- потокобезопасное хранилище чатов с записью снапшота на диск.
type Store struct {
	mu    sync.RWMutex
	chats map[string]*Chat
	// order хранит порядок создания: map порядка не даёт, а список чатов
	// должен быть стабильным между запросами
	order []string
	// knowledge -- долговременная память: общая для всех чатов и живёт отдельно
	// от них, потому что не принадлежит ни одному разговору
	knowledge []Knowledge
	path      string
	now       func() time.Time
}

// snapshot -- формат файла на диске.
type snapshot struct {
	Chats     []Chat      `json:"chats"`
	Knowledge []Knowledge `json:"knowledge,omitempty"`
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
	s.knowledge = loaded.Knowledge

	return s, nil
}

// Now -- часы хранилища. Вынесены наружу, чтобы вызывающий проставлял время теми же
// часами, что и сам стор, и тесты могли их подменить.
func (s *Store) Now() time.Time { return s.now() }

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

// Turn -- всё, что появилось в чате за один ход агента.
type Turn struct {
	// Boundary -- отметка о сжатии или отбрасывании истории. Встаёт в ленту
	// ПЕРЕД вопросом, который вызвал переход: именно так проходит граница окна
	Boundary *Message
	// Input -- метрики входа, достаются вопросу
	Input *InputMeta
	// Answer -- ответ модели либо объяснение, почему его нет
	Answer Message
	// Task -- состояние задачи после хода. nil означает «не трогать»:
	// задача могла не завестись или ход мог закончиться отказом
	Task *agent.Task
}

// FinishTurn закрывает ход одной операцией.
//
// Вход и выход -- две стороны одного вызова, разъехаться они не должны; отметка
// о границе обязана встать перед своим вопросом, иначе окно съедет и свёрнутые
// сообщения снова уедут в модель. Поэтому всё под одним замком и с одной записью
// снапшота.
func (s *Store) FinishTurn(id string, turn Turn) (Chat, error) {
	return s.Update(id, func(chat *Chat) error {
		now := s.now()

		question := lastIndexOfKind(chat.Messages, KindQuestion)

		if turn.Input != nil && question >= 0 {
			chat.Messages[question].Input = turn.Input
		}

		if turn.Boundary != nil {
			boundary := *turn.Boundary
			boundary.ID = newID()
			boundary.CreatedAt = now

			at := question
			if at < 0 {
				at = len(chat.Messages)
			}
			chat.Messages = append(chat.Messages, Message{})
			copy(chat.Messages[at+1:], chat.Messages[at:])
			chat.Messages[at] = boundary
		}

		if turn.Task != nil {
			chat.UpsertTask(*turn.Task)
		}

		turn.Answer.ID = newID()
		turn.Answer.CreatedAt = now
		chat.Messages = append(chat.Messages, turn.Answer)
		return nil
	})
}

func lastIndexOfKind(messages []Message, kind string) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Kind == kind {
			return i
		}
	}
	return -1
}

// ClearMessages очищает историю, сохраняя настройки чата.
//
// Вместе с лентой уходят оба слоя памяти чата: задачи с их рабочей памятью стираются
// явно, пересказ окна -- сам, потому что хранится отметкой среди сообщений.
// Долговременная память не трогается: она общая и живёт вне чатов.
func (s *Store) ClearMessages(id string) (Chat, error) {
	return s.Update(id, func(chat *Chat) error {
		chat.Messages = []Message{}
		chat.Tasks = nil
		return nil
	})
}

// Clone создаёт ветку диалога: абсолютную копию чата в новой точке.
//
// История, настройки и память переезжают целиком -- меняются только идентификатор
// и метки ветки. Тэг проверяется под тем же замком, что и вставка: иначе два
// одновременных чекпоинта могут занять один и тот же тэг.
func (s *Store) Clone(id, tag string) (Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	source, ok := s.chats[id]
	if !ok {
		return Chat{}, ErrNotFound
	}

	for _, existing := range s.chats {
		if strings.EqualFold(existing.Tag, tag) {
			return Chat{}, ErrTagTaken
		}
	}

	now := s.now()
	branch := source.clone()
	branch.ID = newID()
	branch.Tag = tag
	branch.ClonedAt = &now
	branch.ParentID = source.ID
	branch.CreatedAt = now
	branch.UpdatedAt = now

	s.chats[branch.ID] = &branch
	s.order = append(s.order, branch.ID)

	if err := s.persist(); err != nil {
		return Chat{}, err
	}
	return branch.clone(), nil
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

	all := snapshot{
		Chats:     make([]Chat, 0, len(s.order)),
		Knowledge: s.knowledge,
	}
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
