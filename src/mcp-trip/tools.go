package main

// Инструменты сервера планов.
//
// Все, кроме trip_create, принимают trip_id -- так модель связывает шаги одного
// плана. Порядок: trip_create → trip_set_budget (если есть бюджет) →
// trip_add_day на каждый день → trip_publish. trip_get -- для интерфейса.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IDInput -- только trip_id.
type IDInput struct {
	TripID string `json:"trip_id" jsonschema:"id плана из trip_create"`
}

// BudgetInput -- аргументы trip_set_budget.
type BudgetInput struct {
	TripID string `json:"trip_id" jsonschema:"id плана из trip_create"`
	Budget
}

// DayInput -- аргументы trip_add_day.
type DayInput struct {
	TripID string `json:"trip_id" jsonschema:"id плана из trip_create"`
	Day
}

// PublishInput -- аргументы trip_publish.
type PublishInput struct {
	TripID  string `json:"trip_id" jsonschema:"id плана из trip_create"`
	Summary string `json:"summary" jsonschema:"2–4 предложения о поездке: погода, главное, что посмотреть, на что хватит бюджета"`
}

// TripOutput -- ответ инструментов, меняющих план.
type TripOutput struct {
	TripID  string   `json:"trip_id" jsonschema:"id плана"`
	Days    int      `json:"days" jsonschema:"сколько дней уже в плане"`
	Missing []string `json:"missing" jsonschema:"дни поездки, которых ещё нет в плане"`
	File    string   `json:"file,omitempty" jsonschema:"имя HTML-файла после публикации"`
}

func newServer(store *Store, outDir string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-trip", Version: version}, nil)

	state := func(t Trip) TripOutput {
		missing := t.Missing()
		if missing == nil {
			missing = []string{}
		}
		return TripOutput{TripID: t.ID, Days: len(t.Days), Missing: missing, File: t.File}
	}
	status := func(t Trip) string {
		if m := t.Missing(); len(m) > 0 {
			return fmt.Sprintf("План %s: дней %d, ещё нет: %s.", t.ID, len(t.Days), strings.Join(m, ", "))
		}
		return fmt.Sprintf("План %s: все %d дн. на месте, можно публиковать.", t.ID, len(t.Days))
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "trip_create",
		Description: "Завести план поездки. Город, страну, код страны и координаты бери из find_city. " +
			"Возвращает trip_id для остальных инструментов плана. В notes запиши допущения, если что-то пришлось додумать.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in CreateInput) (*mcp.CallToolResult, TripOutput, error) {
		trip, err := store.Create(in)
		if err != nil {
			return nil, TripOutput{}, err
		}
		return textResult(fmt.Sprintf("Заведён план %s: %s, %s — %s, путешественников %d. %s",
			trip.ID, trip.City, trip.StartDate, trip.EndDate, trip.Travelers, status(trip))), state(trip), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "trip_set_budget",
		Description: "Записать бюджет в план. Суммы и курс бери из convert и budget_split сервера денег, не считай сам. " +
			"Если бюджет не задан -- не вызывай.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in BudgetInput) (*mcp.CallToolResult, TripOutput, error) {
		trip, err := store.SetBudget(in.TripID, in.Budget)
		if err != nil {
			return nil, TripOutput{}, err
		}
		return textResult("Бюджет записан. " + status(trip)), state(trip), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "trip_add_day",
		Description: "Добавить в план один день: погоду из trip_weather и 1–6 пунктов -- места из sights/sight_info " +
			"с короткими описаниями и ссылками. Дата -- внутри поездки. Повторный вызов с той же датой заменяет день.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in DayInput) (*mcp.CallToolResult, TripOutput, error) {
		trip, err := store.AddDay(in.TripID, in.Day)
		if err != nil {
			return nil, TripOutput{}, err
		}
		return textResult(fmt.Sprintf("День %s записан. %s", in.Date, status(trip))), state(trip), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "trip_publish",
		Description: "Опубликовать план в HTML. Последний шаг: вызывай, когда все дни добавлены. " +
			"Неполный план сервер не опубликует и скажет, каких дней не хватает.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in PublishInput) (*mcp.CallToolResult, TripOutput, error) {
		trip, err := store.Publish(in.TripID, in.Summary, outDir)
		if err != nil {
			return nil, TripOutput{}, err
		}
		return textResult(fmt.Sprintf("План %s опубликован: %s.", trip.ID, trip.File)), state(trip), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "trip_get",
		Description: "План целиком: даты, дни, бюджет, допущения, файл. Для просмотра, не для сборки.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in IDInput) (*mcp.CallToolResult, Trip, error) {
		trip, err := store.Get(in.TripID)
		if err != nil {
			return nil, Trip{}, err
		}
		return textResult(status(trip)), trip, nil
	})

	return server
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
