package compare

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"doc-index/internal/book"
	"doc-index/internal/chunk"
	"doc-index/internal/index"
	"doc-index/internal/search"
	"doc-index/internal/store"
)

// TopK -- сколько мест смотрим по каждому вопросу (для MRR@10).
const TopK = 10

// Scores -- качество поиска по набору вопросов.
type Scores struct {
	N     int     `json:"n"`
	Hit1  float64 `json:"hit1"`  // нужная глава на первом месте
	Hit3  float64 `json:"hit3"`  // в первой тройке
	Hit5  float64 `json:"hit5"`  // в первой пятёрке
	MRR   float64 `json:"mrr"`   // среднее 1/место (0, если нет в первой десятке)
	Book1 float64 `json:"book1"` // первое место -- из правильной книги
}

// Shape -- форма чанков варианта.
type Shape struct {
	Chunks          int     `json:"chunks"`
	Min             int     `json:"min"`
	P50             int     `json:"p50"`
	P90             int     `json:"p90"`
	Max             int     `json:"max"`
	InRange         float64 `json:"inRange"`         // доля в Min..Max
	OverlapOverhead float64 `json:"overlapOverhead"` // сколько лишних токенов из-за перекрытия
	CrossChapter    int     `json:"crossChapter"`    // чанков, захвативших больше одной главы
	CutStart        int     `json:"cutStart"`        // начинаются посреди предложения
	CutEnd          int     `json:"cutEnd"`          // обрываются посреди предложения
	Histogram       []int   `json:"histogram"`       // по 100 токенов: 0–99, 100–199, …, 1100+
}

// Boundaries -- совпадение разрезов semantic с настоящими главами.
type Boundaries struct {
	Chapters int     `json:"chapters"` // границ глав в книгах
	Matched  int     `json:"matched"`  // из них рядом с разрезом (±2 предложения)
	Cuts     int     `json:"cuts"`     // разрезов всего
	Random   float64 `json:"random"`   // сколько совпало бы, режь мы наугад столько же раз
}

// VariantReport -- всё про один вариант.
type VariantReport struct {
	Variant      index.Variant     `json:"variant"`
	Model        string            `json:"model"`
	Dims         int               `json:"dims"`
	Shape        Shape             `json:"shape"`
	EmbedSeconds float64           `json:"embedSeconds"`
	TotalSeconds float64           `json:"totalSeconds"` // с нарезкой; у semantic -- и с эмбеддингами предложений
	RealTokens   int               `json:"realTokens"`
	All          Scores            `json:"all"`
	ByLang       map[string]Scores `json:"byLang"`
	Boundaries   *Boundaries       `json:"boundaries,omitempty"`
	Ready        bool              `json:"ready"`
}

// Place -- где вариант нашёл ответ на вопрос.
type Place struct {
	Rank    int    `json:"rank"`    // 1…10, 0 -- нет в первой десятке
	TopBook string `json:"topBook"` // книга первого места
}

// QuestionRow -- строка таблицы вопросов.
type QuestionRow struct {
	Question Question         `json:"question"`
	Places   map[string]Place `json:"places"`
}

// Report -- сравнение целиком.
type Report struct {
	GeneratedAt time.Time       `json:"generatedAt"`
	Params      chunk.Params    `json:"params"`
	Variants    []VariantReport `json:"variants"`
	Questions   []QuestionRow   `json:"questions"`
	Invalid     []Question      `json:"invalid"`
	Conclusion  []string        `json:"conclusion"`
}

// Build считает сравнение по индексу в памяти поисковика.
func Build(ctx context.Context, s *search.Searcher, qs []Question, p chunk.Params) (Report, error) {
	if err := s.Refresh(ctx); err != nil {
		return Report{}, err
	}
	infos := s.Books()
	books := map[string]*book.Book{}
	for id, info := range infos {
		books[id] = info.Book
	}
	qs = Validate(qs, books)
	states, err := s.Store.States(ctx)
	if err != nil {
		return Report{}, err
	}

	rep := Report{GeneratedAt: time.Now(), Params: p}
	var valid []Question
	for _, q := range qs {
		if q.Valid {
			valid = append(valid, q)
		} else {
			rep.Invalid = append(rep.Invalid, q)
		}
	}
	rows := make([]QuestionRow, len(valid))
	for i, q := range valid {
		rows[i] = QuestionRow{Question: q, Places: map[string]Place{}}
	}

	for _, v := range s.Variants {
		vr := VariantReport{Variant: v, Model: v.Model, ByLang: map[string]Scores{}}
		for _, st := range states {
			if st.Variant == v.ID {
				vr.Dims = st.Dims
				vr.EmbedSeconds += st.EmbedSeconds
				vr.TotalSeconds += st.TotalSeconds
				vr.RealTokens += st.RealTokens
			}
		}
		cs := s.Chunks(v.ID)
		vr.Ready = len(cs) > 0
		if !vr.Ready {
			rep.Variants = append(rep.Variants, vr)
			continue
		}
		vr.Shape = shape(cs, infos, p)
		if v.Strategy == index.Semantic {
			b := boundaries(cs, infos)
			vr.Boundaries = &b
		}

		var ranks []Place
		for i, q := range valid {
			qv, _, err := s.QueryVector(ctx, v.Model, q.Q)
			if err != nil {
				return Report{}, err
			}
			hits, err := s.Rank(v.ID, qv, "", TopK)
			if err != nil {
				return Report{}, err
			}
			pl := Locate(q, hits)
			rows[i].Places[v.ID] = pl
			ranks = append(ranks, pl)
		}
		vr.All = ScoresOf(valid, ranks, "")
		for _, lang := range []string{"ru", "en"} {
			if sc := ScoresOf(valid, ranks, lang); sc.N > 0 {
				vr.ByLang[lang] = sc
			}
		}
		rep.Variants = append(rep.Variants, vr)
	}
	rep.Questions = rows
	rep.Conclusion = conclude(rep)
	return rep, nil
}

// Locate -- на каком месте выдачи нашлась нужная глава и из какой книги первое место.
func Locate(q Question, hits []search.Hit) Place {
	pl := Place{}
	if len(hits) > 0 {
		pl.TopBook = hits[0].Book
	}
	for _, h := range hits {
		if h.Book != q.Book {
			continue
		}
		for _, sec := range h.Sections {
			for _, want := range q.Chapters {
				if book.SameKey(sec.Key, want) {
					pl.Rank = h.Rank
					return pl
				}
			}
		}
	}
	return pl
}

// ScoresOf -- метрики по местам; lang -- только вопросы на этом языке ("" -- все).
func ScoresOf(qs []Question, ranks []Place, lang string) Scores {
	var sc Scores
	for i, q := range qs {
		if lang != "" && q.Lang != lang {
			continue
		}
		sc.N++
		r := ranks[i].Rank
		if r == 1 {
			sc.Hit1++
		}
		if r >= 1 && r <= 3 {
			sc.Hit3++
		}
		if r >= 1 && r <= 5 {
			sc.Hit5++
		}
		if r >= 1 {
			sc.MRR += 1 / float64(r)
		}
		if ranks[i].TopBook == q.Book {
			sc.Book1++
		}
	}
	if sc.N > 0 {
		n := float64(sc.N)
		sc.Hit1, sc.Hit3, sc.Hit5, sc.MRR, sc.Book1 = sc.Hit1/n, sc.Hit3/n, sc.Hit5/n, sc.MRR/n, sc.Book1/n
	}
	return sc
}

func shape(cs []store.Chunk, infos map[string]*search.BookInfo, p chunk.Params) Shape {
	sh := Shape{Chunks: len(cs), Histogram: make([]int, 12)}
	toks := make([]int, len(cs))
	sum, in := 0, 0
	for i, c := range cs {
		toks[i] = c.Tokens
		sum += c.Tokens
		if c.Tokens >= p.Min && c.Tokens <= p.Max {
			in++
		}
		sh.Histogram[min(c.Tokens/100, 11)]++
		if len(c.Sections) > 1 {
			sh.CrossChapter++
		}
		if info := infos[c.Book]; info != nil {
			if !info.SentenceStarts[c.Start] {
				sh.CutStart++
			}
			if !info.SentenceEnds[c.End] {
				sh.CutEnd++
			}
		}
	}
	sort.Ints(toks)
	sh.Min, sh.Max = toks[0], toks[len(toks)-1]
	sh.P50, sh.P90 = toks[len(toks)/2], toks[min(len(toks)-1, len(toks)*9/10)]
	sh.InRange = float64(in) / float64(len(cs))

	// перекрытие: сколько токенов проиндексировано сверх самого текста
	body := 0
	for _, c := range cs {
		if info := infos[c.Book]; info != nil && c.End > c.BodyStart {
			frac := float64(len([]rune(info.Book.Text[c.BodyStart:c.End]))) /
				math.Max(1, float64(len([]rune(info.Book.Text[c.Start:c.End]))))
			body += int(math.Round(float64(c.Tokens) * frac))
		}
	}
	if body > 0 {
		sh.OverlapOverhead = float64(sum-body) / float64(body)
	}
	return sh
}

// boundaries -- насколько разрезы semantic совпали с главами.
func boundaries(cs []store.Chunk, infos map[string]*search.BookInfo) Boundaries {
	var bd Boundaries
	sentences := 0
	for id, info := range infos {
		sents := info.Sentences
		sentences += len(sents)
		idx := map[int]int{} // начало предложения → номер
		for i, s := range sents {
			idx[s.Start] = i
		}
		var cuts []int
		for _, c := range cs {
			if c.Book == id && c.Ordinal > 0 {
				if i, ok := idx[c.BodyStart]; ok {
					cuts = append(cuts, i)
				}
			}
		}
		bd.Cuts += len(cuts)
		for i := 1; i < len(sents); i++ {
			if sents[i].Section == sents[i-1].Section {
				continue
			}
			bd.Chapters++
			for _, cut := range cuts {
				if cut >= i-2 && cut <= i+2 {
					bd.Matched++
					break
				}
			}
		}
	}
	if sentences > 0 {
		// разрез наугад попадает в окно из 5 предложений вокруг границы
		bd.Random = math.Min(float64(bd.Chapters), float64(bd.Chapters)*float64(bd.Cuts)*5/float64(sentences))
	}
	return bd
}

func conclude(rep Report) []string {
	var out []string
	var ready []VariantReport
	for _, v := range rep.Variants {
		if v.Ready && v.All.N > 0 {
			ready = append(ready, v)
		}
	}
	if len(ready) == 0 {
		return nil
	}
	pct := func(x float64) string { return fmt.Sprintf("%.0f%%", 100*x) }

	best := ready[0]
	for _, v := range ready[1:] {
		if v.All.MRR > best.All.MRR {
			best = v
		}
	}
	out = append(out, fmt.Sprintf("Лучше всех по контрольным вопросам — «%s»: нужная глава на первом месте в %s вопросов, MRR %.2f.",
		best.Variant.Title, pct(best.All.Hit1), best.All.MRR))

	byID := map[string]VariantReport{}
	for _, v := range ready {
		byID[v.Variant.ID] = v
	}
	if f, ok := byID["fixed"]; ok {
		if s, ok := byID["structure"]; ok {
			out = append(out, fmt.Sprintf("Фиксированное окно захватывает две главы в %d чанках из %d и начинается посреди предложения в %d; "+
				"по структуре — %d, %d. Разница в поиске: hit@1 %s против %s.",
				f.Shape.CrossChapter, f.Shape.Chunks, f.Shape.CutStart, s.Shape.CrossChapter, s.Shape.CutStart,
				pct(f.All.Hit1), pct(s.All.Hit1)))
		}
	}
	if sem, ok := byID["semantic"]; ok && sem.Boundaries != nil && sem.Boundaries.Chapters > 0 {
		b := sem.Boundaries
		out = append(out, fmt.Sprintf("Смысловая нарезка глав не видит, но у %d из %d границ глав рядом (±2 предложения) оказался её разрез; "+
			"если бы те же %d разрезов стояли наугад, совпало бы около %.0f.", b.Matched, b.Chapters, b.Cuts, b.Random))
	}
	for _, v := range ready {
		if v.Variant.Strategy == index.Structure && v.Variant.ID != "structure" {
			ru, en := v.ByLang["ru"], v.ByLang["en"]
			if ru.N > 0 && en.N > 0 {
				base := byID["structure"].ByLang["ru"]
				out = append(out, fmt.Sprintf("Модель решает не меньше нарезки: «%s» на английских вопросах находит нужную главу первой в %s, "+
					"на русских — в %s (у многоязычной модели на русских — %s).",
					v.Variant.Title, pct(en.Hit1), pct(ru.Hit1), pct(base.Hit1)))
			}
		}
	}
	return out
}
