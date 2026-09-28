package main

// Погода на даты поездки.
//
// Отдельно от get_forecast, потому что вопрос другой. Прорабу нужна погода на
// объекте сейчас и на ближайшие дни. Путешественнику -- на конкретные даты,
// которые могут быть и через два месяца, когда прогноза ещё нет. Поэтому:
//
//	поездка целиком в пределах 16 дней ──► прогноз Open-Meteo на эти даты
//	дальше                              ──► климатическая норма: те же календарные
//	                                        дни за пять прошлых лет из архива
//
// Норма честно называется нормой: «обычно в эти дни +18…+23, дождь в 2 годах из 5»
// -- это не прогноз, и модель, и человек должны это видеть.
//
// Инструмент работает по координатам, а не по названию: координаты даёт сервер
// мест, и агент, который строит поездку, передаёт их сюда. Так одно место не
// геокодируется дважды разными сервисами с риском получить два разных города.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// archiveBase -- архив наблюдений Open-Meteo.
var archiveBase = "https://archive-api.open-meteo.com/v1/archive"

// Границы запроса.
const (
	forecastHorizonDays = 16
	maxTripDays         = 16
	climateYears        = 5
	// rainyDayMM -- с какого количества осадков день считается дождливым
	rainyDayMM = 1.0
)

// Источники данных о погоде.
const (
	SourceForecast = "forecast"
	SourceClimate  = "climate"
)

// TripWeatherInput -- аргументы trip_weather.
type TripWeatherInput struct {
	Latitude  float64 `json:"latitude" jsonschema:"широта места поездки, например из find_city сервера мест"`
	Longitude float64 `json:"longitude" jsonschema:"долгота места поездки"`
	StartDate string  `json:"start_date" jsonschema:"первый день поездки, ГГГГ-ММ-ДД"`
	EndDate   string  `json:"end_date" jsonschema:"последний день поездки, ГГГГ-ММ-ДД; не больше 16 дней от начала"`
}

// TripDay -- погода одного дня поездки.
type TripDay struct {
	Date         string  `json:"date" jsonschema:"дата, ГГГГ-ММ-ДД"`
	Weather      string  `json:"weather" jsonschema:"погода словами"`
	Min          float64 `json:"min" jsonschema:"минимальная температура, °C"`
	Max          float64 `json:"max" jsonschema:"максимальная температура, °C"`
	PrecipChance int     `json:"precipChance" jsonschema:"вероятность осадков, %; для нормы -- доля лет с дождём в этот день"`
	PrecipSum    float64 `json:"precipSum" jsonschema:"осадки за сутки, мм; для нормы -- среднее за годы"`
	WindMax      float64 `json:"windMax" jsonschema:"максимальный ветер, км/ч"`
}

// TripWeatherOutput -- результат trip_weather.
type TripWeatherOutput struct {
	Latitude  float64   `json:"latitude" jsonschema:"широта"`
	Longitude float64   `json:"longitude" jsonschema:"долгота"`
	Timezone  string    `json:"timezone" jsonschema:"часовой пояс места"`
	StartDate string    `json:"startDate" jsonschema:"первый день"`
	EndDate   string    `json:"endDate" jsonschema:"последний день"`
	Source    string    `json:"source" jsonschema:"forecast -- прогноз; climate -- климатическая норма по прошлым годам"`
	Note      string    `json:"note" jsonschema:"как понимать эти данные"`
	Days      []TripDay `json:"days" jsonschema:"погода по дням поездки"`
}

// ErrBadTrip -- даты или координаты не годятся.
var ErrBadTrip = errors.New("неверные параметры поездки")

// tripWeather выбирает источник по датам и собирает погоду по дням.
func tripWeather(ctx context.Context, client *http.Client, in TripWeatherInput, today time.Time) (TripWeatherOutput, error) {
	start, end, err := tripDates(in, today)
	if err != nil {
		return TripWeatherOutput{}, err
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return TripWeatherOutput{}, fmt.Errorf("%w: координаты вне Земли (%v, %v)", ErrBadTrip, in.Latitude, in.Longitude)
	}

	out := TripWeatherOutput{
		Latitude:  in.Latitude,
		Longitude: in.Longitude,
		StartDate: start.Format(time.DateOnly),
		EndDate:   end.Format(time.DateOnly),
	}

	horizon := dayOf(today).AddDate(0, 0, forecastHorizonDays-1)
	if !end.After(horizon) {
		out.Source = SourceForecast
		out.Note = "Прогноз погоды на даты поездки."
		out.Timezone, out.Days, err = forecastRange(ctx, client, in.Latitude, in.Longitude, start, end)
	} else {
		out.Source = SourceClimate
		out.Note = fmt.Sprintf("Даты дальше %d дней, прогноза ещё нет. Это климатическая норма: "+
			"те же календарные дни за %d прошлых лет. Вероятность осадков -- доля лет, когда в этот день шёл дождь.",
			forecastHorizonDays, climateYears)
		out.Timezone, out.Days, err = climateRange(ctx, client, in.Latitude, in.Longitude, start, end, today)
	}
	if err != nil {
		return TripWeatherOutput{}, err
	}
	return out, nil
}

// tripDates разбирает и проверяет даты поездки.
func tripDates(in TripWeatherInput, today time.Time) (time.Time, time.Time, error) {
	start, err := time.Parse(time.DateOnly, strings.TrimSpace(in.StartDate))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: start_date %q не в формате ГГГГ-ММ-ДД", ErrBadTrip, in.StartDate)
	}
	end, err := time.Parse(time.DateOnly, strings.TrimSpace(in.EndDate))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: end_date %q не в формате ГГГГ-ММ-ДД", ErrBadTrip, in.EndDate)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: поездка кончается раньше, чем начинается", ErrBadTrip)
	}
	if days := int(end.Sub(start).Hours()/24) + 1; days > maxTripDays {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: поездка на %d дней, погоду даю не больше чем на %d", ErrBadTrip, days, maxTripDays)
	}
	if start.Before(dayOf(today)) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: поездка начинается в прошлом (%s)", ErrBadTrip, in.StartDate)
	}
	return start, end, nil
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type dailyBody struct {
	Timezone string `json:"timezone"`
	Daily    struct {
		Time         []string   `json:"time"`
		Max          []*float64 `json:"temperature_2m_max"`
		Min          []*float64 `json:"temperature_2m_min"`
		PrecipChance []*int     `json:"precipitation_probability_max"`
		PrecipSum    []*float64 `json:"precipitation_sum"`
		Code         []*int     `json:"weather_code"`
		WindMax      []*float64 `json:"wind_speed_10m_max"`
	} `json:"daily"`
}

func dailyQuery(lat, lon float64, start, end time.Time, fields []string) url.Values {
	query := url.Values{}
	query.Set("latitude", formatCoord(lat))
	query.Set("longitude", formatCoord(lon))
	query.Set("timezone", "auto")
	query.Set("start_date", start.Format(time.DateOnly))
	query.Set("end_date", end.Format(time.DateOnly))
	query.Set("daily", strings.Join(fields, ","))
	return query
}

// forecastRange -- прогноз на конкретные даты.
func forecastRange(ctx context.Context, client *http.Client, lat, lon float64, start, end time.Time) (string, []TripDay, error) {
	var body dailyBody
	query := dailyQuery(lat, lon, start, end, []string{
		"temperature_2m_max", "temperature_2m_min", "precipitation_probability_max",
		"precipitation_sum", "weather_code", "wind_speed_10m_max",
	})
	if err := fetch(ctx, client, forecastBase, query, &body); err != nil {
		return "", nil, err
	}

	days := make([]TripDay, 0, len(body.Daily.Time))
	for i, date := range body.Daily.Time {
		day := TripDay{
			Date:         date,
			Min:          value(body.Daily.Min, i),
			Max:          value(body.Daily.Max, i),
			PrecipChance: intValue(body.Daily.PrecipChance, i),
			PrecipSum:    value(body.Daily.PrecipSum, i),
			WindMax:      value(body.Daily.WindMax, i),
			Weather:      weatherText(intValue(body.Daily.Code, i)),
		}
		days = append(days, day)
	}
	if len(days) == 0 {
		return "", nil, errors.New("Open-Meteo не прислал прогноз на эти даты")
	}
	return body.Timezone, days, nil
}

// climateRange -- норма по тем же календарным дням за прошлые годы.
//
// Каждый год -- отдельный запрос к архиву: так проще сопоставить календарные дни,
// и нет риска утащить лишние мегабайты. 29 февраля в невисокосный год пропускается.
func climateRange(ctx context.Context, client *http.Client, lat, lon float64, start, end, today time.Time) (string, []TripDay, error) {
	type acc struct {
		min, max, precip, wind float64
		rainy, years           int
	}
	n := int(end.Sub(start).Hours()/24) + 1
	sums := make([]acc, n)
	timezone := ""

	// архив отстаёт от сегодняшнего дня на несколько дней: если те же даты год
	// назад ещё не легли в архив, начинаем с позапрошлого года
	first := 1
	if !end.AddDate(-1, 0, 0).Before(dayOf(today).AddDate(0, 0, -7)) {
		first = 2
	}
	for back := first; back < first+climateYears; back++ {
		from := start.AddDate(-back, 0, 0)
		to := end.AddDate(-back, 0, 0)
		var body dailyBody
		query := dailyQuery(lat, lon, from, to, []string{
			"temperature_2m_max", "temperature_2m_min", "precipitation_sum", "wind_speed_10m_max",
		})
		if err := fetch(ctx, client, archiveBase, query, &body); err != nil {
			return "", nil, err
		}
		timezone = body.Timezone
		for i, date := range body.Daily.Time {
			day, err := time.Parse(time.DateOnly, date)
			if err != nil || body.Daily.Max == nil || i >= len(body.Daily.Max) || body.Daily.Max[i] == nil {
				continue
			}
			idx := int(day.AddDate(back, 0, 0).Sub(start).Hours() / 24)
			if idx < 0 || idx >= n {
				continue
			}
			s := &sums[idx]
			s.max += value(body.Daily.Max, i)
			s.min += value(body.Daily.Min, i)
			s.wind += value(body.Daily.WindMax, i)
			precip := value(body.Daily.PrecipSum, i)
			s.precip += precip
			if precip >= rainyDayMM {
				s.rainy++
			}
			s.years++
		}
	}

	days := make([]TripDay, 0, n)
	for i := range n {
		s := sums[i]
		date := start.AddDate(0, 0, i).Format(time.DateOnly)
		if s.years == 0 {
			return "", nil, fmt.Errorf("в архиве нет данных на %s", date)
		}
		years := float64(s.years)
		day := TripDay{
			Date:         date,
			Min:          round1(s.min / years),
			Max:          round1(s.max / years),
			PrecipSum:    round1(s.precip / years),
			WindMax:      round1(s.wind / years),
			PrecipChance: int(math.Round(float64(s.rainy) / years * 100)),
		}
		day.Weather = climateText(day)
		days = append(days, day)
	}
	return timezone, days, nil
}

// climateText -- норма словами. Кода погоды у нормы нет: пять разных лет
// не складываются в один «переменная облачность».
func climateText(day TripDay) string {
	switch {
	case day.PrecipChance >= 60:
		return "обычно дождливо"
	case day.PrecipChance >= 30:
		return "дожди нередки"
	default:
		return "обычно без осадков"
	}
}

func value(list []*float64, i int) float64 {
	if i < len(list) && list[i] != nil {
		return *list[i]
	}
	return 0
}

func intValue(list []*int, i int) int {
	if i < len(list) && list[i] != nil {
		return *list[i]
	}
	return 0
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// renderTripWeather -- погода поездки словами.
func renderTripWeather(out TripWeatherOutput) string {
	var b strings.Builder
	kind := "Прогноз"
	if out.Source == SourceClimate {
		kind = "Климатическая норма (не прогноз)"
	}
	fmt.Fprintf(&b, "%s на %s — %s, точка %.4f, %.4f, часовой пояс %s\n%s\n\n",
		kind, out.StartDate, out.EndDate, out.Latitude, out.Longitude, out.Timezone, out.Note)
	for _, day := range out.Days {
		fmt.Fprintf(&b, "%s: %s, от %s до %s, осадки %d%%, %.1f мм, ветер до %.0f км/ч\n",
			day.Date, day.Weather, celsius(day.Min), celsius(day.Max), day.PrecipChance, day.PrecipSum, day.WindMax)
	}
	return b.String()
}
