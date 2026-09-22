package main

// Клиент Open-Meteo -- бесплатного API погоды.
//
// Выбран он ровно за одно свойство: ни ключа, ни регистрации, ни квоты на карту.
// MCP-серверу, которому нужен секрет, пришлось бы объяснять, где взять ключ и куда
// его положить; этот запускается одной командой и работает.
//
// Эндпоинтов два, и они разные по смыслу: геокодер переводит название в координаты,
// прогноз работает только по координатам. Поэтому «погода в Москве» -- это всегда
// два запроса, и первый из них может ответить неоднозначно.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Базы API вынесены в переменные, чтобы тест подставил httptest.Server:
// проверки этого модуля в сеть не ходят.
var (
	geocodeBase  = "https://geocoding-api.open-meteo.com/v1/search"
	forecastBase = "https://api.open-meteo.com/v1/forecast"
)

// ErrPlaceNotFound -- геокодер не знает такого названия.
//
// Отдельная ошибка, потому что это не поломка: так выглядит опечатка или деревня,
// которой нет в справочнике. Модель должна получить её текстом и переспросить
// человека, а не увидеть сбой инструмента.
var ErrPlaceNotFound = errors.New("место не найдено")

// Place -- населённый пункт, как его знает геокодер.
type Place struct {
	Name       string  `json:"name" jsonschema:"название места, как его вернул геокодер"`
	Country    string  `json:"country,omitempty" jsonschema:"страна"`
	Region     string  `json:"region,omitempty" jsonschema:"регион или область"`
	Latitude   float64 `json:"latitude" jsonschema:"широта"`
	Longitude  float64 `json:"longitude" jsonschema:"долгота"`
	Timezone   string  `json:"timezone,omitempty" jsonschema:"часовой пояс места"`
	Population int     `json:"population,omitempty" jsonschema:"население, если известно"`
}

// Title -- место одной строкой: «Москва, Россия».
func (p Place) Title() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{p.Name, p.Region, p.Country} {
		if part != "" && !contains(parts, part) {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ", ")
}

// Conditions -- погода прямо сейчас.
type Conditions struct {
	Time          string  `json:"time" jsonschema:"местное время наблюдения"`
	Weather       string  `json:"weather" jsonschema:"словесное описание погоды"`
	Temperature   float64 `json:"temperature" jsonschema:"температура воздуха в градусах Цельсия"`
	FeelsLike     float64 `json:"feelsLike" jsonschema:"как ощущается, в градусах Цельсия"`
	Humidity      int     `json:"humidity" jsonschema:"относительная влажность в процентах"`
	Precipitation float64 `json:"precipitation" jsonschema:"осадки за последний час в миллиметрах"`
	WindSpeed     float64 `json:"windSpeed" jsonschema:"скорость ветра в километрах в час"`
	Code          int     `json:"code" jsonschema:"код погоды WMO"`
}

// DayForecast -- одни сутки прогноза.
type DayForecast struct {
	Date         string  `json:"date" jsonschema:"дата в формате ГГГГ-ММ-ДД"`
	Weather      string  `json:"weather" jsonschema:"словесное описание погоды"`
	Min          float64 `json:"min" jsonschema:"минимальная температура за сутки"`
	Max          float64 `json:"max" jsonschema:"максимальная температура за сутки"`
	PrecipChance int     `json:"precipChance" jsonschema:"вероятность осадков в процентах"`
	PrecipSum    float64 `json:"precipSum" jsonschema:"сумма осадков за сутки в миллиметрах"`
	WindMax      float64 `json:"windMax" jsonschema:"максимальный ветер в километрах в час"`
	Code         int     `json:"code" jsonschema:"код погоды WMO"`
}

// geocode переводит название места в координаты.
//
// count больше единицы даже когда нужен один ответ: одноимённых мест много --
// «Москва» есть и в России, и в Айдахо, -- и выбор между ними это отдельный
// инструмент, а не догадка внутри запроса погоды.
func geocode(ctx context.Context, client *http.Client, name, countryCode string, count int) ([]Place, error) {
	query := url.Values{}
	query.Set("name", name)
	query.Set("count", strconv.Itoa(count))
	query.Set("language", "ru")
	query.Set("format", "json")
	if countryCode != "" {
		query.Set("countryCode", strings.ToUpper(countryCode))
	}

	var body struct {
		Results []struct {
			Name       string  `json:"name"`
			Country    string  `json:"country"`
			Admin1     string  `json:"admin1"`
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
			Timezone   string  `json:"timezone"`
			Population int     `json:"population"`
		} `json:"results"`
	}
	if err := fetch(ctx, client, geocodeBase, query, &body); err != nil {
		return nil, err
	}

	// ненайденное место у Open-Meteo -- это отсутствие ключа results в теле,
	// а не пустой массив и не код ошибки
	if len(body.Results) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrPlaceNotFound, name)
	}

	places := make([]Place, 0, len(body.Results))
	for _, item := range body.Results {
		places = append(places, Place{
			Name:       item.Name,
			Country:    item.Country,
			Region:     item.Admin1,
			Latitude:   item.Latitude,
			Longitude:  item.Longitude,
			Timezone:   item.Timezone,
			Population: item.Population,
		})
	}
	return places, nil
}

// forecast -- погода сейчас и прогноз по суткам для точки на карте.
//
// timezone=auto важнее, чем кажется: сутки прогноза должны совпадать с сутками
// на объекте, иначе «завтра» в плане работ будет означать чужое завтра.
func forecast(ctx context.Context, client *http.Client, place Place, days int) (Conditions, []DayForecast, error) {
	query := url.Values{}
	query.Set("latitude", formatCoord(place.Latitude))
	query.Set("longitude", formatCoord(place.Longitude))
	query.Set("timezone", "auto")
	query.Set("forecast_days", strconv.Itoa(days))
	query.Set("current", strings.Join([]string{
		"temperature_2m", "apparent_temperature", "relative_humidity_2m",
		"precipitation", "weather_code", "wind_speed_10m",
	}, ","))
	query.Set("daily", strings.Join([]string{
		"temperature_2m_max", "temperature_2m_min", "precipitation_probability_max",
		"precipitation_sum", "weather_code", "wind_speed_10m_max",
	}, ","))

	var body struct {
		Current struct {
			Time          string  `json:"time"`
			Temperature   float64 `json:"temperature_2m"`
			FeelsLike     float64 `json:"apparent_temperature"`
			Humidity      int     `json:"relative_humidity_2m"`
			Precipitation float64 `json:"precipitation"`
			Code          int     `json:"weather_code"`
			WindSpeed     float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Daily struct {
			Time         []string  `json:"time"`
			Max          []float64 `json:"temperature_2m_max"`
			Min          []float64 `json:"temperature_2m_min"`
			PrecipChance []int     `json:"precipitation_probability_max"`
			PrecipSum    []float64 `json:"precipitation_sum"`
			Code         []int     `json:"weather_code"`
			WindMax      []float64 `json:"wind_speed_10m_max"`
		} `json:"daily"`
	}
	if err := fetch(ctx, client, forecastBase, query, &body); err != nil {
		return Conditions{}, nil, err
	}

	current := Conditions{
		Time:          body.Current.Time,
		Weather:       weatherText(body.Current.Code),
		Temperature:   body.Current.Temperature,
		FeelsLike:     body.Current.FeelsLike,
		Humidity:      body.Current.Humidity,
		Precipitation: body.Current.Precipitation,
		WindSpeed:     body.Current.WindSpeed,
		Code:          body.Current.Code,
	}

	// массивы daily параллельные, и провайдер вправе прислать их разной длины --
	// скажем, когда часть показателей для этой точки не считается. Идём по самому
	// короткому, иначе первый же такой ответ уронит сервер на индексе
	length := len(body.Daily.Time)
	for _, n := range []int{
		len(body.Daily.Max), len(body.Daily.Min), len(body.Daily.PrecipChance),
		len(body.Daily.PrecipSum), len(body.Daily.Code), len(body.Daily.WindMax),
	} {
		if n < length {
			length = n
		}
	}

	forecastDays := make([]DayForecast, 0, length)
	for i := range length {
		forecastDays = append(forecastDays, DayForecast{
			Date:         body.Daily.Time[i],
			Weather:      weatherText(body.Daily.Code[i]),
			Min:          body.Daily.Min[i],
			Max:          body.Daily.Max[i],
			PrecipChance: body.Daily.PrecipChance[i],
			PrecipSum:    body.Daily.PrecipSum[i],
			WindMax:      body.Daily.WindMax[i],
			Code:         body.Daily.Code[i],
		})
	}

	return current, forecastDays, nil
}

// fetch -- один GET к Open-Meteo с разбором тела.
func fetch(ctx context.Context, client *http.Client, base string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("запрос к Open-Meteo не собрался: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Open-Meteo не ответил: %w", err)
	}
	defer resp.Body.Close()

	// потолок на тело: чужой сервис не обязан быть вежливым
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("чтение ответа Open-Meteo: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Open-Meteo ответил %d: %s", resp.StatusCode, trim(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ответ Open-Meteo не разобрался как json: %s", trim(string(body)))
	}
	return nil
}

// formatCoord -- координата без экспоненты и без хвоста нулей.
func formatCoord(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
