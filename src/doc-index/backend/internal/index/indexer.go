// Package index -- конвейер индексации: книги → чанки → эмбеддинги → SQLite.
//
//	fetch → parse → calibrate → для каждого варианта и книги:
//	    отпечаток совпал? → пропуск
//	    chunk → embed (пачками, на GPU) → store
//
// Каждый шаг -- событие: CLI печатает их в лог, веб-интерфейс показывает вживую.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/embed"
	"doc-index/internal/store"
	"doc-index/internal/tokens"
)

// Event -- шаг индексации.
type Event struct {
	Seq     int       `json:"seq"`
	Time    time.Time `json:"time"`
	Stage   string    `json:"stage"` // ollama, pull, fetch, parse, calibrate, gpu, chunk, sentences, embed, skip, done, error
	Book    string    `json:"book,omitempty"`
	Variant string    `json:"variant,omitempty"`
	Message string    `json:"message"`
	Done    int       `json:"done,omitempty"`
	Total   int       `json:"total,omitempty"`
	Level   string    `json:"level,omitempty"` // "", warn, error
}

// Indexer строит индекс.
type Indexer struct {
	Store    *store.Store
	Ollama   *embed.Client
	Fetcher  *book.Fetcher
	Books    []int
	Variants []Variant
	Params   chunk.Params
	Batch    int
}

// Summary -- итог запуска.
type Summary struct {
	Built, Skipped int
	Seconds        float64
}

type run struct {
	ix      *Indexer
	ctx     context.Context
	emit    func(Event)
	est     tokens.Estimator
	checked map[string]bool // модели, прошедшие проверку GPU
	books   []*book.Book

	estTokens, realTokens int // для сверки калибровки по основной модели
}

// Run строит всё, чего не хватает. rebuild -- пересчитать всё заново.
func (ix *Indexer) Run(ctx context.Context, rebuild bool, emit func(Event)) (Summary, error) {
	started := time.Now()
	r := &run{ix: ix, ctx: ctx, emit: emit, checked: map[string]bool{}}
	if ix.Batch <= 0 {
		ix.Batch = 32
	}

	if err := r.ollama(); err != nil {
		return Summary{}, err
	}
	if rebuild {
		if err := ix.Store.Reset(ctx); err != nil {
			return Summary{}, err
		}
		r.say(Event{Stage: "reset", Message: "индекс очищен, считаю всё заново"})
	}
	if err := r.fetchBooks(); err != nil {
		return Summary{}, err
	}
	if err := r.calibrate(); err != nil {
		return Summary{}, err
	}

	var sum Summary
	for _, v := range ix.Variants {
		for _, b := range r.books {
			built, err := r.variant(v, b)
			if err != nil {
				return sum, fmt.Errorf("%s / %s: %w", v.ID, b.ID, err)
			}
			if built {
				sum.Built++
			} else {
				sum.Skipped++
			}
		}
	}
	if r.realTokens > 0 {
		errPct := 100 * float64(r.estTokens-r.realTokens) / float64(r.realTokens)
		r.say(Event{Stage: "calibrate", Message: fmt.Sprintf(
			"сверка оценки токенов: оценка %d, модель насчитала %d, расхождение %+.1f%%",
			r.estTokens, r.realTokens, errPct)})
	}
	_ = ix.Store.SetMeta(ctx, "index_version", strconv.FormatInt(time.Now().UnixNano(), 10))
	sum.Seconds = time.Since(started).Seconds()
	msg := fmt.Sprintf("готово за %.1f с: посчитано %d, без изменений %d", sum.Seconds, sum.Built, sum.Skipped)
	r.say(Event{Stage: "done", Message: msg})
	return sum, nil
}

func (r *run) say(e Event) {
	e.Time = time.Now()
	if r.emit != nil {
		r.emit(e)
	}
}

// ollama -- Ollama отвечает, нужные модели скачаны.
func (r *run) ollama() error {
	ver, err := r.ix.Ollama.Version(r.ctx)
	if err != nil {
		return err
	}
	r.say(Event{Stage: "ollama", Message: "Ollama " + ver + " на связи: " + r.ix.Ollama.URL})
	seen := map[string]bool{}
	for _, v := range r.ix.Variants {
		if seen[v.Model] {
			continue
		}
		seen[v.Model] = true
		has, err := r.ix.Ollama.Has(r.ctx, v.Model)
		if err != nil {
			return err
		}
		if has {
			r.say(Event{Stage: "ollama", Message: "модель " + v.Model + " уже скачана"})
			continue
		}
		r.say(Event{Stage: "pull", Message: "скачиваю модель " + v.Model + " (один раз)"})
		last, lastStatus := time.Time{}, ""
		err = r.ix.Ollama.Pull(r.ctx, v.Model, func(status string, done, total int64) {
			if status == lastStatus && time.Since(last) < 2*time.Second {
				return
			}
			last, lastStatus = time.Now(), status
			e := Event{Stage: "pull", Message: v.Model + ": " + status}
			if total > 0 {
				e.Done, e.Total = int(done>>20), int(total>>20)
				e.Message += fmt.Sprintf(" %d/%d МБ", e.Done, e.Total)
			}
			r.say(e)
		})
		if err != nil {
			return err
		}
		r.say(Event{Stage: "pull", Message: "модель " + v.Model + " скачана"})
	}
	return nil
}

func (r *run) fetchBooks() error {
	for _, n := range r.ix.Books {
		raw, cached, err := r.ix.Fetcher.Get(r.ctx, n)
		if err != nil {
			return err
		}
		from := "скачана с зеркала " + r.ix.Fetcher.Mirror
		if cached {
			from = "из кэша"
		}
		b, err := book.Parse(n, raw)
		if err != nil {
			return err
		}
		r.say(Event{Stage: "fetch", Book: b.ID, Message: fmt.Sprintf("%s — %d КБ, %s", b.Title, len(raw)>>10, from)})
		chapters := 0
		for _, s := range b.Sections {
			if s.Title != "" {
				chapters++
			}
		}
		r.say(Event{Stage: "parse", Book: b.ID, Message: fmt.Sprintf(
			"очищено: убрано %d КБ служебного текста; %d разделов (%d с описанием из оглавления), %d абзацев, %d тыс. символов",
			max(0, len(raw)-len(b.Text))>>10, len(b.Sections), chapters, len(b.Paragraphs), len([]rune(b.Text))/1000)})
		if err := r.ix.Store.SaveBook(r.ctx, b); err != nil {
			return err
		}
		r.books = append(r.books, b)
	}
	return nil
}

// calibrate подбирает коэффициент «символов на токен» для основной модели.
// Считается один раз и хранится в индексе: иначе от запуска к запуску
// сдвигались бы границы чанков и отпечатки.
func (r *run) calibrate() error {
	model := r.ix.Variants[0].Model
	key := "chars_per_token:" + model
	saved, err := r.ix.Store.Meta(r.ctx, key)
	if err != nil {
		return err
	}
	if cpt, err := strconv.ParseFloat(saved, 64); err == nil && cpt > 0 {
		r.est = tokens.Estimator{CharsPerToken: cpt}
		r.say(Event{Stage: "calibrate", Message: fmt.Sprintf("символов на токен %s: %.2f (сохранённая калибровка)", model, cpt)})
		return nil
	}

	samples := calibrationSamples(r.books, 30)
	r.say(Event{Stage: "calibrate", Message: fmt.Sprintf(
		"калибрую счётчик токенов: %d образцов по одному в %s", len(samples), model), Total: len(samples)})
	var got []tokens.Sample
	for i, s := range samples {
		res, err := r.ix.Ollama.Embed(r.ctx, model, []string{s})
		if err != nil {
			return err
		}
		if i == 0 {
			if err := r.requireGPU(model); err != nil {
				return err
			}
		}
		got = append(got, tokens.Sample{Runes: len([]rune(s)), Tokens: res.PromptTokens})
	}
	r.est = tokens.Calibrate(got, embed.Lookup(model).SpecialTokens)
	if err := r.ix.Store.SetMeta(r.ctx, key, strconv.FormatFloat(r.est.CharsPerToken, 'f', 3, 64)); err != nil {
		return err
	}
	r.say(Event{Stage: "calibrate", Message: fmt.Sprintf("символов на токен %s: %.2f", model, r.est.CharsPerToken)})
	return nil
}

// calibrationSamples -- куски по ~2000 символов, равномерно по книгам.
func calibrationSamples(books []*book.Book, n int) []string {
	var paras []string
	for _, b := range books {
		for _, p := range b.Paragraphs {
			paras = append(paras, b.Text[p.Start:p.End])
		}
	}
	if len(paras) == 0 {
		return nil
	}
	var out []string
	step := max(1, len(paras)/n)
	for i := 0; i < len(paras) && len(out) < n; i += step {
		s := ""
		for j := i; j < len(paras) && len([]rune(s)) < 2000; j++ {
			if s != "" {
				s += "\n\n"
			}
			s += paras[j]
		}
		out = append(out, s)
	}
	return out
}

func (r *run) requireGPU(model string) error {
	if r.checked[model] {
		return nil
	}
	p, err := r.ix.Ollama.RequireGPU(r.ctx, model)
	if err != nil {
		return err
	}
	r.checked[model] = true
	r.say(Event{Stage: "gpu", Message: fmt.Sprintf("✔ %s целиком в видеопамяти: %d МБ", model, p.SizeVRAM>>20)})
	return nil
}

// Fingerprint -- отпечаток варианта книги: текст, стратегия, модель, параметры
// нарезки, калибровка, формат текста для эмбеддинга.
func Fingerprint(v Variant, b *book.Book, p chunk.Params, est tokens.Estimator) string {
	raw, _ := json.Marshal([]string{b.SHA256, v.ID, v.Strategy, v.Model, p.Fingerprint(),
		strconv.FormatFloat(est.CharsPerToken, 'f', 3, 64), embedFormat})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// variant строит один вариант для одной книги. false -- уже был готов.
func (r *run) variant(v Variant, b *book.Book) (bool, error) {
	started := time.Now()
	fp := Fingerprint(v, b, r.ix.Params, r.est)
	states, err := r.ix.Store.States(r.ctx)
	if err != nil {
		return false, err
	}
	for _, st := range states {
		if st.Variant == v.ID && st.Book == b.ID && st.Fingerprint == fp {
			r.say(Event{Stage: "skip", Book: b.ID, Variant: v.ID, Message: fmt.Sprintf(
				"%s / %s: без изменений (%d чанков)", v.Title, b.Title, st.Chunks)})
			return false, nil
		}
	}
	if err := r.ix.Store.DropStale(r.ctx, v.ID, b.ID, fp); err != nil {
		return false, err
	}

	var cs []chunk.Chunk
	switch v.Strategy {
	case Fixed:
		cs = chunk.Fixed(b, r.est, r.ix.Params)
	case Structure:
		cs = chunk.Structure(b, r.est, r.ix.Params)
	case Semantic:
		sents := chunk.Sentences(b)
		vectors, err := r.sentenceVectors(v, b, sents)
		if err != nil {
			return false, err
		}
		cs = chunk.Semantic(b, r.est, r.ix.Params, sents, vectors)
	default:
		return false, fmt.Errorf("неизвестная стратегия %q", v.Strategy)
	}
	rows := r.rows(v, b, cs, fp)
	r.say(Event{Stage: "chunk", Book: b.ID, Variant: v.ID, Message: fmt.Sprintf(
		"%s / %s: %d чанков, %s", v.Title, b.Title, len(rows), sizeLine(rows))})

	existing, err := r.ix.Store.Existing(r.ctx, v.ID, b.ID, fp)
	if err != nil {
		return false, err
	}
	var todo []store.Chunk
	for _, c := range rows {
		if !existing[c.ChunkID] {
			todo = append(todo, c)
		}
	}
	if len(existing) > 0 && len(todo) < len(rows) {
		r.say(Event{Stage: "embed", Book: b.ID, Variant: v.ID, Message: fmt.Sprintf(
			"продолжаю прерванную индексацию: %d из %d чанков уже посчитаны", len(rows)-len(todo), len(rows))})
	}

	embedStarted := time.Now()
	realTokens, dims := 0, 0
	for i := 0; i < len(todo); i += r.ix.Batch {
		batch := todo[i:min(i+r.ix.Batch, len(todo))]
		inputs := make([]string, len(batch))
		estBatch := 0
		for j, c := range batch {
			inputs[j] = EmbedText(v, b, c)
			estBatch += r.est.Count(inputs[j])
		}
		res, err := r.ix.Ollama.Embed(r.ctx, v.Model, inputs)
		if err != nil {
			return false, err
		}
		if err := r.requireGPU(v.Model); err != nil {
			return false, err
		}
		for j := range batch {
			batch[j].Vector = res.Vectors[j]
		}
		dims = len(res.Vectors[0])
		if err := r.ix.Store.Insert(r.ctx, batch); err != nil {
			return false, err
		}
		realTokens += res.PromptTokens
		if v.Model == r.ix.Variants[0].Model {
			r.estTokens += estBatch
			r.realTokens += res.PromptTokens - embed.Lookup(v.Model).SpecialTokens*len(batch)
		}
		done := min(i+r.ix.Batch, len(todo))
		secs := time.Since(embedStarted).Seconds()
		r.say(Event{Stage: "embed", Book: b.ID, Variant: v.ID, Done: done, Total: len(todo), Message: fmt.Sprintf(
			"%s / %s: пачка %d/%d, %.1f тыс. токенов/с", v.Title, b.Title,
			(done+r.ix.Batch-1)/r.ix.Batch, (len(todo)+r.ix.Batch-1)/r.ix.Batch, float64(realTokens)/secs/1000)})
	}
	if dims == 0 && len(rows) > 0 {
		dims = r.storedDims(v.ID, b.ID)
	}

	total := 0
	for _, c := range rows {
		total += c.Tokens
	}
	st := store.State{Variant: v.ID, Book: b.ID, Fingerprint: fp, Model: v.Model, Dims: dims,
		Chunks: len(rows), Tokens: total, RealTokens: realTokens,
		EmbedSeconds: time.Since(embedStarted).Seconds(), TotalSeconds: time.Since(started).Seconds(),
		BuiltAt: time.Now()}
	if err := r.ix.Store.SaveState(r.ctx, st); err != nil {
		return false, err
	}
	_ = r.ix.Store.SetMeta(r.ctx, "index_version", strconv.FormatInt(time.Now().UnixNano(), 10))
	r.say(Event{Stage: "store", Book: b.ID, Variant: v.ID, Message: fmt.Sprintf(
		"%s / %s: записано в индекс, вектор %d чисел, %.1f с", v.Title, b.Title, dims, st.TotalSeconds)})
	return true, nil
}

func (r *run) storedDims(variant, bookID string) int {
	cs, err := r.ix.Store.Chunks(r.ctx, variant, bookID, true)
	if err != nil || len(cs) == 0 {
		return 0
	}
	return len(cs[0].Vector)
}

// sentenceVectors -- эмбеддинги окон предложений для semantic.
func (r *run) sentenceVectors(v Variant, b *book.Book, sents []chunk.Sentence) ([][]float32, error) {
	windows := chunk.Windows(b, sents)
	prefix := embed.Lookup(v.Model).DocPrefix
	batch := r.ix.Batch * 4 // окна короткие
	out := make([][]float32, 0, len(windows))
	started := time.Now()
	r.say(Event{Stage: "sentences", Book: b.ID, Variant: v.ID, Total: len(windows), Message: fmt.Sprintf(
		"%s / %s: %d предложений — считаю эмбеддинги окон «предложение ± соседи», чтобы найти смысловые переходы",
		v.Title, b.Title, len(windows))})
	lastSay := time.Now()
	for i := 0; i < len(windows); i += batch {
		part := windows[i:min(i+batch, len(windows))]
		in := make([]string, len(part))
		for j, w := range part {
			in[j] = prefix + w
		}
		res, err := r.ix.Ollama.Embed(r.ctx, v.Model, in)
		if err != nil {
			return nil, err
		}
		if err := r.requireGPU(v.Model); err != nil {
			return nil, err
		}
		out = append(out, res.Vectors...)
		if time.Since(lastSay) > time.Second || len(out) == len(windows) {
			lastSay = time.Now()
			r.say(Event{Stage: "sentences", Book: b.ID, Variant: v.ID, Done: len(out), Total: len(windows),
				Message: fmt.Sprintf("%s / %s: предложения %d/%d, %.0f окон/с", v.Title, b.Title,
					len(out), len(windows), float64(len(out))/time.Since(started).Seconds())})
		}
	}
	if len(out) != len(sents) {
		return nil, errors.New("число векторов предложений не совпало с числом предложений")
	}
	return out, nil
}

func (r *run) rows(v Variant, b *book.Book, cs []chunk.Chunk, fp string) []store.Chunk {
	out := make([]store.Chunk, len(cs))
	for i, c := range cs {
		out[i] = store.Chunk{
			Variant: v.ID, ChunkID: chunk.ID(v.ID, b.ID, c.Ordinal), Book: b.ID, Source: b.Source(),
			Title: b.Title, Author: b.Author, Section: SectionLabel(b, c.Sections), Sections: c.Sections,
			Ordinal: c.Ordinal, Start: c.Start, BodyStart: c.BodyStart, End: c.End, Tokens: c.Tokens,
			Fingerprint: fp, Text: b.Text[c.Start:c.End],
		}
	}
	return out
}

func sizeLine(rows []store.Chunk) string {
	if len(rows) == 0 {
		return "пусто"
	}
	lo, hi, sum, in := rows[0].Tokens, rows[0].Tokens, 0, 0
	for _, c := range rows {
		lo, hi = min(lo, c.Tokens), max(hi, c.Tokens)
		sum += c.Tokens
		if c.Tokens >= 500 && c.Tokens <= 1000 {
			in++
		}
	}
	return fmt.Sprintf("токенов от %d до %d, в среднем %d; в диапазоне 500–1000 — %d из %d",
		lo, hi, sum/len(rows), in, len(rows))
}
