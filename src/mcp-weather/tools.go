package main

// Инструменты сервера: что он умеет и как об этом рассказывает.
//
// Описания инструментов и параметров здесь -- не документация для человека.
// Их читает модель, и по ним она решает, звать инструмент или обойтись. Поэтому
// в описании сказано не только что делает инструмент, но и когда его звать:
// «работы на улице, надо понять, можно ли работать сейчас» -- это подсказка
// к решению, а не пересказ имени функции.
//
// Схемы входа и выхода не пишутся руками: AddTool выводит их из Go-структур,
// а теги jsonschema дают описания полей. Поле без omitempty становится
// обязательным, с omitempty -- необязательным.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Границы, внутри которых у параметров есть смысл.
const (
	defaultPlaces = 5
	maxPlaces     = 10
	defaultDays   = 3
	maxDays       = 7
)

// FindPlaceInput -- аргументы find_place.
type FindPlaceInput struct {
	Name  string `json:"name" jsonschema:"название населённого пункта, например «Москва» или «Нижний Тагил»"`
	Limit int    `json:"limit,omitempty" jsonschema:"сколько вариантов вернуть, от 1 до 10; по умолчанию 5"`
}

// FindPlaceOutput -- результат find_place.
type FindPlaceOutput struct {
	Places []Place `json:"places" jsonschema:"подходящие места, самые населённые первыми"`
}

// ForecastInput -- аргументы get_forecast.
type ForecastInput struct {
	Place       string `json:"place" jsonschema:"город или посёлок, где ведутся работы"`
	CountryCode string `json:"countryCode,omitempty" jsonschema:"код страны из двух букв (RU, KZ, BY), если название встречается в нескольких странах"`
	Days        int    `json:"days,omitempty" jsonschema:"на сколько суток вперёд нужен прогноз, от 1 до 7; по умолчанию 3"`
}

// ForecastOutput -- результат get_forecast.
type ForecastOutput struct {
	Place   Place         `json:"place" jsonschema:"место, которое удалось разобрать по названию"`
	Current Conditions    `json:"current" jsonschema:"погода на момент запроса"`
	Days    []DayForecast `json:"days" jsonschema:"прогноз по суткам"`
	Hint    string        `json:"hint" jsonschema:"что нынешние условия значат для наружных работ"`
}

// newServer собирает сервер со всеми инструментами.
//
// Клиент берётся параметром, а не создаётся внутри: тесту нужно подсунуть свой,
// а рабочему процессу -- один общий с разумным таймаутом.
func newServer(client *http.Client) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-weather", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "find_place",
		Description: "Найти населённый пункт по названию: координаты, страна, регион, часовой пояс. " +
			"Нужен, когда название неоднозначно — одноимённые города есть в разных странах. " +
			"Ради одной только погоды вызывать не обязательно: get_forecast ищет место сам.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FindPlaceInput) (*mcp.CallToolResult, FindPlaceOutput, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, FindPlaceOutput{}, fmt.Errorf("не указано название места")
		}

		places, err := geocode(ctx, client, name, "", clamp(in.Limit, defaultPlaces, 1, maxPlaces))
		if err != nil {
			// ошибку возвращаем как есть: SDK сам завернёт её в результат с isError,
			// и модель прочитает причину текстом
			return nil, FindPlaceOutput{}, err
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: renderPlaces(places)}},
		}, FindPlaceOutput{Places: places}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_forecast",
		Description: "Фактическая погода и прогноз на несколько суток в месте работ. " +
			"Вызывай, когда работы идут на улице или в неотапливаемом помещении и надо понять, " +
			"можно ли работать сейчас, сколько будут сохнуть смеси и не помешают ли осадки. " +
			"Данные настоящие, от метеослужбы, а не оценка.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ForecastInput) (*mcp.CallToolResult, ForecastOutput, error) {
		name := strings.TrimSpace(in.Place)
		if name == "" {
			return nil, ForecastOutput{}, fmt.Errorf("не указано место работ")
		}
		days := clamp(in.Days, defaultDays, 1, maxDays)

		// берём один вариант: выбирать между одноимёнными городами -- работа
		// find_place, и решать её молча внутри прогноза нельзя
		places, err := geocode(ctx, client, name, strings.TrimSpace(in.CountryCode), 1)
		if err != nil {
			return nil, ForecastOutput{}, err
		}
		place := places[0]

		current, forecastDays, err := forecast(ctx, client, place, days)
		if err != nil {
			return nil, ForecastOutput{}, err
		}

		out := ForecastOutput{
			Place:   place,
			Current: current,
			Days:    forecastDays,
			Hint:    workHint(current),
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: renderForecast(out)}},
		}, out, nil
	})

	return server
}

// renderPlaces -- список мест словами.
func renderPlaces(places []Place) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Найдено мест: %d\n", len(places))
	for _, place := range places {
		fmt.Fprintf(&out, "\n- %s (%.4f, %.4f), часовой пояс %s",
			place.Title(), place.Latitude, place.Longitude, place.Timezone)
		if place.Population > 0 {
			fmt.Fprintf(&out, ", население %d", place.Population)
		}
	}
	return out.String()
}

// renderForecast -- погода словами.
//
// Форматирование живёт на сервере, а не у того, кто его вызвал: как читается
// погода, знает тот, кто знает единицы измерения. Клиенту остаётся вставить
// готовый текст в диалог.
func renderForecast(data ForecastOutput) string {
	var out strings.Builder

	fmt.Fprintf(&out, "Погода: %s, часовой пояс %s, местное время %s\n\n",
		data.Place.Title(), data.Place.Timezone, data.Current.Time)

	fmt.Fprintf(&out, "Сейчас: %s, %s (ощущается %s), влажность %d%%, ветер %.0f км/ч",
		data.Current.Weather,
		celsius(data.Current.Temperature),
		signed(data.Current.FeelsLike),
		data.Current.Humidity,
		data.Current.WindSpeed)
	if data.Current.Precipitation > 0 {
		fmt.Fprintf(&out, ", осадки %.1f мм/ч", data.Current.Precipitation)
	}
	fmt.Fprintf(&out, "\nДля наружных работ: %s\n", data.Hint)

	if len(data.Days) > 0 {
		out.WriteString("\nПрогноз по суткам:\n")
		for _, day := range data.Days {
			fmt.Fprintf(&out, "  %s: %s, от %s до %s, вероятность осадков %d%%, за сутки %.1f мм, ветер до %.0f км/ч\n",
				day.Date, day.Weather, celsius(day.Min), celsius(day.Max),
				day.PrecipChance, day.PrecipSum, day.WindMax)
		}
	}

	return out.String()
}

// signed -- температура со знаком: «+16.1», «-3.0».
//
// Знак плюса важен не для красоты: без него «5» и «-5» отличаются одним символом,
// а для мокрых работ это разница между «медленно» и «нельзя».
func signed(value float64) string {
	sign := ""
	if value > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.1f", sign, value)
}

// celsius -- то же с единицей измерения.
func celsius(value float64) string { return signed(value) + " °C" }

// clamp -- значение в границах, ноль означает «по умолчанию».
//
// Аргумент вне границ не отклоняем: модель, попросившая прогноз на 30 суток,
// должна получить прогноз на семь, а не отказ. Столько же пользы, меньше ходов.
func clamp(value, fallback, low, high int) int {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
