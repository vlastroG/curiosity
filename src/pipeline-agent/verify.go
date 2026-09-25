package main

// Проверка цепочки после прогона.
//
// Модель могла сделать что угодно: поискать трижды, законспектировать не ту выдачу,
// сохранить чужой конспект. Проверка смотрит на трассу -- что реально вызывалось и
// что вернулось -- и отвечает на один вопрос: дошли ли данные от поиска до файла
// без подмены. Для этого сверяется каждое звено:
//
//	hn_search.search_id  ==  summarize.аргумент search_id,   hn_search.sha256  == summarize.source_sha256
//	summarize.summary_id ==  save_to_file.аргумент summary_id, summarize.sha256 == save_to_file.summarySha256
//	save_to_file.sha256  ==  sha256 файла в описи сервера (list_files)

import (
	"context"
	"encoding/json"
	"fmt"
)

type searchOut struct {
	SearchID string `json:"search_id"`
	SHA256   string `json:"sha256"`
	Count    int    `json:"count"`
}

type summarizeOut struct {
	SummaryID    string `json:"summary_id"`
	SHA256       string `json:"sha256"`
	SourceID     string `json:"source_id"`
	SourceSHA256 string `json:"source_sha256"`
}

type savedOut struct {
	File       string `json:"file"`
	SHA256     string `json:"sha256"`
	SummaryID  string `json:"summaryId"`
	SummarySHA string `json:"summarySha256"`
	SourceID   string `json:"sourceId"`
	SourceSHA  string `json:"sourceSha256"`
}

// Report -- итог проверки.
type Report struct {
	File       string
	Links      []string
	Violations []string
}

// OK -- цепочка корректна.
func (r Report) OK() bool { return len(r.Violations) == 0 }

// verify проверяет трассу. files -- опись сервера (list_files), чтобы убедиться,
// что файл действительно лежит там и с тем же хешем.
func verify(trace []Step, files []savedOut) Report {
	var r Report
	fail := func(format string, args ...any) { r.Violations = append(r.Violations, fmt.Sprintf(format, args...)) }

	// последнее удачное сохранение -- то, что получил пользователь
	saveAt := -1
	var save savedOut
	var saveArgs struct {
		SummaryID string `json:"summary_id"`
	}
	for i, step := range trace {
		if step.Tool == "save_to_file" && step.OK {
			saveAt = i
		}
	}
	if saveAt < 0 {
		fail("файл не сохранён: удачного вызова save_to_file нет")
		return r
	}
	json.Unmarshal(trace[saveAt].Output, &save)
	json.Unmarshal(trace[saveAt].Arguments, &saveArgs)
	r.File = save.File

	// конспект, который сохранили, -- откуда он
	sumAt := -1
	var sum summarizeOut
	var sumArgs struct {
		SearchID string `json:"search_id"`
	}
	for i := saveAt - 1; i >= 0; i-- {
		var out summarizeOut
		if trace[i].Tool == "summarize" && trace[i].OK && json.Unmarshal(trace[i].Output, &out) == nil && out.SummaryID == saveArgs.SummaryID {
			sumAt, sum = i, out
			json.Unmarshal(trace[i].Arguments, &sumArgs)
			break
		}
	}
	if sumAt < 0 {
		fail("save_to_file получил summary_id=%q, которого не возвращал ни один summarize до него", saveArgs.SummaryID)
		return r
	}
	r.Links = append(r.Links, fmt.Sprintf("шаг %d summarize → %s → шаг %d save_to_file", trace[sumAt].N, sum.SummaryID, trace[saveAt].N))
	if save.SummaryID != sum.SummaryID || save.SummarySHA != sum.SHA256 {
		fail("файл собран не из того конспекта: в файле %s (sha %s), summarize вернул %s (sha %s)",
			save.SummaryID, shortHash(save.SummarySHA), sum.SummaryID, shortHash(sum.SHA256))
	}

	// выдача, по которой сделан конспект
	searchAt := -1
	var search searchOut
	for i := sumAt - 1; i >= 0; i-- {
		var out searchOut
		if trace[i].Tool == "hn_search" && trace[i].OK && json.Unmarshal(trace[i].Output, &out) == nil && out.SearchID == sumArgs.SearchID {
			searchAt, search = i, out
			break
		}
	}
	if searchAt < 0 {
		fail("summarize получил search_id=%q, которого не возвращал ни один hn_search до него", sumArgs.SearchID)
		return r
	}
	r.Links = append([]string{fmt.Sprintf("шаг %d hn_search → %s → шаг %d summarize", trace[searchAt].N, search.SearchID, trace[sumAt].N)}, r.Links...)
	if sum.SourceID != search.SearchID || sum.SourceSHA256 != search.SHA256 {
		fail("конспект сделан не по той выдаче: источник %s (sha %s), hn_search вернул %s (sha %s)",
			sum.SourceID, shortHash(sum.SourceSHA256), search.SearchID, shortHash(search.SHA256))
	}
	if save.SourceID != search.SearchID || save.SourceSHA != search.SHA256 {
		fail("в происхождении файла не та выдача: %s (sha %s)", save.SourceID, shortHash(save.SourceSHA))
	}
	if search.Count == 0 {
		fail("законспектирована пустая выдача %s", search.SearchID)
	}

	// файл на месте и не изменился
	found := false
	for _, f := range files {
		if f.File == save.File {
			found = true
			if f.SHA256 != save.SHA256 {
				fail("файл %s на сервере изменился: sha %s, а сохраняли %s", f.File, shortHash(f.SHA256), shortHash(save.SHA256))
			}
		}
	}
	if !found {
		fail("файла %s нет в описи сервера", save.File)
	}
	return r
}

// fetchFiles -- опись сервера для проверки.
func fetchFiles(ctx context.Context, tools ToolBox) ([]savedOut, error) {
	result, err := tools.Call(ctx, "list_files", "")
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return nil, fmt.Errorf("list_files: %s", result.Text)
	}
	var out struct {
		Files []savedOut `json:"files"`
	}
	if err := json.Unmarshal(result.Structured, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}
