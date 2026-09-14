package store

import (
	"errors"
	"path/filepath"
	"testing"

	"agent-sprout/internal/agent"
)

func TestKnowledgeSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chats.json")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("открытие: %v", err)
	}
	if _, err := first.AddKnowledge("Железобетон", "Работы вести по СП 63."); err != nil {
		t.Fatalf("добавление знания: %v", err)
	}

	// долговременная память на то и долговременная, что переживает перезапуск
	second, err := Open(path)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}

	list := second.Knowledge()
	if len(list) != 1 || list[0].Title != "Железобетон" {
		t.Fatalf("знание не восстановилось: %+v", list)
	}
	if items := second.KnowledgeItems(); len(items) != 1 || items[0].Text == "" {
		t.Fatalf("агенту знание должно приезжать с текстом: %+v", items)
	}
}

func TestKnowledgeTitlesAreUnique(t *testing.T) {
	s, _ := Open("")

	if _, err := s.AddKnowledge("Железобетон", "СП 63"); err != nil {
		t.Fatalf("первое знание: %v", err)
	}

	// заголовки -- то, по чему диспетчер выбирает знание под задачу; два одинаковых
	// делают выбор бессмысленным
	if _, err := s.AddKnowledge("железобетон", "другой текст"); !errors.Is(err, ErrTitleTaken) {
		t.Fatalf("ожидалась ErrTitleTaken, получено %v", err)
	}
	if list := s.Knowledge(); len(list) != 1 {
		t.Fatalf("занятый заголовок не должен создавать запись: %d", len(list))
	}
}

func TestKnowledgeUpdateAndDelete(t *testing.T) {
	s, _ := Open("")
	record, _ := s.AddKnowledge("Железобетон", "СП 63")

	// пустое поле означает «не менять»: правка заголовка не должна стирать текст
	updated, err := s.UpdateKnowledge(record.ID, "ЖБК", "")
	if err != nil {
		t.Fatalf("правка: %v", err)
	}
	if updated.Title != "ЖБК" || updated.Text != "СП 63" {
		t.Fatalf("правка заголовка не должна трогать текст: %+v", updated)
	}

	if err := s.DeleteKnowledge(record.ID); err != nil {
		t.Fatalf("удаление: %v", err)
	}
	if list := s.Knowledge(); len(list) != 0 {
		t.Fatalf("справочник должен опустеть: %+v", list)
	}
	if err := s.DeleteKnowledge(record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("повторное удаление должно давать ErrNotFound, получено %v", err)
	}
}

func TestKnowledgeIsNotClearedWithChatHistory(t *testing.T) {
	s, _ := Open("")
	if _, err := s.AddKnowledge("Железобетон", "СП 63"); err != nil {
		t.Fatalf("добавление: %v", err)
	}

	chat, _ := s.Create("чат", agent.DefaultConfig("deepseek-v4-flash"))
	if _, err := s.ClearMessages(chat.ID); err != nil {
		t.Fatalf("очистка: %v", err)
	}

	// очистка чата стирает два слоя памяти из трёх: долговременная живёт вне чатов
	if list := s.Knowledge(); len(list) != 1 {
		t.Fatalf("долговременная память не должна зависеть от чатов: %+v", list)
	}
}
