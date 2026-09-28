package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var today = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

func clock() time.Time { return today }

// travelAPI -- подставные прогноз и архив. Архив отвечает на каждый год по-своему,
// чтобы усреднение было видно.
func travelAPI(t *testing.T) (*http.Client, *[]url.Values, *[]url.Values) {
	t.Helper()
	var forecasts, archives []url.Values

	fc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forecasts = append(forecasts, r.URL.Query())
		w.Write([]byte(`{"timezone":"Europe/Istanbul","daily":{"time":["2026-09-27","2026-09-28"],
			"temperature_2m_max":[24.5,22.0],"temperature_2m_min":[17.0,16.5],
			"precipitation_probability_max":[10,70],"precipitation_sum":[0,6.4],
			"weather_code":[1,63],"wind_speed_10m_max":[12,20]}}`))
	}))
	t.Cleanup(fc.Close)

	ar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		archives = append(archives, r.URL.Query())
		start, _ := time.Parse(time.DateOnly, r.URL.Query().Get("start_date"))
		// год 2025 -- дождь во второй день, остальные годы сухие; тёплые и прохладные годы чередуются
		rain := "0"
		if start.Year() == 2025 {
			rain = "5"
		}
		max := "20"
		if start.Year()%2 == 0 {
			max = "24"
		}
		w.Write([]byte(`{"timezone":"Europe/Istanbul","daily":{"time":["` +
			start.Format(time.DateOnly) + `","` + start.AddDate(0, 0, 1).Format(time.DateOnly) + `"],
			"temperature_2m_max":[` + max + `,` + max + `],"temperature_2m_min":[15,15],
			"precipitation_sum":[0,` + rain + `],"wind_speed_10m_max":[10,10]}}`))
	}))
	t.Cleanup(ar.Close)

	prevF, prevA := forecastBase, archiveBase
	forecastBase, archiveBase = fc.URL, ar.URL
	t.Cleanup(func() { forecastBase, archiveBase = prevF, prevA })
	return fc.Client(), &forecasts, &archives
}

func TestTripWeatherForecast(t *testing.T) {
	client, forecasts, archives := travelAPI(t)
	out, err := tripWeather(context.Background(), client, TripWeatherInput{
		Latitude: 41.01, Longitude: 28.98, StartDate: "2026-09-27", EndDate: "2026-09-28",
	}, today)
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != SourceForecast || len(*archives) != 0 {
		t.Fatalf("ближние даты должны идти в прогноз: %s, архив %d", out.Source, len(*archives))
	}
	q := (*forecasts)[0]
	if q.Get("start_date") != "2026-09-27" || q.Get("end_date") != "2026-09-28" || q.Get("latitude") != "41.01" {
		t.Errorf("запрос прогноза: %v", q)
	}
	if len(out.Days) != 2 || out.Days[1].Weather != "дождь" || out.Days[1].PrecipChance != 70 || out.Timezone != "Europe/Istanbul" {
		t.Errorf("дни: %+v", out.Days)
	}
}

func TestTripWeatherClimate(t *testing.T) {
	client, forecasts, archives := travelAPI(t)
	out, err := tripWeather(context.Background(), client, TripWeatherInput{
		Latitude: 41.01, Longitude: 28.98, StartDate: "2026-11-10", EndDate: "2026-11-11",
	}, today)
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != SourceClimate || len(*forecasts) != 0 || !strings.Contains(out.Note, "норма") {
		t.Fatalf("дальние даты -- норма: %+v", out)
	}
	years := []int{}
	for _, q := range *archives {
		d, _ := time.Parse(time.DateOnly, q.Get("start_date"))
		years = append(years, d.Year())
		if q.Get("start_date")[4:] != "-11-10" || q.Get("end_date")[4:] != "-11-11" {
			t.Errorf("не те календарные дни: %v", q)
		}
	}
	sort.Ints(years)
	if len(years) != climateYears || years[0] != 2021 || years[4] != 2025 {
		t.Errorf("годы архива: %v", years)
	}
	// 2022 и 2024 по 24°, 2021/2023/2025 по 20° -> 21.6
	if out.Days[0].Max != 21.6 || out.Days[0].Min != 15 {
		t.Errorf("средняя: %+v", out.Days[0])
	}
	// дождь только в 2025 во второй день: 1 год из 5
	if out.Days[1].PrecipChance != 20 || out.Days[1].PrecipSum != 1 || out.Days[0].PrecipChance != 0 {
		t.Errorf("осадки: %+v", out.Days)
	}
}

// Даты, у которых год назад ещё нет архива, берутся со сдвигом на год.
func TestTripWeatherClimateArchiveLag(t *testing.T) {
	client, _, archives := travelAPI(t)
	soon := today.AddDate(0, 0, 20).Format(time.DateOnly)
	if _, err := tripWeather(context.Background(), client, TripWeatherInput{Latitude: 1, Longitude: 1, StartDate: soon, EndDate: soon}, today); err != nil {
		t.Fatal(err)
	}
	for _, q := range *archives {
		end, _ := time.Parse(time.DateOnly, q.Get("end_date"))
		if !end.Before(today.AddDate(0, 0, -7)) {
			t.Errorf("запрошены даты, которых ещё нет в архиве: %v", q)
		}
	}
}

func TestTripWeatherBadInput(t *testing.T) {
	client, _, _ := travelAPI(t)
	cases := map[string]TripWeatherInput{
		"не дата":         {StartDate: "10.10.2026", EndDate: "2026-10-12"},
		"конец до начала": {StartDate: "2026-10-12", EndDate: "2026-10-10"},
		"слишком долго":   {StartDate: "2026-10-01", EndDate: "2026-10-30"},
		"в прошлом":       {StartDate: "2026-09-01", EndDate: "2026-09-03"},
		"вне Земли":       {Latitude: 120, StartDate: "2026-10-01", EndDate: "2026-10-02"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tripWeather(context.Background(), client, in, today); !errors.Is(err, ErrBadTrip) {
				t.Errorf("ждали ErrBadTrip, получили %v", err)
			}
		})
	}
}

// listOver -- tools/list по настоящему HTTP, как его видит внешний клиент.
func listOver(t *testing.T, endpoint string) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return list.Tools
}

// Совместимость: на /mcp ровно прежние инструменты -- от этого набора зависит
// помощник строителя. trip_weather есть только на /mcp/travel.
func TestEndpointsCompatibility(t *testing.T) {
	server := httptest.NewServer(newMux(http.DefaultClient, clock))
	defer server.Close()

	legacy := listOver(t, server.URL+"/mcp")
	travel := listOver(t, server.URL+"/mcp/travel")

	toolNames := func(tools []*mcp.Tool) string {
		n := []string{}
		for _, tool := range tools {
			n = append(n, tool.Name)
		}
		sort.Strings(n)
		return strings.Join(n, ",")
	}
	if got := toolNames(legacy); got != "find_place,get_forecast" {
		t.Errorf("/mcp изменился: %s", got)
	}
	if got := toolNames(travel); got != "find_place,get_forecast,trip_weather" {
		t.Errorf("/mcp/travel: %s", got)
	}

	// схемы старых инструментов на обоих адресах одинаковые
	schemas := map[string]string{}
	for _, tool := range legacy {
		raw, _ := tool.InputSchema.(map[string]any)
		schemas[tool.Name] = strings.Join(names(raw["required"]), ",")
	}
	for _, tool := range travel {
		if want, ok := schemas[tool.Name]; ok {
			raw, _ := tool.InputSchema.(map[string]any)
			if got := strings.Join(names(raw["required"]), ","); got != want {
				t.Errorf("схема %s на /mcp/travel разошлась: %s против %s", tool.Name, got, want)
			}
		}
	}

	resp, err := http.Get(server.URL + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("/health: %v %v", resp, err)
	}
}

func TestTripWeatherTool(t *testing.T) {
	client, _, _ := travelAPI(t)
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	go newTravelServer(client, clock).Run(ctx, st)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result := call(t, session, "trip_weather", map[string]any{
		"latitude": 41.01, "longitude": 28.98, "start_date": "2026-11-10", "end_date": "2026-11-11",
	})
	if result.IsError || !strings.Contains(text(t, result), "Климатическая норма") {
		t.Errorf("ответ: %s", text(t, result))
	}
	bad := call(t, session, "trip_weather", map[string]any{
		"latitude": 41.01, "longitude": 28.98, "start_date": "2026-09-01", "end_date": "2026-09-02",
	})
	if !bad.IsError {
		t.Error("поездка в прошлом принята")
	}
}
