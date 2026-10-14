package main

// serve -- HTTP-обёртка над моделью в формате OpenAI /v1/chat/completions,
// чтобы другие сервисы (doc-index) ходили к локальной модели как к облачной.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

type server struct {
	c      *client
	numCtx int       // контекстное окно по умолчанию; 0 до готовности -- максимум модели
	info   modelInfo // сведения о модели; info.ContextLength -- её максимум
	ready  atomic.Bool
}

func serve(ctx context.Context, c *client, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":8080", "адрес HTTP-сервера")
	defCtx, _ := strconv.Atoi(os.Getenv("LLMCLI_CTX"))
	numCtx := fs.Int("ctx", defCtx, "контекстное окно в токенах; 0 -- максимум модели (env LLMCLI_CTX)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s := &server{c: c, numCtx: *numCtx}
	go s.prepare(ctx)
	srv := &http.Server{Addr: *addr, Handler: s.handler()}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	log.Printf("llmcli serve: %s, модель %s", *addr, model)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// prepare ждёт Ollama, скачивает модель при необходимости и узнаёт её контекст.
func (s *server) prepare(ctx context.Context) {
	for {
		info, err := s.c.show(ctx, model)
		if errors.Is(err, errNotFound) {
			log.Printf("скачиваю %s...", model)
			if err = s.c.pull(ctx, model); err == nil {
				continue
			}
		}
		if err == nil {
			s.info = info
			if s.numCtx <= 0 || (info.ContextLength > 0 && s.numCtx > info.ContextLength) {
				s.numCtx = info.ContextLength
			}
			s.ready.Store(true)
			log.Printf("модель готова, контекст %d токенов", s.numCtx)
			return
		}
		log.Printf("модель не готова: %v; повтор через 5 с", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			writeError(w, http.StatusServiceUnavailable, "модель ещё скачивается")
			return
		}
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"model": model, "ready": s.ready.Load()}
		if s.ready.Load() {
			out["parameterSize"] = s.info.ParameterSize
			out["quantization"] = s.info.Quantization
			out["contextLength"] = s.info.ContextLength
			out["numCtx"] = s.numCtx
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /v1/chat/completions", s.complete)
	return mux
}

func (s *server) complete(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeError(w, http.StatusServiceUnavailable, "модель ещё скачивается")
		return
	}
	var req struct {
		Messages    []message `json:"messages"`
		MaxTokens   int       `json:"max_tokens"`
		Temperature *float64  `json:"temperature"`
		NumCtx      int       `json:"num_ctx"` // расширение: контекстное окно на запрос
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	numCtx := s.numCtx
	if req.NumCtx > 0 {
		numCtx = req.NumCtx
		if limit := s.info.ContextLength; limit > 0 && numCtx > limit {
			numCtx = limit
		}
	}
	// ответ вместе с запросом всё равно ограничен контекстом
	opts := map[string]any{"num_ctx": numCtx}
	if req.MaxTokens > 0 {
		opts["num_predict"] = min(req.MaxTokens, numCtx)
	}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	var out bytes.Buffer
	st, err := s.c.chat(r.Context(), chatRequest{Model: model, Messages: req.Messages, Options: opts}, &out)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	finish := "stop"
	if st.DoneReason == "length" {
		finish = "length"
	}
	resp := map[string]any{
		"model": model,
		"choices": []map[string]any{{
			"message":       message{Role: "assistant", Content: out.String()},
			"finish_reason": finish,
		}},
		"usage": map[string]int{"prompt_tokens": st.PromptEvalCount, "completion_tokens": st.EvalCount},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg}})
}
