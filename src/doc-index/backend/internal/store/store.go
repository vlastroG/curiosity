// Package store -- индекс в SQLite: книги, чанки с метаданными и векторами,
// состояние вариантов индекса.
//
// Вектор хранится в той же строке, что и чанк, -- BLOB из float32
// (little-endian). Поиск -- полный перебор в памяти: на тысячу-другую векторов
// это миллисекунды, отдельный векторный движок не нужен.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"doc-index/internal/book"
)

const schema = `
CREATE TABLE IF NOT EXISTS books (
	id         TEXT PRIMARY KEY,
	gutenberg  INTEGER NOT NULL,
	title      TEXT NOT NULL,
	title_ru   TEXT NOT NULL,
	author     TEXT NOT NULL,
	sha256     TEXT NOT NULL,
	text       TEXT NOT NULL,
	sections   TEXT NOT NULL, -- JSON: главы со смещениями
	paragraphs TEXT NOT NULL  -- JSON: [[start, end, section], ...]
);
CREATE TABLE IF NOT EXISTS chunks (
	variant     TEXT NOT NULL,
	chunk_id    TEXT NOT NULL,
	book        TEXT NOT NULL,
	source      TEXT NOT NULL, -- gutenberg:74
	title       TEXT NOT NULL, -- название книги
	author      TEXT NOT NULL,
	section     TEXT NOT NULL, -- «Chapter II. Strong Temptations…»
	sections    TEXT NOT NULL, -- JSON: номера всех захваченных глав
	ordinal     INTEGER NOT NULL,
	start       INTEGER NOT NULL, -- байты в тексте книги, с перекрытием
	body_start  INTEGER NOT NULL, -- конец перекрытия с предыдущим чанком
	end         INTEGER NOT NULL,
	tokens      INTEGER NOT NULL,
	fingerprint TEXT NOT NULL,
	text        TEXT NOT NULL,
	vector      BLOB NOT NULL,
	PRIMARY KEY (variant, chunk_id)
);
CREATE INDEX IF NOT EXISTS chunks_book ON chunks (variant, book, start);
CREATE TABLE IF NOT EXISTS variants (
	variant       TEXT NOT NULL,
	book          TEXT NOT NULL,
	fingerprint   TEXT NOT NULL,
	model         TEXT NOT NULL,
	dims          INTEGER NOT NULL,
	chunks        INTEGER NOT NULL,
	tokens        INTEGER NOT NULL, -- оценка
	real_tokens   INTEGER NOT NULL, -- по prompt_eval_count
	embed_seconds REAL NOT NULL,
	total_seconds REAL NOT NULL,
	built_at      TEXT NOT NULL,
	PRIMARY KEY (variant, book)
);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);`

// Store -- открытый индекс.
type Store struct {
	db *sql.DB
}

// Open открывает (или создаёт) индекс.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("схема индекса: %w", err)
	}
	return &Store{db: db}, nil
}

// Close закрывает базу.
func (s *Store) Close() error { return s.db.Close() }

// Reset стирает весь индекс, кроме скачанных книг.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks; DELETE FROM variants; DELETE FROM meta;`)
	return err
}

// SaveBook сохраняет очищенную книгу и её разметку.
func (s *Store) SaveBook(ctx context.Context, b *book.Book) error {
	sections, err := json.Marshal(storedSections(b.Sections))
	if err != nil {
		return err
	}
	paras := make([][3]int, len(b.Paragraphs))
	for i, p := range b.Paragraphs {
		paras[i] = [3]int{p.Start, p.End, p.Section}
	}
	paragraphs, _ := json.Marshal(paras)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO books (id, gutenberg, title, title_ru, author, sha256, text, sections, paragraphs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET gutenberg=excluded.gutenberg, title=excluded.title,
			title_ru=excluded.title_ru, author=excluded.author, sha256=excluded.sha256,
			text=excluded.text, sections=excluded.sections, paragraphs=excluded.paragraphs`,
		b.ID, b.Gutenberg, b.Title, b.TitleRu, b.Author, b.SHA256, b.Text, sections, paragraphs)
	return err
}

// Books -- все книги индекса в порядке номеров Gutenberg.
func (s *Store) Books(ctx context.Context) ([]*book.Book, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, gutenberg, title, title_ru, author, sha256, text, sections, paragraphs
		FROM books ORDER BY gutenberg`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*book.Book
	for rows.Next() {
		b := &book.Book{}
		var sections, paragraphs []byte
		if err := rows.Scan(&b.ID, &b.Gutenberg, &b.Title, &b.TitleRu, &b.Author, &b.SHA256,
			&b.Text, &sections, &paragraphs); err != nil {
			return nil, err
		}
		var ss []sectionJSON
		if err := json.Unmarshal(sections, &ss); err != nil {
			return nil, fmt.Errorf("книга %s: %w", b.ID, err)
		}
		for _, x := range ss {
			b.Sections = append(b.Sections, book.Section{N: x.N, Key: x.Key, Label: x.Label, Title: x.Title,
				Start: x.Start, BodyStart: x.BodyStart, End: x.End})
		}
		var ps [][3]int
		if err := json.Unmarshal(paragraphs, &ps); err != nil {
			return nil, fmt.Errorf("книга %s: %w", b.ID, err)
		}
		for _, p := range ps {
			b.Paragraphs = append(b.Paragraphs, book.Paragraph{Start: p[0], End: p[1], Section: p[2]})
		}
		out = append(out, book.FromStored(b))
	}
	return out, rows.Err()
}

type sectionJSON struct {
	N         int    `json:"n"`
	Key       string `json:"key"`
	Label     string `json:"label"`
	Title     string `json:"title"`
	Start     int    `json:"start"`
	BodyStart int    `json:"bodyStart"`
	End       int    `json:"end"`
}

func storedSections(ss []book.Section) []sectionJSON {
	out := make([]sectionJSON, len(ss))
	for i, x := range ss {
		out[i] = sectionJSON{x.N, x.Key, x.Label, x.Title, x.Start, x.BodyStart, x.End}
	}
	return out
}

// Chunk -- строка индекса: чанк, его метаданные и вектор.
type Chunk struct {
	Variant     string    `json:"variant"`
	ChunkID     string    `json:"chunkId"`
	Book        string    `json:"book"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	Section     string    `json:"section"`
	Sections    []int     `json:"sections"`
	Ordinal     int       `json:"ordinal"`
	Start       int       `json:"start"`
	BodyStart   int       `json:"bodyStart"`
	End         int       `json:"end"`
	Tokens      int       `json:"tokens"`
	Fingerprint string    `json:"fingerprint"`
	Text        string    `json:"text"`
	Vector      []float32 `json:"-"`
}

// Existing -- id чанков варианта и книги, уже посчитанных с этим отпечатком.
// По ним прерванная индексация продолжается, а не начинается заново.
func (s *Store) Existing(ctx context.Context, variant, bookID, fingerprint string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT chunk_id FROM chunks WHERE variant=? AND book=? AND fingerprint=?`, variant, bookID, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DropStale удаляет чанки варианта и книги, посчитанные с другим отпечатком,
// и отметку о готовности варианта.
func (s *Store) DropStale(ctx context.Context, variant, bookID, fingerprint string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM chunks WHERE variant=? AND book=? AND fingerprint<>?`, variant, bookID, fingerprint)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM variants WHERE variant=? AND book=? AND fingerprint<>?`, variant, bookID, fingerprint)
	return err
}

// Insert записывает пачку чанков одной транзакцией.
func (s *Store) Insert(ctx context.Context, cs []Chunk) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO chunks (variant, chunk_id, book, source, title, author, section, sections,
			ordinal, start, body_start, end, tokens, fingerprint, text, vector)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range cs {
		sections, _ := json.Marshal(c.Sections)
		if _, err := stmt.ExecContext(ctx, c.Variant, c.ChunkID, c.Book, c.Source, c.Title, c.Author,
			c.Section, sections, c.Ordinal, c.Start, c.BodyStart, c.End, c.Tokens, c.Fingerprint, c.Text,
			EncodeVector(c.Vector)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const chunkColumns = `variant, chunk_id, book, source, title, author, section, sections, ordinal,
	start, body_start, end, tokens, fingerprint, text`

// Chunks -- чанки варианта (всех книг или одной), с векторами или без.
func (s *Store) Chunks(ctx context.Context, variant, bookID string, withVectors bool) ([]Chunk, error) {
	cols := chunkColumns
	if withVectors {
		cols += ", vector"
	}
	q := `SELECT ` + cols + ` FROM chunks WHERE variant=?`
	args := []any{variant}
	if bookID != "" {
		q += ` AND book=?`
		args = append(args, bookID)
	}
	q += ` ORDER BY book, ordinal`
	return s.queryChunks(ctx, withVectors, q, args...)
}

// Chunk -- один чанк по id.
func (s *Store) Chunk(ctx context.Context, variant, chunkID string) (Chunk, error) {
	cs, err := s.queryChunks(ctx, false,
		`SELECT `+chunkColumns+` FROM chunks WHERE variant=? AND chunk_id=?`, variant, chunkID)
	if err != nil {
		return Chunk{}, err
	}
	if len(cs) == 0 {
		return Chunk{}, ErrNotFound
	}
	return cs[0], nil
}

// ErrNotFound -- такого чанка нет.
var ErrNotFound = errors.New("не найдено")

func (s *Store) queryChunks(ctx context.Context, withVectors bool, q string, args ...any) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		var sections, vector []byte
		dest := []any{&c.Variant, &c.ChunkID, &c.Book, &c.Source, &c.Title, &c.Author, &c.Section, &sections,
			&c.Ordinal, &c.Start, &c.BodyStart, &c.End, &c.Tokens, &c.Fingerprint, &c.Text}
		if withVectors {
			dest = append(dest, &vector)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(sections, &c.Sections)
		if withVectors {
			c.Vector = DecodeVector(vector)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// State -- готовый вариант индекса для одной книги.
type State struct {
	Variant      string    `json:"variant"`
	Book         string    `json:"book"`
	Fingerprint  string    `json:"fingerprint"`
	Model        string    `json:"model"`
	Dims         int       `json:"dims"`
	Chunks       int       `json:"chunks"`
	Tokens       int       `json:"tokens"`
	RealTokens   int       `json:"realTokens"`
	EmbedSeconds float64   `json:"embedSeconds"`
	TotalSeconds float64   `json:"totalSeconds"`
	BuiltAt      time.Time `json:"builtAt"`
}

// SaveState отмечает вариант книги готовым.
func (s *Store) SaveState(ctx context.Context, st State) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO variants (variant, book, fingerprint, model, dims, chunks, tokens, real_tokens,
			embed_seconds, total_seconds, built_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.Variant, st.Book, st.Fingerprint, st.Model, st.Dims, st.Chunks, st.Tokens, st.RealTokens,
		st.EmbedSeconds, st.TotalSeconds, st.BuiltAt.UTC().Format(time.RFC3339))
	return err
}

// States -- все готовые варианты.
func (s *Store) States(ctx context.Context) ([]State, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT variant, book, fingerprint, model, dims, chunks, tokens, real_tokens,
			embed_seconds, total_seconds, built_at
		FROM variants ORDER BY variant, book`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []State
	for rows.Next() {
		var st State
		var built string
		if err := rows.Scan(&st.Variant, &st.Book, &st.Fingerprint, &st.Model, &st.Dims, &st.Chunks,
			&st.Tokens, &st.RealTokens, &st.EmbedSeconds, &st.TotalSeconds, &built); err != nil {
			return nil, err
		}
		st.BuiltAt, _ = time.Parse(time.RFC3339, built)
		out = append(out, st)
	}
	return out, rows.Err()
}

// Meta -- значение служебного ключа ("" если нет).
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta записывает служебный ключ.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, key, value)
	return err
}

// EncodeVector -- float32 little-endian.
func EncodeVector(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(x))
	}
	return out
}

// DecodeVector -- обратно из BLOB.
func DecodeVector(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}
