package main

// Инструменты сервера мест.
//
// Описания говорят модели не только что делает инструмент, но и откуда брать
// аргументы: координаты для sights -- из find_city, page_id для sight_info --
// из sights. Так цепочка между серверами складывается из подсказок, а не из
// жёстко зашитого порядка.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FindCityInput -- аргументы find_city.
type FindCityInput struct {
	Name        string `json:"name" jsonschema:"название города, например «Стамбул» или «Tbilisi»"`
	CountryCode string `json:"country_code,omitempty" jsonschema:"код страны из двух букв, если название неоднозначно"`
}

// FindCityOutput -- результат find_city.
type FindCityOutput struct {
	City         City   `json:"city" jsonschema:"найденный город -- самый крупный из подходящих"`
	Alternatives []City `json:"alternatives" jsonschema:"другие места с тем же названием"`
}

// SightsInput -- аргументы sights.
type SightsInput struct {
	Latitude  float64 `json:"latitude" jsonschema:"широта центра поиска -- из find_city"`
	Longitude float64 `json:"longitude" jsonschema:"долгота центра поиска -- из find_city"`
	RadiusKM  float64 `json:"radius_km,omitempty" jsonschema:"радиус поиска в км, от 1 до 10; по умолчанию 5"`
	Limit     int     `json:"limit,omitempty" jsonschema:"сколько мест вернуть, от 1 до 20; по умолчанию 12"`
}

// SightsOutput -- результат sights.
type SightsOutput struct {
	Sights []Sight `json:"sights" jsonschema:"места рядом, ближайшие первыми"`
}

// SightInfoInput -- аргументы sight_info.
type SightInfoInput struct {
	PageID int    `json:"page_id" jsonschema:"id статьи из результата sights"`
	Lang   string `json:"lang,omitempty" jsonschema:"язык статьи из результата sights: ru или en; по умолчанию ru"`
}

// newServer собирает MCP-сервер.
func newServer(p *Places) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-places", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "find_city",
		Description: "Найти город: координаты, страна и её код ISO, часовой пояс. С него начинается любой план поездки: " +
			"координаты нужны погоде и поиску мест, код страны -- серверу денег для валюты.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FindCityInput) (*mcp.CallToolResult, FindCityOutput, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" || len([]rune(name)) > 100 {
			return nil, FindCityOutput{}, errors.New("название города пустое или длиннее 100 символов")
		}
		cities, err := p.findCity(ctx, name, strings.TrimSpace(in.CountryCode))
		if err != nil {
			return nil, FindCityOutput{}, err
		}
		out := FindCityOutput{City: cities[0], Alternatives: cities[1:]}
		c := out.City
		where := c.Name + ", " + c.Country
		if c.Region != "" && c.Region != c.Name {
			where = c.Name + ", " + c.Region + ", " + c.Country
		}
		text := fmt.Sprintf("%s: координаты %.4f, %.4f, часовой пояс %s, код страны %s.",
			where, c.Latitude, c.Longitude, c.Timezone, c.CountryCode)
		if len(out.Alternatives) > 0 {
			alt := make([]string, 0, len(out.Alternatives))
			for _, a := range out.Alternatives {
				alt = append(alt, a.Name+", "+a.Country)
			}
			text += " Есть и другие места с таким названием: " + strings.Join(alt, "; ") + "."
		}
		return textResult(text), out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "sights",
		Description: "Достопримечательности и заметные места рядом с точкой -- статьи Википедии с координатами и " +
			"коротким описанием. Координаты бери из find_city. Выбери из списка то, что подходит к интересам путешественника.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SightsInput) (*mcp.CallToolResult, SightsOutput, error) {
		if err := checkCoords(in.Latitude, in.Longitude); err != nil {
			return nil, SightsOutput{}, err
		}
		radius := int(clampF(in.RadiusKM, 5, 1, 10) * 1000)
		limit := clampI(in.Limit, 12, 1, 20)

		list, err := p.sights(ctx, "ru", in.Latitude, in.Longitude, radius, limit)
		if err != nil {
			return nil, SightsOutput{}, err
		}
		// о небольших городах в русской Википедии бывает пусто -- тогда берём английскую.
		// Смешивать языки при непустой русской выдаче нельзя: одно место придёт дважды
		if len(list) == 0 {
			if extra, err := p.sights(ctx, "en", in.Latitude, in.Longitude, radius, limit); err == nil {
				list = append(list, extra...)
			}
		}
		if len(list) > limit {
			list = list[:limit]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Мест рядом: %d.\n", len(list))
		for _, s := range list {
			fmt.Fprintf(&b, "- %s [page_id=%d, lang=%s, %d м]", s.Title, s.PageID, s.Lang, s.DistanceM)
			if s.Description != "" {
				fmt.Fprintf(&b, ": %s", s.Description)
			}
			b.WriteString("\n")
		}
		return textResult(b.String()), SightsOutput{Sights: list}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sight_info",
		Description: "Описание места из Википедии и ссылка на статью. page_id и lang бери из результата sights.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SightInfoInput) (*mcp.CallToolResult, SightInfo, error) {
		lang := strings.ToLower(strings.TrimSpace(in.Lang))
		if lang == "" {
			lang = "ru"
		}
		if lang != "ru" && lang != "en" {
			return nil, SightInfo{}, fmt.Errorf("lang может быть ru или en, а не %q", in.Lang)
		}
		if in.PageID <= 0 {
			return nil, SightInfo{}, errors.New("page_id должен быть положительным числом из результата sights")
		}
		info, err := p.sightInfo(ctx, lang, in.PageID)
		if err != nil {
			return nil, SightInfo{}, err
		}
		return textResult(fmt.Sprintf("%s (%s)\n%s", info.Title, info.URL, info.Extract)), info, nil
	})

	return server
}

func checkCoords(lat, lon float64) error {
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return fmt.Errorf("координаты %v, %v не похожи на настоящие -- возьми их из find_city", lat, lon)
	}
	return nil
}

// distanceM -- расстояние между точками по дуге большого круга, в метрах.
func distanceM(lat1, lon1, lat2, lon2 float64) int {
	const earth = 6371000.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return int(math.Round(2 * earth * math.Asin(math.Sqrt(a))))
}

func clampI(v, fallback, low, high int) int {
	if v == 0 {
		return fallback
	}
	return max(low, min(high, v))
}

func clampF(v, fallback, low, high float64) float64 {
	if v == 0 {
		return fallback
	}
	return math.Max(low, math.Min(high, v))
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
