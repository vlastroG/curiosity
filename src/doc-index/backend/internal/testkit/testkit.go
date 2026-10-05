// Package testkit -- общее для тестов: синтетическая книга в формате
// Project Gutenberg и поддельная Ollama.
//
// Тесты не ходят в сеть и не используют настоящие книги: и файл Gutenberg,
// и эмбеддинги здесь свои. Эмбеддинг -- «мешок слов»: каждое слово
// попадает в одну из 64 координат по хешу. Тексты с общими словами
// получаются похожими -- этого достаточно, чтобы проверить поиск.
package testkit

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"unicode"
)

// Chapter -- глава синтетической книги.
type Chapter struct {
	Key, Title string
	Paragraphs []string
}

// GutenbergFile собирает файл так, как его отдаёт Gutenberg: шапка,
// START, титул, оглавление в стиле «Тома Сойера», иллюстрации, текст, END,
// лицензия.
func GutenbergFile(title string, chapters []Chapter) []byte {
	var b strings.Builder
	const bom = string(rune(0xFEFF))
	b.WriteString(bom + "The Project Gutenberg eBook of " + title + "\r\n\r\nThis eBook is for the use of anyone.\r\n\r\n")
	b.WriteString("*** START OF THE PROJECT GUTENBERG EBOOK " + strings.ToUpper(title) + " ***\r\n\r\n\r\n")
	b.WriteString(strings.ToUpper(title) + "\r\n\r\nBy Somebody\r\n\r\n\r\nCONTENTS\r\n\r\n")
	for _, c := range chapters {
		if strings.HasPrefix(c.Key, "CHAPTER") && c.Title != "" {
			// описание переносится на вторую строку, как в оригинале
			words := strings.Fields(c.Title)
			half := len(words) / 2
			b.WriteString(c.Key + ". " + strings.Join(words[:half], " ") + "\r\n" + strings.Join(words[half:], " ") + "\r\n\r\n")
		}
	}
	b.WriteString("\r\n\r\nILLUSTRATIONS\r\n\r\nA Picture\r\n\r\nAnother Picture\r\n\r\n\r\n\r\n")
	for _, c := range chapters {
		b.WriteString(c.Key + "\r\n\r\n\r\n")
		for _, p := range c.Paragraphs {
			b.WriteString(wrap(p, 70) + "\r\n\r\n")
		}
		b.WriteString("\r\n\r\n")
	}
	b.WriteString("*** END OF THE PROJECT GUTENBERG EBOOK " + strings.ToUpper(title) + " ***\r\n\r\n")
	b.WriteString("Updated editions will replace the previous one. START: FULL LICENSE\r\n")
	return []byte(b.String())
}

// wrap переносит строки по ширине, как в текстовых файлах Gutenberg.
func wrap(s string, width int) string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	return strings.Join(append(lines, line), "\r\n")
}

// Novel -- книга на три главы с разными темами: забор, кладбище, пещера.
// В каждой главе много абзацев, так что чанков набирается несколько.
func Novel() []Chapter {
	topic := func(words []string, n int) []string {
		var ps []string
		for i := 0; i < n; i++ {
			var sents []string
			for j := 0; j < 5; j++ {
				w1, w2, w3 := words[(i+j)%len(words)], words[(i+2*j+1)%len(words)], words[(i+3*j+2)%len(words)]
				sents = append(sents, fmt.Sprintf("The %s was near the %s and the %s stayed there for a long while.", w1, w2, w3))
			}
			ps = append(ps, strings.Join(sents, " "))
		}
		return ps
	}
	return []Chapter{
		{Key: "PREFACE", Paragraphs: []string{"Most of the adventures recorded in this book really occurred. The rest are invented."}},
		{Key: "CHAPTER I", Title: "The Fence—Whitewash and Paint—A Good Trade",
			Paragraphs: topic([]string{"fence", "whitewash", "brush", "paint", "apple", "boards", "bucket"}, 40)},
		{Key: "CHAPTER II", Title: "The Graveyard—Midnight—A Murder",
			Paragraphs: topic([]string{"graveyard", "midnight", "doctor", "knife", "grave", "lantern", "murder"}, 40)},
		{Key: "CHAPTER III", Title: "Lost in the Cave—Candles—The Escape",
			Paragraphs: topic([]string{"cave", "candle", "darkness", "passage", "bats", "spring", "kite"}, 40)},
		{Key: "CONCLUSION", Paragraphs: []string{"So endeth this chronicle. It being strictly a history of a boy, it must stop here."}},
	}
}

// Dims -- размерность поддельных эмбеддингов.
const Dims = 64

// Vector -- «мешок слов» по хешу. Не нормализован: нормализация -- забота клиента.
func Vector(text string) []float32 {
	v := make([]float32, Dims)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len(w) < 3 {
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%Dims] += 3
	}
	v[Dims-1] += 0.5 // ни один вектор не нулевой
	return v
}

// Ollama -- поддельная Ollama: /api/version, /api/show, /api/pull, /api/embed, /api/ps.
type Ollama struct {
	*httptest.Server

	mu      sync.Mutex
	Models  map[string]bool // скачанные модели
	VRAM    int64           // сколько модели в видеопамяти; 0 -- «считает на CPU»
	Calls   int             // вызовов /api/embed
	Inputs  int             // входов в них всего
	FailAt  int             // упасть на этом вызове /api/embed (0 -- никогда)
	Reranks int             // вызовов /api/generate (реранкер)
	loaded  map[string]bool
}

// NewOllama -- поддельная Ollama, у которой уже скачаны models и всё на GPU.
func NewOllama(models ...string) *Ollama {
	o := &Ollama{Models: map[string]bool{}, VRAM: 1 << 30, loaded: map[string]bool{}}
	for _, m := range models {
		o.Models[m] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"version": "0.0.0-test"})
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		o.mu.Lock()
		ok := o.Models[req.Model]
		o.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": "model '" + req.Model + "' not found"})
			return
		}
		writeJSON(w, map[string]any{"details": map[string]string{"family": "test"}})
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, line := range []string{
			`{"status":"pulling manifest"}`,
			`{"status":"pulling abc","completed":524288,"total":1048576}`,
			`{"status":"pulling abc","completed":1048576,"total":1048576}`,
			`{"status":"success"}`,
		} {
			fmt.Fprintln(w, line)
		}
		o.mu.Lock()
		o.Models[req.Model] = true
		o.mu.Unlock()
	})
	mux.HandleFunc("POST /api/embed", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string
			Input []string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		o.mu.Lock()
		o.Calls++
		o.Inputs += len(req.Input)
		fail := o.FailAt > 0 && o.Calls == o.FailAt
		known := o.Models[req.Model]
		if known {
			o.loaded[req.Model] = true
		}
		o.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]string{"error": "boom"})
			return
		}
		if !known {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": "model not found"})
			return
		}
		vecs := make([][]float32, len(req.Input))
		tokens := 0
		for i, in := range req.Input {
			vecs[i] = Vector(in)
			tokens += len([]rune(in))/4 + 2
		}
		writeJSON(w, map[string]any{"model": req.Model, "embeddings": vecs, "prompt_eval_count": tokens})
	})
	// реранкер: «yes» тем вероятнее, чем больше слов запроса есть в отрывке
	mux.HandleFunc("POST /api/generate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model  string
			Prompt string
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		o.mu.Lock()
		known := o.Models[req.Model]
		if known {
			o.loaded[req.Model] = true
			o.Reranks++
		}
		o.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": "model not found"})
			return
		}
		p := Relevance(between(req.Prompt, "<Query>: ", "\n"), between(req.Prompt, "<Document>: ", "<|im_end|>"))
		p = math.Min(math.Max(p, 0.001), 0.999)
		writeJSON(w, map[string]any{"response": "yes", "logprobs": []any{map[string]any{
			"token": "yes", "logprob": math.Log(p),
			"top_logprobs": []any{
				map[string]any{"token": "yes", "logprob": math.Log(p)},
				map[string]any{"token": "No", "logprob": math.Log(1 - p)},
			},
		}}})
	})
	mux.HandleFunc("GET /api/ps", func(w http.ResponseWriter, _ *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		var ms []map[string]any
		for m := range o.loaded {
			ms = append(ms, map[string]any{"name": m + ":latest", "model": m + ":latest",
				"size": int64(1 << 30), "size_vram": o.VRAM})
		}
		writeJSON(w, map[string]any{"models": ms})
	})
	o.Server = httptest.NewServer(mux)
	return o
}

// Relevance -- доля слов запроса (от трёх букв), которые есть в документе.
func Relevance(query, doc string) float64 {
	words := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	}
	in := map[string]bool{}
	for _, w := range words(doc) {
		in[w] = true
	}
	total, hit := 0, 0
	for _, w := range words(query) {
		if len(w) < 3 {
			continue
		}
		total++
		if in[w] {
			hit++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	s = s[i+len(from):]
	if j := strings.Index(s, to); j >= 0 {
		s = s[:j]
	}
	return s
}

// RerankCalls -- сколько раз звали реранкер.
func (o *Ollama) RerankCalls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Reranks
}

// Stats -- сколько было вызовов и входов.
func (o *Ollama) Stats() (calls, inputs int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Calls, o.Inputs
}

// SetVRAM -- поменять размещение модели.
func (o *Ollama) SetVRAM(n int64) {
	o.mu.Lock()
	o.VRAM = n
	o.mu.Unlock()
}

// SetFailAt -- упасть на n-м вызове /api/embed, считая от текущего.
func (o *Ollama) SetFailAt(n int) {
	o.mu.Lock()
	o.FailAt = o.Calls + n
	o.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
