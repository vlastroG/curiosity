package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSeedIdempotent(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.Create(ctx, "Уже есть", "создан руками"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "seed.json")
	os.WriteFile(path, []byte(`[
		{"title":"Уже есть","description":"дубль"},
		{"title":"Вкладка не появляется","description":"после обновления","status":"resolved","resolution":"pyrevit attach"},
		{"title":"Медленный старт","description":"минута на загрузку"}
	]`), 0o644)

	added, err := seed(ctx, store, path)
	if err != nil || added != 2 {
		t.Fatalf("первый посев: добавлено %d, ошибка %v", added, err)
	}
	if added, err := seed(ctx, store, path); err != nil || added != 0 {
		t.Fatalf("повторный посев: добавлено %d, ошибка %v", added, err)
	}

	all, _ := store.List(ctx, "", 100)
	if len(all) != 3 {
		t.Fatalf("тикетов %d", len(all))
	}
	found, _ := store.Search(ctx, "вкладка", 5)
	if len(found) != 1 || found[0].Status != "resolved" || found[0].Resolution != "pyrevit attach" {
		t.Errorf("засеянный тикет: %+v", found)
	}
}

func TestSeedBadStatus(t *testing.T) {
	store, _ := openStore(filepath.Join(t.TempDir(), "tickets.db"))
	defer store.Close()
	path := filepath.Join(t.TempDir(), "seed.json")
	os.WriteFile(path, []byte(`[{"title":"x","description":"y","status":"done"}]`), 0o644)
	if _, err := seed(context.Background(), store, path); err == nil {
		t.Error("неизвестный статус в seed должен быть ошибкой")
	}
}
