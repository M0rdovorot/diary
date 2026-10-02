package entry

import (
	"encoding/json"
	"time"
)

// Source — откуда взята порция информации.
type Source struct {
	VoiceID   int64
	SentAt    time.Time
	Logical   time.Time // день, к которому отнесено голосовое
	Relation  string    // same_day | retro
	Confirmed bool      // расшифровку подтвердил пользователь (текстовые записи подтверждены всегда)
}

type Observation struct {
	Field string
	Value any
	Src   Source
}

type Part struct {
	Value any
	Src   Source
}

// Card — собранная из наблюдений карточка одного дня.
type Card struct {
	Date    time.Time // нулевая — дата не определена
	Loc     *time.Location
	Primary int64 // голосовое основной записи (первое same_day); 0 — основной нет
	Parts   map[string][]Part
	Voices  []Source // различные голосовые в порядке появления
}

// BuildCard сливает наблюдения дня. obs должны идти в хронологическом порядке (по sent_at).
// Само слияние (дополнить текст, объединить списки, взять свежее число) происходит при
// отрисовке: здесь сохраняются все порции вместе с источниками.
func BuildCard(date time.Time, loc *time.Location, obs []Observation) Card {
	c := Card{Date: date, Loc: loc, Parts: map[string][]Part{}}
	seen := map[int64]bool{}
	for _, o := range obs {
		c.Parts[o.Field] = append(c.Parts[o.Field], Part{Value: o.Value, Src: o.Src})
		if !seen[o.Src.VoiceID] {
			seen[o.Src.VoiceID] = true
			c.Voices = append(c.Voices, o.Src)
		}
		if c.Primary == 0 && o.Src.Relation == "same_day" {
			c.Primary = o.Src.VoiceID
		}
	}
	return c
}

// CardFromSegment — карточка из одного сегмента (для ответа на конкретное голосовое; пометок нет).
func CardFromSegment(seg Segment, date *time.Time) Card {
	src := Source{VoiceID: 1, Relation: "same_day", Confirmed: true}
	c := Card{Loc: time.UTC, Primary: 1, Parts: map[string][]Part{}, Voices: []Source{src}}
	if date != nil {
		c.Date = *date
	}
	for key, v := range seg.Values {
		c.Parts[key] = []Part{{Value: v, Src: src}}
	}
	return c
}

// DecodeValue восстанавливает типизированное значение поля из JSON, сохранённого в БД.
func DecodeValue(schema Schema, field string, raw []byte) (any, bool) {
	f, ok := schema.Lookup(field)
	if !ok {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return normalize(f.Kind, v)
}

// DecodeValues разбирает payload сегмента из БД.
func DecodeValues(schema Schema, raw []byte) map[string]any {
	var m map[string]json.RawMessage
	out := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, r := range m {
		if v, ok := DecodeValue(schema, k, r); ok {
			out[k] = v
		}
	}
	return out
}
