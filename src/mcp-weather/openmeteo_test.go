package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Ответы Open-Meteo записаны с настоящего API и урезаны до нужных полей.
const (
	geocodeMoscow = `{"results":[{"id":524901,"name":"Москва","latitude":55.75204,
		"longitude":37.61781,"country_code":"RU","timezone":"Europe/Moscow",
		"population":10381222,"country":"Россия","admin1":"Москва"}]}`

	// ненайденное место -- это тело без ключа results, а не пустой массив
	geocodeNothing = `{"generationtime_ms":0.15}`

	forecastMoscow = `{"latitude":55.75,"longitude":37.625,"timezone":"Europe/Moscow",
		"current":{"time":"2026-09-22T17:45","temperature_2m":16.1,"apparent_temperature":16.4,
		"relative_humidity_2m":90,"precipitation":0.30,"weather_code":61,"wind_speed_10m":8.2},
		"daily":{"time":["2026-09-22","2026-09-23"],"temperature_2m_max":[16.8,19.1],
		"temperature_2m_min":[12.5,12.0],"precipitation_probability_max":[88,55],
		"precipitation_sum":[5.2,1.1],"weather_code":[95,3],"wind_speed_10m_max":[14.0,11.0]}}`
)

// openMeteo поднимает подставной Open-Meteo и переключает на него базы API.
//
// Возвращает запись последнего запроса к каждому эндпоинту: в части проверок
// важен не ответ, а то, что именно мы спросили.
func openMeteo(t *testing.T, geocodeBody, forecastBody string, status int) (*http.Client, *url.Values, *url.Values) {
	t.Helper()

	var geocodeQuery, forecastQuery url.Values

	geo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geocodeQuery = r.URL.Query()
		w.WriteHeader(status)
		w.Write([]byte(geocodeBody))
	}))
	t.Cleanup(geo.Close)

	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forecastQuery = r.URL.Query()
		w.WriteHeader(status)
		w.Write([]byte(forecastBody))
	}))
	t.Cleanup(fc.Close)

	previousGeocode, previousForecast := geocodeBase, forecastBase
	geocodeBase, forecastBase = geo.URL, fc.URL
	t.Cleanup(func() { geocodeBase, forecastBase = previousGeocode, previousForecast })

	return geo.Client(), &geocodeQuery, &forecastQuery
}

func TestGeocode(t *testing.T) {
	client, query, _ := openMeteo(t, geocodeMoscow, "", http.StatusOK)

	places, err := geocode(context.Background(), client, "Москва", "RU", 3)
	if err != nil {
		t.Fatalf("геокодер: %v", err)
	}
	if len(places) != 1 {
		t.Fatalf("мест %d, ожидалось 1", len(places))
	}

	place := places[0]
	if place.Name != "Москва" || place.Country != "Россия" || place.Region != "Москва" {
		t.Fatalf("место разобралось неверно: %+v", place)
	}
	if place.Latitude != 55.75204 || place.Longitude != 37.61781 {
		t.Fatalf("координаты разобрались неверно: %+v", place)
	}
	if place.Timezone != "Europe/Moscow" {
		t.Fatalf("часовой пояс %q", place.Timezone)
	}
	// одноимённые названия в Title не дублируются
	if got := place.Title(); got != "Москва, Россия" {
		t.Fatalf("Title = %q", got)
	}

	if got := query.Get("count"); got != "3" {
		t.Fatalf("count = %q, ожидалось 3", got)
	}
	if got := query.Get("countryCode"); got != "RU" {
		t.Fatalf("countryCode = %q", got)
	}
	if got := query.Get("language"); got != "ru" {
		t.Fatalf("language = %q: без него названия придут по-английски", got)
	}
}

func TestGeocodePlaceNotFound(t *testing.T) {
	client, _, _ := openMeteo(t, geocodeNothing, "", http.StatusOK)

	_, err := geocode(context.Background(), client, "Тарабарск", "", 1)
	if !errors.Is(err, ErrPlaceNotFound) {
		t.Fatalf("ошибка %v, ожидалась ErrPlaceNotFound", err)
	}
	if !strings.Contains(err.Error(), "Тарабарск") {
		t.Fatalf("в ошибке нет искомого названия: %v", err)
	}
}

func TestGeocodeServerError(t *testing.T) {
	client, _, _ := openMeteo(t, `{"error":true,"reason":"нет"}`, "", http.StatusInternalServerError)

	_, err := geocode(context.Background(), client, "Москва", "", 1)
	if err == nil {
		t.Fatal("ошибки нет, а код ответа 500")
	}
	if errors.Is(err, ErrPlaceNotFound) {
		t.Fatalf("поломка выдана за ненайденное место: %v", err)
	}
}

func TestGeocodeBadJSON(t *testing.T) {
	client, _, _ := openMeteo(t, `не json`, "", http.StatusOK)

	if _, err := geocode(context.Background(), client, "Москва", "", 1); err == nil {
		t.Fatal("ошибки нет, а тело не разбирается")
	}
}

func TestForecast(t *testing.T) {
	client, _, query := openMeteo(t, geocodeMoscow, forecastMoscow, http.StatusOK)

	place := Place{Name: "Москва", Latitude: 55.75204, Longitude: 37.61781}
	current, days, err := forecast(context.Background(), client, place, 2)
	if err != nil {
		t.Fatalf("прогноз: %v", err)
	}

	if current.Temperature != 16.1 || current.Humidity != 90 || current.WindSpeed != 8.2 {
		t.Fatalf("текущая погода разобралась неверно: %+v", current)
	}
	if current.Weather != "слабый дождь" {
		t.Fatalf("код 61 расшифрован как %q", current.Weather)
	}

	if len(days) != 2 {
		t.Fatalf("суток %d, ожидалось 2", len(days))
	}
	if days[0].Max != 16.8 || days[0].PrecipChance != 88 || days[0].PrecipSum != 5.2 {
		t.Fatalf("первые сутки разобрались неверно: %+v", days[0])
	}
	if days[0].Weather != "гроза" {
		t.Fatalf("код 95 расшифрован как %q", days[0].Weather)
	}

	// координаты уезжают без экспоненты, иначе Open-Meteo их не примет
	if got := query.Get("latitude"); got != "55.75204" {
		t.Fatalf("latitude = %q", got)
	}
	if got := query.Get("timezone"); got != "auto" {
		t.Fatalf("timezone = %q: без auto сутки прогноза будут чужими", got)
	}
	if got := query.Get("forecast_days"); got != "2" {
		t.Fatalf("forecast_days = %q", got)
	}
}

// Провайдер вправе прислать параллельные массивы daily разной длины -- например,
// когда часть показателей для этой точки не считается. Идти по самому длинному
// значило бы уронить сервер на индексе.
func TestForecastRaggedDaily(t *testing.T) {
	ragged := `{"current":{"time":"2026-09-22T17:45","temperature_2m":16.1,"weather_code":0},
		"daily":{"time":["2026-09-22","2026-09-23","2026-09-24"],
		"temperature_2m_max":[16.8,19.1],"temperature_2m_min":[12.5,12.0],
		"precipitation_probability_max":[88,55],"precipitation_sum":[5.2,1.1],
		"weather_code":[0,3],"wind_speed_10m_max":[14.0,11.0]}}`

	client, _, _ := openMeteo(t, geocodeMoscow, ragged, http.StatusOK)

	_, days, err := forecast(context.Background(), client, Place{}, 3)
	if err != nil {
		t.Fatalf("прогноз: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("суток %d, ожидалось 2 -- по самому короткому массиву", len(days))
	}
}

func TestForecastServerError(t *testing.T) {
	client, _, _ := openMeteo(t, geocodeMoscow, `{"reason":"нет"}`, http.StatusInternalServerError)

	if _, _, err := forecast(context.Background(), client, Place{}, 3); err == nil {
		t.Fatal("ошибки нет, а код ответа 500")
	}
}

func TestFormatCoord(t *testing.T) {
	cases := map[float64]string{
		55.75204: "55.75204",
		0.0001:   "0.0001", // не 1e-04: Open-Meteo экспоненту не принимает
		-37.5:    "-37.5",
	}
	for value, want := range cases {
		if got := formatCoord(value); got != want {
			t.Fatalf("formatCoord(%v) = %q, ожидалось %q", value, got, want)
		}
	}
}
