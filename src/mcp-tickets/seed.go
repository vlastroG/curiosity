package main

// Посев тестовых тикетов из json-файла (SEED_FILE). Повторный запуск ничего
// не дублирует: тикет с тем же заголовком уже есть -- пропускаем.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

type seedTicket struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Resolution  string `json:"resolution"`
}

// seed добавляет недостающие тикеты из файла; возвращает, сколько добавлено.
func seed(ctx context.Context, store *Store, path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var items []seedTicket
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, fmt.Errorf("%s не разобрался: %w", path, err)
	}
	for _, it := range items {
		if it.Status != "" && !slices.Contains(Statuses, it.Status) {
			return 0, fmt.Errorf("тикет %q: неизвестный статус %q", it.Title, it.Status)
		}
	}
	added := 0
	for _, it := range items {
		var exists bool
		if err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tickets WHERE title = ?)`, it.Title).Scan(&exists); err != nil {
			return added, err
		}
		if exists {
			continue
		}
		tk, err := store.Create(ctx, it.Title, it.Description)
		if err != nil {
			return added, err
		}
		if it.Status != "" || it.Resolution != "" {
			if _, err := store.Update(ctx, tk.ID, it.Status, it.Resolution); err != nil {
				return added, err
			}
		}
		added++
	}
	return added, nil
}
