package main

// Хранилище тикетов -- SQLite в одном файле, полнотекстовый поиск -- FTS5.
//
// tickets_fts -- внешнее содержимое (content='tickets'): тексты не дублируются,
// индекс держат в согласии триггеры. Токенизатор unicode61 приводит к нижнему
// регистру и кириллицу, поэтому «Вход» и «вход» -- одно слово.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tickets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	title       TEXT    NOT NULL,
	description TEXT    NOT NULL,
	status      TEXT    NOT NULL DEFAULT 'open',
	resolution  TEXT    NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS tickets_fts USING fts5(
	title, description, resolution, content='tickets', content_rowid='id', tokenize='unicode61'
);
CREATE TRIGGER IF NOT EXISTS tickets_ai AFTER INSERT ON tickets BEGIN
	INSERT INTO tickets_fts(rowid, title, description, resolution) VALUES (new.id, new.title, new.description, new.resolution);
END;
CREATE TRIGGER IF NOT EXISTS tickets_ad AFTER DELETE ON tickets BEGIN
	INSERT INTO tickets_fts(tickets_fts, rowid, title, description, resolution) VALUES ('delete', old.id, old.title, old.description, old.resolution);
END;
CREATE TRIGGER IF NOT EXISTS tickets_au AFTER UPDATE ON tickets BEGIN
	INSERT INTO tickets_fts(tickets_fts, rowid, title, description, resolution) VALUES ('delete', old.id, old.title, old.description, old.resolution);
	INSERT INTO tickets_fts(rowid, title, description, resolution) VALUES (new.id, new.title, new.description, new.resolution);
END;
`

// Statuses -- допустимые статусы тикета.
var Statuses = []string{"open", "in_progress", "resolved"}

// ErrNotFound -- тикета с таким номером нет.
var ErrNotFound = errors.New("не найден")

// Ticket -- тикет в том виде, в каком его видят клиенты.
type Ticket struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status" jsonschema:"open, in_progress или resolved"`
	Resolution  string `json:"resolution"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// Store -- доступ к базе.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// openStore открывает файл базы и создаёт таблицы, если их нет.
func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("схема базы не создалась: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Create сохраняет новый тикет со статусом open.
func (s *Store) Create(ctx context.Context, title, description string) (Ticket, error) {
	at := s.now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tickets(title, description, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		title, description, at, at)
	if err != nil {
		return Ticket{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Ticket{}, err
	}
	return s.Get(ctx, id)
}

const columns = `t.id, t.title, t.description, t.status, t.resolution, t.created_at, t.updated_at`

// Get -- тикет по номеру; ErrNotFound, если его нет.
func (s *Store) Get(ctx context.Context, id int64) (Ticket, error) {
	tickets, err := s.query(ctx, `SELECT `+columns+` FROM tickets t WHERE t.id = ?`, id)
	if err != nil {
		return Ticket{}, err
	}
	if len(tickets) == 0 {
		return Ticket{}, fmt.Errorf("тикет #%d %w", id, ErrNotFound)
	}
	return tickets[0], nil
}

// List -- тикеты, свежие первыми; status == "" -- все.
func (s *Store) List(ctx context.Context, status string, limit int) ([]Ticket, error) {
	return s.query(ctx, `SELECT `+columns+` FROM tickets t WHERE ? = '' OR t.status = ? ORDER BY t.id DESC LIMIT ?`,
		status, status, limit)
}

// Search -- тикеты по словам запроса, самые подходящие первыми.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Ticket, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	return s.query(ctx, `SELECT `+columns+` FROM tickets_fts f JOIN tickets t ON t.id = f.rowid
		WHERE tickets_fts MATCH ? ORDER BY bm25(tickets_fts) LIMIT ?`, match, limit)
}

// Update меняет статус и/или решение; пустое значение оставляет поле как было.
func (s *Store) Update(ctx context.Context, id int64, status, resolution string) (Ticket, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Ticket{}, err
	}
	if status == "" {
		status = current.Status
	}
	if resolution == "" {
		resolution = current.Resolution
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tickets SET status = ?, resolution = ?, updated_at = ? WHERE id = ?`,
		status, resolution, s.now().Unix(), id); err != nil {
		return Ticket{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Ticket, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ticket{}
	for rows.Next() {
		var tk Ticket
		var created, updated int64
		if err := rows.Scan(&tk.ID, &tk.Title, &tk.Description, &tk.Status, &tk.Resolution, &created, &updated); err != nil {
			return nil, err
		}
		tk.CreatedAt = time.Unix(created, 0).UTC().Format(time.RFC3339)
		tk.UpdatedAt = time.Unix(updated, 0).UTC().Format(time.RFC3339)
		out = append(out, tk)
	}
	return out, rows.Err()
}

// ftsQuery превращает текст в запрос FTS5: каждое слово в кавычках и по префиксу
// («вход» найдёт «входа»), слова через OR. Синтаксис FTS из текста так не пройдёт.
func ftsQuery(q string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		terms = append(terms, `"`+w+`"*`)
	}
	return strings.Join(terms, " OR ")
}
