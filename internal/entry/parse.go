package entry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	SourceExplicit = "explicit"
	SourceRelative = "relative"
	SourceUnknown  = "unknown"
	SourceUser     = "user" // дату уточнил пользователь
)

// Segment — порция информации об одном дне из одного голосового.
type Segment struct {
	Date       string         // "2006-01-02" или "" (дата неизвестна)
	DateSource string         // explicit | relative | unknown
	Values     map[string]any // text→string, list→[]string, number→float64, bool→bool; только упомянутое
}

// ParseExtraction разбирает ответ модели. Модель иногда оборачивает JSON в ```-блок или
// добавляет пояснения, поэтому берём подстроку от первой «{» до последней «}».
// Значения разбираются только для активных категорий схемы: остальные ключи отбрасываются.
func ParseExtraction(raw string, schema Schema) ([]Segment, error) {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, errors.New("в ответе нет JSON-объекта")
	}
	var doc struct {
		Segments []map[string]any `json:"segments"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &doc); err != nil {
		return nil, fmt.Errorf("невалидный JSON: %w", err)
	}

	var segs []Segment
	for _, m := range doc.Segments {
		seg := Segment{Values: map[string]any{}}
		if s, ok := m["date"].(string); ok {
			seg.Date = strings.TrimSpace(s)
		}
		if _, err := time.Parse("2006-01-02", seg.Date); err != nil {
			seg.Date = ""
		}
		src, _ := m["date_source"].(string)
		switch {
		case seg.Date == "":
			seg.DateSource = SourceUnknown
		case src == SourceRelative:
			seg.DateSource = SourceRelative
		default:
			seg.DateSource = SourceExplicit
		}

		for _, c := range schema.Active() {
			if v, ok := normalize(c.Kind, m[c.Key]); ok {
				seg.Values[c.Key] = v
			}
		}
		if len(seg.Values) > 0 {
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		return nil, errors.New("в ответе нет сегментов с данными")
	}
	return segs, nil
}

// ResolvedDate возвращает дату сегмента, если ей можно доверять: она названа, не позже
// сегодняшнего дня дневника today и не старше двух лет. Иначе дату надо уточнить у пользователя.
func (s Segment) ResolvedDate(today time.Time) (time.Time, bool) {
	if s.Date == "" || s.DateSource == SourceUnknown {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", s.Date)
	if err != nil || d.After(today) || d.Before(today.AddDate(-2, 0, 0)) {
		return time.Time{}, false
	}
	return d, true
}

// Значения-заглушки, которые модель иногда пишет вместо null.
var placeholders = map[string]bool{
	"не упоминалось": true, "не упоминается": true, "неизвестно": true, "нет данных": true,
	"нет информации": true, "не указано": true, "null": true, "none": true, "-": true, "—": true,
}

func cleanString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || placeholders[strings.ToLower(strings.TrimRight(s, ". "))] {
		return "", false
	}
	return s, true
}

func normalize(k Kind, v any) (any, bool) {
	switch k {
	case Text:
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		return cleanString(s)
	case List:
		var items []string
		seen := map[string]bool{}
		add := func(s string) {
			if s, ok := cleanString(s); ok && !seen[s] {
				seen[s] = true
				items = append(items, s)
			}
		}
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok {
					add(s)
				}
			}
		case string:
			add(x)
		}
		return items, len(items) > 0
	case Number:
		switch x := v.(type) {
		case float64:
			return x, x >= 0
		case string:
			f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(x), ",", "."), 64)
			return f, err == nil && f >= 0
		}
	case Bool:
		switch x := v.(type) {
		case bool:
			return x, true
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "да":
				return true, true
			case "false", "нет":
				return false, true
			}
		}
	}
	return nil, false
}
