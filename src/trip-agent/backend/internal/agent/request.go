package agent

// Запрос на план поездки: форма из интерфейса, её проверка и умолчания.
//
// Пользователь не пишет свободный текст -- он заполняет форму. Единственное
// текстовое поле -- город, и оно проверяется строго. Всё остальное -- даты,
// числа и значения из фиксированных списков. Поэтому то, что уходит модели,
// собрано кодом из проверенных полей, а не вставлено как есть.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Form -- поля формы «Новый план».
type Form struct {
	City      string   `json:"city"`
	StartDate string   `json:"startDate,omitempty"`
	EndDate   string   `json:"endDate,omitempty"`
	Travelers int      `json:"travelers,omitempty"`
	Budget    float64  `json:"budget,omitempty"`
	Currency  string   `json:"currency,omitempty"`
	Interests []string `json:"interests,omitempty"`
	Pace      string   `json:"pace,omitempty"`
}

// Request -- проверенный запрос с подставленными умолчаниями.
type Request struct {
	Form
	Start       time.Time `json:"-"`
	End         time.Time `json:"-"`
	Days        int       `json:"days"`
	HasBudget   bool      `json:"hasBudget"`
	Assumptions []string  `json:"assumptions"`
}

// Допустимые значения.
var (
	Interests = map[string]string{
		"museums": "музеи", "architecture": "архитектура", "nature": "природа и парки",
		"food": "еда", "nightlife": "ночная жизнь", "kids": "с детьми",
	}
	Paces      = map[string]string{"calm": "спокойный: 2–3 места в день", "busy": "насыщенный: 4–5 мест в день"}
	Currencies = map[string]bool{"RUB": true, "USD": true, "EUR": true}
)

// Границы.
const (
	MaxTripDays    = 14
	MaxTravelers   = 10
	DefaultLead    = 7
	DefaultTripLen = 3
)

// ErrInvalid -- форма заполнена неверно.
var ErrInvalid = errors.New("форма заполнена неверно")

// cityPattern -- буквы любых алфавитов, пробел, дефис, точка, апостроф.
// Ни цифр, ни скобок, ни двоеточий: в названии города им не место,
// а в попытке инъекции -- самое место.
var cityPattern = regexp.MustCompile(`^\p{L}[\p{L} .'’-]{0,79}$`)

// Prepare проверяет форму и подставляет умолчания; всё додуманное -- в Assumptions.
func Prepare(f Form, today time.Time) (Request, error) {
	r := Request{Form: f, Assumptions: []string{}}
	r.City = strings.Join(strings.Fields(f.City), " ")
	if !cityPattern.MatchString(r.City) {
		return Request{}, fmt.Errorf("%w: город -- только буквы, пробел, дефис, точка или апостроф, до 80 символов", ErrInvalid)
	}

	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	start, end := strings.TrimSpace(f.StartDate), strings.TrimSpace(f.EndDate)
	switch {
	case start == "" && end == "":
		r.Start = day.AddDate(0, 0, DefaultLead)
		r.End = r.Start.AddDate(0, 0, DefaultTripLen-1)
		r.Assumptions = append(r.Assumptions, fmt.Sprintf("Даты не указаны -- план на %d дня через неделю.", DefaultTripLen))
	case start == "":
		return Request{}, fmt.Errorf("%w: указан конец поездки без начала", ErrInvalid)
	default:
		var err error
		if r.Start, err = time.Parse(time.DateOnly, start); err != nil {
			return Request{}, fmt.Errorf("%w: дата начала не в формате ГГГГ-ММ-ДД", ErrInvalid)
		}
		if end == "" {
			r.End = r.Start.AddDate(0, 0, DefaultTripLen-1)
			r.Assumptions = append(r.Assumptions, fmt.Sprintf("Дата окончания не указана -- поездка на %d дня.", DefaultTripLen))
		} else if r.End, err = time.Parse(time.DateOnly, end); err != nil {
			return Request{}, fmt.Errorf("%w: дата окончания не в формате ГГГГ-ММ-ДД", ErrInvalid)
		}
	}
	if r.Start.Before(day) {
		return Request{}, fmt.Errorf("%w: поездка начинается в прошлом", ErrInvalid)
	}
	if r.End.Before(r.Start) {
		return Request{}, fmt.Errorf("%w: поездка кончается раньше, чем начинается", ErrInvalid)
	}
	r.Days = int(r.End.Sub(r.Start).Hours()/24) + 1
	if r.Days > MaxTripDays {
		return Request{}, fmt.Errorf("%w: поездка не длиннее %d дней", ErrInvalid, MaxTripDays)
	}
	r.StartDate, r.EndDate = r.Start.Format(time.DateOnly), r.End.Format(time.DateOnly)

	switch {
	case f.Travelers == 0:
		r.Travelers = 1
		r.Assumptions = append(r.Assumptions, "Число путешественников не указано -- план на одного.")
	case f.Travelers < 1 || f.Travelers > MaxTravelers:
		return Request{}, fmt.Errorf("%w: путешественников от 1 до %d", ErrInvalid, MaxTravelers)
	}

	switch {
	case f.Budget < 0 || f.Budget > 1e10:
		return Request{}, fmt.Errorf("%w: бюджет должен быть положительным", ErrInvalid)
	case f.Budget > 0:
		r.HasBudget = true
		r.Currency = strings.ToUpper(strings.TrimSpace(f.Currency))
		if r.Currency == "" {
			r.Currency = "RUB"
		}
		if !Currencies[r.Currency] {
			return Request{}, fmt.Errorf("%w: валюта бюджета -- RUB, USD или EUR", ErrInvalid)
		}
	default:
		r.Currency = ""
	}

	seen := map[string]bool{}
	r.Interests = []string{}
	for _, i := range f.Interests {
		if _, ok := Interests[i]; !ok {
			return Request{}, fmt.Errorf("%w: неизвестный интерес %q", ErrInvalid, i)
		}
		if !seen[i] {
			seen[i] = true
			r.Interests = append(r.Interests, i)
		}
	}
	if r.Pace == "" {
		r.Pace = "calm"
	}
	if _, ok := Paces[r.Pace]; !ok {
		return Request{}, fmt.Errorf("%w: темп -- calm или busy", ErrInvalid)
	}
	return r, nil
}

// Prompt -- запрос для модели. Собран из проверенных полей.
func (r Request) Prompt() string {
	var b strings.Builder
	b.WriteString("<trip_request>\n")
	fmt.Fprintf(&b, "Город: %s\n", r.City)
	fmt.Fprintf(&b, "Даты: %s — %s (%d дн.)\n", r.StartDate, r.EndDate, r.Days)
	fmt.Fprintf(&b, "Путешественников: %d\n", r.Travelers)
	if r.HasBudget {
		fmt.Fprintf(&b, "Бюджет: %.0f %s на всю поездку и всех путешественников\n", r.Budget, r.Currency)
	} else {
		b.WriteString("Бюджет: не указан -- инструменты денег для бюджета не вызывай\n")
	}
	if len(r.Interests) == 0 {
		b.WriteString("Интересы: не указаны -- общий план\n")
	} else {
		names := make([]string, 0, len(r.Interests))
		for _, i := range r.Interests {
			names = append(names, Interests[i])
		}
		fmt.Fprintf(&b, "Интересы: %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(&b, "Темп: %s\n", Paces[r.Pace])
	if len(r.Assumptions) > 0 {
		fmt.Fprintf(&b, "Допущения (запиши их в notes при trip_create): %s\n", strings.Join(r.Assumptions, " "))
	}
	b.WriteString("</trip_request>")
	return b.String()
}
