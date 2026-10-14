// Package chat -- беседы с экспертом по Твену: история сообщений, память
// задачи, сводка старой части диалога. Хранится в той же SQLite, что индекс.
package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"doc-index/internal/rag"
)

const schema = `
CREATE TABLE IF NOT EXISTS chats (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	owner           TEXT NOT NULL DEFAULT '', -- логин пользователя; у каждого свои чаты
	title           TEXT NOT NULL DEFAULT '', -- пусто -- название по теме
	created         TEXT NOT NULL,
	updated         TEXT NOT NULL,
	settings        TEXT NOT NULL, -- JSON: Settings
	state           TEXT NOT NULL, -- JSON: rag.TaskState
	summary         TEXT NOT NULL DEFAULT '',
	summarized_upto INTEGER NOT NULL DEFAULT 0 -- сообщения с номером до него включительно -- в сводке
);
CREATE TABLE IF NOT EXISTS messages (
	chat_id INTEGER NOT NULL,
	seq     INTEGER NOT NULL, -- 1, 2, … внутри чата
	role    TEXT NOT NULL,    -- user | assistant
	text    TEXT NOT NULL,
	error   TEXT NOT NULL DEFAULT '',
	result  TEXT NOT NULL DEFAULT '', -- JSON: rag.Result у ответа (поиск, отрывки)
	created TEXT NOT NULL,
	PRIMARY KEY (chat_id, seq)
);`

// Роли сообщений.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ErrNotFound -- нет такого чата.
var ErrNotFound = errors.New("чат не найден")

// MaxTitle -- длина названия чата.
const MaxTitle = 120

// Settings -- настройки чата: поиск и сжатие истории.
type Settings struct {
	rag.Settings
	CompressAfter int `json:"compressAfter"` // сжимать, когда несжатых сообщений больше
}

// Validate проверяет диапазоны.
func (s Settings) Validate() error {
	if s.CompressAfter < 4 || s.CompressAfter > 50 {
		return errors.New("сжатие: после 4–50 сообщений")
	}
	return s.Settings.Validate()
}

// Message -- сообщение чата.
type Message struct {
	Seq     int         `json:"seq"`
	Role    string      `json:"role"`
	Text    string      `json:"text"`
	Error   string      `json:"error,omitempty"`
	Created time.Time   `json:"created"`
	Result  *rag.Result `json:"result,omitempty"` // у ответа: как искали и что нашли
}

// Chat -- беседа.
type Chat struct {
	ID             int64         `json:"id"`
	Title          string        `json:"title"` // своё название или тема
	Named          bool          `json:"named"` // название задал пользователь
	Created        time.Time     `json:"created"`
	Updated        time.Time     `json:"updated"`
	Settings       Settings      `json:"settings"`
	State          rag.TaskState `json:"state"`
	Summary        string        `json:"summary"`
	SummarizedUpto int           `json:"summarizedUpto"`
	Count          int           `json:"count"`          // сообщений
	Last           string        `json:"last,omitempty"` // начало последнего сообщения -- для списка
	Messages       []Message     `json:"messages"`       // в списке чатов -- null
}

// DefaultTitle -- название чата без темы.
const DefaultTitle = "Новый чат"

func (c *Chat) title(own string) {
	c.Named = own != ""
	switch {
	case own != "":
		c.Title = own
	case c.State.Topic != "":
		c.Title = c.State.Topic
	default:
		c.Title = DefaultTitle
	}
}

// Store -- чаты в SQLite.
type Store struct{ db *sql.DB }

// Open создаёт таблицы, если их нет, и добавляет владельца в чаты старой схемы.
func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("схема чатов: %w", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('chats') WHERE name = 'owner'`).Scan(&n); err != nil {
		return nil, fmt.Errorf("схема чатов: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE chats ADD COLUMN owner TEXT NOT NULL DEFAULT ''`); err != nil {
			return nil, fmt.Errorf("владелец чатов: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// Adopt отдаёт пользователю чаты без владельца -- созданные до разделения по пользователям.
func (s *Store) Adopt(ctx context.Context, owner string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chats SET owner = ? WHERE owner = ''`, owner)
	return err
}

// Owns -- ErrNotFound, если чата нет или он чужой.
func (s *Store) Owns(ctx context.Context, id int64, owner string) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE id = ? AND owner = ?`, id, owner).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Create -- новый пустой чат пользователя owner.
func (s *Store) Create(ctx context.Context, owner string, st Settings) (*Chat, error) {
	settings, _ := json.Marshal(st)
	state, _ := json.Marshal(rag.TaskState{Theses: []rag.Item{}, Open: []rag.Item{}})
	t := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO chats (owner, created, updated, settings, state) VALUES (?, ?, ?, ?, ?)`,
		owner, t, t, string(settings), string(state))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

const chatColumns = `c.id, c.title, c.created, c.updated, c.settings, c.state, c.summary, c.summarized_upto,
	(SELECT COUNT(*) FROM messages m WHERE m.chat_id = c.id),
	COALESCE((SELECT substr(m.text, 1, 160) FROM messages m WHERE m.chat_id = c.id ORDER BY m.seq DESC LIMIT 1), '')`

func scanChat(row interface{ Scan(...any) error }) (*Chat, error) {
	var c Chat
	var title, created, updated, settings, state string
	if err := row.Scan(&c.ID, &title, &created, &updated, &settings, &state, &c.Summary, &c.SummarizedUpto,
		&c.Count, &c.Last); err != nil {
		return nil, err
	}
	c.Created, c.Updated = parseTime(created), parseTime(updated)
	if err := json.Unmarshal([]byte(settings), &c.Settings); err != nil {
		return nil, fmt.Errorf("настройки чата %d: %w", c.ID, err)
	}
	if err := json.Unmarshal([]byte(state), &c.State); err != nil {
		return nil, fmt.Errorf("память чата %d: %w", c.ID, err)
	}
	if c.State.Theses == nil {
		c.State.Theses = []rag.Item{}
	}
	if c.State.Open == nil {
		c.State.Open = []rag.Item{}
	}
	c.title(title)
	return &c, nil
}

// List -- чаты пользователя owner, свежие сверху, без сообщений.
func (s *Store) List(ctx context.Context, owner string) ([]*Chat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+chatColumns+` FROM chats c WHERE c.owner = ? ORDER BY c.updated DESC, c.id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get -- чат с сообщениями.
func (s *Store) Get(ctx context.Context, id int64) (*Chat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats c WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, role, text, error, result, created FROM messages WHERE chat_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c.Messages = []Message{}
	for rows.Next() {
		var m Message
		var result, created string
		if err := rows.Scan(&m.Seq, &m.Role, &m.Text, &m.Error, &result, &created); err != nil {
			return nil, err
		}
		m.Created = parseTime(created)
		if result != "" {
			m.Result = &rag.Result{}
			if err := json.Unmarshal([]byte(result), m.Result); err != nil {
				return nil, fmt.Errorf("сообщение %d чата %d: %w", m.Seq, id, err)
			}
		}
		c.Messages = append(c.Messages, m)
	}
	return c, rows.Err()
}

func (s *Store) update(ctx context.Context, id int64, set string, args ...any) error {
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET `+set+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Rename задаёт название; пустое -- название снова берётся из темы.
func (s *Store) Rename(ctx context.Context, id int64, title string) error {
	title = strings.Join(strings.Fields(title), " ")
	if len([]rune(title)) > MaxTitle {
		return fmt.Errorf("название длиннее %d символов", MaxTitle)
	}
	return s.update(ctx, id, `title = ?`, title)
}

// SetSettings меняет настройки чата.
func (s *Store) SetSettings(ctx context.Context, id int64, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	return s.update(ctx, id, `settings = ?`, string(b))
}

// SetState сохраняет память задачи.
func (s *Store) SetState(ctx context.Context, id int64, st rag.TaskState) error {
	if err := st.Validate(); err != nil {
		return err
	}
	st.Topic = strings.TrimSpace(st.Topic)
	b, _ := json.Marshal(st)
	return s.update(ctx, id, `state = ?`, string(b))
}

// Delete удаляет чат со всеми сообщениями.
func (s *Store) Delete(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE chat_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// appendTurn сохраняет реплику, ответ и новую память задачи одной транзакцией.
func (s *Store) appendTurn(ctx context.Context, id int64, user, reply Message, st rag.TaskState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := now()
	for _, m := range []Message{user, reply} {
		var result string
		if m.Result != nil {
			b, _ := json.Marshal(m.Result)
			result = string(b)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO messages (chat_id, seq, role, text, error, result, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, m.Seq, m.Role, m.Text, m.Error, result, t); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(st)
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET state = ?, updated = ? WHERE id = ?`, string(b), t, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) setSummary(ctx context.Context, id int64, summary string, upto int) error {
	return s.update(ctx, id, `summary = ?, summarized_upto = ?`, summary, upto)
}
