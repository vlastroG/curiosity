package docs

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"support-desk/internal/embed"

	_ "modernc.org/sqlite"
)

const indexSchema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chunks (
	id    INTEGER PRIMARY KEY,
	path  TEXT NOT NULL,
	title TEXT NOT NULL,
	text  TEXT NOT NULL,
	vec   BLOB NOT NULL
);
`

// batchSize -- чанков в одном запросе к Ollama.
const batchSize = 32

// Embedder -- то, что считает векторы; в работе -- embed.Client.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) (embed.Result, error)
}

// Hit -- найденный фрагмент.
type Hit struct {
	Path  string  `json:"path"`
	Title string  `json:"title"`
	Text  string  `json:"-"`
	Score float64 `json:"score"`
}

// Index -- чанки в SQLite и их копия в памяти для поиска.
type Index struct {
	db *sql.DB

	mu     sync.RWMutex
	chunks []Piece
	vecs   [][]float32
	bm25   *BM25
	files  int
}

// Open открывает файл индекса и создаёт таблицы, если их нет.
func Open(path string) (*Index, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(indexSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("схема индекса не создалась: %w", err)
	}
	return &Index{db: db}, nil
}

func (x *Index) Close() error { return x.db.Close() }

// Ensure приводит индекс в соответствие с файлами: если отпечаток совпал --
// только загружает сохранённое, иначе режет и считает векторы заново.
func (x *Index) Ensure(ctx context.Context, o Options, e Embedder, progress func(done, total int)) (bool, error) {
	files, err := Walk(o)
	if err != nil {
		return false, err
	}
	fp := Fingerprint(o.Model, files)
	if x.meta(ctx, "fingerprint") == fp {
		if err := x.load(ctx); err == nil && len(x.chunks) > 0 {
			return false, nil
		}
	}

	var chunks []Piece
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(o.Root, filepath.FromSlash(f.Path)))
		if err != nil {
			return false, err
		}
		chunks = append(chunks, Chunk(f.Path, string(raw))...)
	}
	if len(chunks) == 0 {
		return false, errors.New("в файлах документации нет текста")
	}

	vecs := make([][]float32, 0, len(chunks))
	for start := 0; start < len(chunks); start += batchSize {
		end := min(start+batchSize, len(chunks))
		texts := make([]string, end-start)
		for i, c := range chunks[start:end] {
			texts[i] = c.Text
		}
		res, err := e.Embed(ctx, o.Model, texts)
		if err != nil {
			return false, fmt.Errorf("эмбеддинги: %w", err)
		}
		vecs = append(vecs, res.Vectors...)
		if progress != nil {
			progress(end, len(chunks))
		}
	}

	if err := x.save(ctx, chunks, vecs, fp, len(files)); err != nil {
		return false, err
	}
	return true, x.load(ctx)
}

func (x *Index) save(ctx context.Context, chunks []Piece, vecs [][]float32, fp string, files int) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
		return err
	}
	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO chunks(id, path, title, text, vec) VALUES (?, ?, ?, ?, ?)`,
			i, c.Path, c.Title, c.Text, encode(vecs[i])); err != nil {
			return err
		}
	}
	for key, value := range map[string]string{"fingerprint": fp, "files": strconv.Itoa(files)} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (x *Index) load(ctx context.Context) error {
	rows, err := x.db.QueryContext(ctx, `SELECT path, title, text, vec FROM chunks ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var chunks []Piece
	var vecs [][]float32
	for rows.Next() {
		var c Piece
		var blob []byte
		if err := rows.Scan(&c.Path, &c.Title, &c.Text, &blob); err != nil {
			return err
		}
		chunks = append(chunks, c)
		vecs = append(vecs, decode(blob))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	files, _ := strconv.Atoi(x.meta(ctx, "files"))

	x.mu.Lock()
	defer x.mu.Unlock()
	x.chunks, x.vecs, x.bm25, x.files = chunks, vecs, NewBM25(texts), files
	return nil
}

func (x *Index) meta(ctx context.Context, key string) string {
	var value string
	x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	return value
}

// Stats -- сколько файлов и чанков в загруженном индексе.
func (x *Index) Stats() (files, chunks int) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.files, len(x.chunks)
}

// Search -- top-k фрагментов: места по косинусу и по BM25, слитые RRF.
func (x *Index) Search(ctx context.Context, e Embedder, model, query string, k int) ([]Hit, error) {
	res, err := e.Embed(ctx, model, []string{query})
	if err != nil {
		return nil, fmt.Errorf("эмбеддинг вопроса: %w", err)
	}
	q := res.Vectors[0]

	x.mu.RLock()
	defer x.mu.RUnlock()
	dense := make([]Scored, len(x.vecs))
	for i, v := range x.vecs {
		var dot float64
		for j := range min(len(v), len(q)) {
			dot += float64(v[j]) * float64(q[j])
		}
		dense[i] = Scored{I: i, Score: dot}
	}
	slices.SortStableFunc(dense, func(a, b Scored) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	dense = dense[:min(len(dense), 50)]
	lexical := x.bm25.Rank(query)
	lexical = lexical[:min(len(lexical), 50)]

	var hits []Hit
	for _, s := range RRF([][]Scored{dense, lexical}, 60) {
		if len(hits) == k {
			break
		}
		c := x.chunks[s.I]
		hits = append(hits, Hit{Path: c.Path, Title: c.Title, Text: c.Text, Score: s.Score})
	}
	return hits, nil
}

func encode(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(f))
	}
	return out
}

func decode(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}
