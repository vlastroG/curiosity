package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-sprout/internal/agent"
	"agent-sprout/internal/store"
)

// Deps -- всё, что нужно HTTP-слою. Собирается в main и передаётся сюда явно,
// чтобы пакет не лез в глобальные переменные и легко поднимался в тестах.
type Deps struct {
	Agent        *agent.Agent
	Store        *store.Store
	DefaultModel string
	StaticDir    string
	// TurnTimeout -- сколько ход может идти целиком. Отсчитывается от начала хода,
	// а не от вызова модели: ход состоит из нескольких вызовов
	TurnTimeout time.Duration
}

// New собирает маршрутизатор.
//
// Используется штатный http.ServeMux: с Go 1.22 он понимает метод и параметры пути,
// так что внешний роутер проекту не нужен -- зависимостей у бэкенда нет вообще.
func New(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/catalog", deps.handleCatalog)

	mux.HandleFunc("GET /api/chats", deps.handleListChats)
	mux.HandleFunc("POST /api/chats", deps.handleCreateChat)
	mux.HandleFunc("GET /api/chats/{id}", deps.handleGetChat)
	mux.HandleFunc("PATCH /api/chats/{id}", deps.handlePatchChat)
	mux.HandleFunc("DELETE /api/chats/{id}", deps.handleDeleteChat)
	mux.HandleFunc("POST /api/chats/{id}/checkpoint", deps.handleCheckpoint)
	mux.HandleFunc("POST /api/chats/{id}/messages", deps.handlePostMessage)
	mux.HandleFunc("DELETE /api/chats/{id}/messages", deps.handleClearMessages)
	mux.HandleFunc("POST /api/chats/{id}/task/cancel", deps.handleCancelTask)

	// профиль один на всё приложение -- отсюда маршруты без идентификатора
	mux.HandleFunc("GET /api/profile", deps.handleGetProfile)
	mux.HandleFunc("PUT /api/profile", deps.handleSaveProfile)

	// долговременная память общая для всех чатов, поэтому и маршруты у неё свои
	mux.HandleFunc("GET /api/knowledge", deps.handleListKnowledge)
	mux.HandleFunc("POST /api/knowledge", deps.handleCreateKnowledge)
	mux.HandleFunc("PATCH /api/knowledge/{id}", deps.handleUpdateKnowledge)
	mux.HandleFunc("DELETE /api/knowledge/{id}", deps.handleDeleteKnowledge)

	// всё остальное под /api -- явная 404 в том же формате, что и прочие ошибки,
	// иначе клиент получит HTML страницы вместо JSON
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "нет такого метода API")
	})

	mux.Handle("/", staticHandler(deps.StaticDir))

	return mux
}

// staticHandler раздаёт собранный фронтенд с откатом на index.html.
//
// Откат нужен, потому что интерфейс -- одностраничное приложение: любой путь,
// кроме реально существующего файла, должен вернуть ту же страницу.
//
// Заголовки кеширования здесь обязательны. Без Cache-Control браузер кеширует ответ
// по эвристике -- на долю времени, прошедшего с Last-Modified. Для index.html это
// ловушка: страница всего лишь указывает на бандл с хешем в имени, и закешированная
// страница продолжает грузить СТАРЫЙ бандл. Правка интерфейса при этом не доезжает
// до пользователя вообще, а выглядит как «починил, но не работает».
func staticHandler(dir string) http.Handler {
	if dir == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "статика не собрана: STATIC_DIR не задан", http.StatusNotFound)
		})
	}

	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Join(dir, filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")))
		if info, err := os.Stat(clean); err == nil && !info.IsDir() {
			w.Header().Set("Cache-Control", cacheControl(r.URL.Path))
			files.ServeHTTP(w, r)
			return
		}

		// страница-указатель: перепроверять при каждом заходе. Ревалидация дешёвая --
		// Last-Modified отдаётся, и обычно это 304 без тела
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

// cacheControl -- как долго файлу можно лежать в кеше браузера.
//
// Всё, что собрано сборщиком, лежит в /assets и содержит хеш содержимого в имени:
// поменялось содержимое -- поменялось имя. Такое кешируется навсегда. Остальное
// (favicon и прочие файлы с постоянным именем) -- только с перепроверкой.
func cacheControl(path string) string {
	if strings.HasPrefix(path, "/assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}
