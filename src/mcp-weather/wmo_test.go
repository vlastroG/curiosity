package main

import (
	"strings"
	"testing"
)

func TestWeatherText(t *testing.T) {
	cases := []struct {
		name string
		code int
		want string
	}{
		{"ясно", 0, "ясно"},
		{"пасмурно", 3, "пасмурно"},
		{"дождь", 61, "слабый дождь"},
		{"снег", 75, "сильный снег"},
		{"гроза", 95, "гроза"},
		{"неизвестный код не проглатывается", 88, "код погоды 88"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := weatherText(tc.code); got != tc.want {
				t.Fatalf("weatherText(%d) = %q, ожидалось %q", tc.code, got, tc.want)
			}
		})
	}
}

func TestWorkHint(t *testing.T) {
	cases := []struct {
		name       string
		conditions Conditions
		want       string // подстрока, которая обязана встретиться
	}{
		{
			name:       "обычные условия",
			conditions: Conditions{Temperature: 18, Humidity: 55, WindSpeed: 10, Code: 1},
			want:       "условия для наружных работ обычные",
		},
		{
			name:       "мороз",
			conditions: Conditions{Temperature: -4, Humidity: 60, Code: 0},
			want:       "мороз",
		},
		{
			name:       "холодно, но не мороз",
			conditions: Conditions{Temperature: 3, Humidity: 60, Code: 0},
			want:       "холодно",
		},
		{
			name:       "жара",
			conditions: Conditions{Temperature: 34, Humidity: 30, Code: 0},
			want:       "жарко",
		},
		{
			name:       "дождь по коду, даже без накопленных осадков",
			conditions: Conditions{Temperature: 15, Humidity: 70, Code: 63},
			want:       "идут осадки",
		},
		{
			name:       "дождь по количеству, даже при ясном коде",
			conditions: Conditions{Temperature: 15, Humidity: 70, Precipitation: 0.3, Code: 0},
			want:       "идут осадки",
		},
		{
			name:       "снежная крупа осадками не считается",
			conditions: Conditions{Temperature: 15, Humidity: 50, Code: 77},
			want:       "условия для наружных работ обычные",
		},
		{
			name:       "влажность",
			conditions: Conditions{Temperature: 20, Humidity: 92, Code: 1},
			want:       "высокая влажность (92%)",
		},
		{
			name:       "ветер",
			conditions: Conditions{Temperature: 20, Humidity: 50, WindSpeed: 45, Code: 1},
			want:       "сильный ветер (45 км/ч)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workHint(tc.conditions)
			if got == "" {
				t.Fatal("подсказка пустая: молчание читается как «данных нет»")
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("workHint = %q, ожидалась подстрока %q", got, tc.want)
			}
		})
	}
}

// Границы порогов проверяем отдельно: именно на них проще всего ошибиться
// на единицу и получить «холодно» при +5 или «обычные условия» при +4.
func TestWorkHintBoundaries(t *testing.T) {
	cases := []struct {
		name string
		temp float64
		cold bool
	}{
		{"ровно на границе холода работать уже можно", coldLimit, false},
		{"чуть ниже границы -- уже холодно", coldLimit - 0.1, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workHint(Conditions{Temperature: tc.temp, Humidity: 50, Code: 0})
			if strings.Contains(got, "холодно") != tc.cold {
				t.Fatalf("при %.1f °C получили %q", tc.temp, got)
			}
		})
	}
}
