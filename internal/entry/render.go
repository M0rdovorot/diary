package entry

import (
	"fmt"
	"strings"
	"time"
)

const notMentioned = "не упоминалось"

// Render — текст сообщения для Telegram по одному сегменту (обычный текст, без markdown).
// date == nil — дата ещё не определена.
func Render(seg Segment, date *time.Time, schema Schema) string {
	return RenderCard(CardFromSegment(seg, date), schema)
}

// RenderCard печатает карточку дня (см. View) текстом для Telegram. Порции не из основной
// записи помечаются источником, у чисел и да/нет при расхождении показывается «ранее».
func RenderCard(c Card, schema Schema) string {
	v := c.View(schema)
	var b strings.Builder
	if c.Date.IsZero() {
		b.WriteString("Дата: не определена")
	} else {
		fmt.Fprintf(&b, "Дата: %s", c.Date.Format("02.01.2006"))
	}
	if len(v.Voices) > 1 {
		b.WriteString("\nЗаписи: " + voicesLine(v.Voices))
	}
	if v.Unconfirmed {
		b.WriteString("\n⚠ Есть неподтверждённые расшифровки — желательно проверить: /pending")
	}
	b.WriteString("\n\n")

	for _, s := range v.Sections {
		fmt.Fprintf(&b, "%s\n%s\n\n", s.Title, sectionText(s))
	}
	return strings.TrimSpace(b.String())
}

func voicesLine(voices []VoiceView) string {
	parts := make([]string, len(voices))
	for i, v := range voices {
		parts[i] = v.Label
		if !v.Confirmed {
			parts[i] += " ⚠"
		}
	}
	return strings.Join(parts, "; ")
}

// sectionText печатает содержимое раздела. Группа, в которой ничего не упомянуто, — одна
// строка; иначе строки по каждой категории: «— Название: значение».
func sectionText(s SectionView) string {
	if !s.Group {
		return itemText(s.Items[0])
	}
	if !s.Mentioned {
		return notMentioned
	}
	var lines []string
	for _, it := range s.Items {
		lines = append(lines, groupLines(it)...)
	}
	return strings.Join(lines, "\n")
}

// itemText — содержимое одиночной категории (раздел без группы).
func itemText(it ItemView) string {
	if !it.Mentioned() {
		return notMentioned
	}
	switch it.Kind {
	case "diary":
		out := make([]string, len(it.Blocks))
		for i, b := range it.Blocks {
			out[i] = joinNonEmpty([]string{headerText(b.Header), strings.Join(b.Texts, "\n")}, "\n")
		}
		return strings.Join(out, "\n\n")
	case "text":
		return strings.Join(valueLines(it.Values, ""), "\n")
	case "list":
		return strings.Join(valueLines(it.Values, "— "), "\n")
	}
	return scalarText(it)
}

func groupLines(it ItemView) []string {
	if !it.Mentioned() {
		return []string{"— " + it.Title + ": " + notMentioned}
	}
	switch it.Kind {
	case "text":
		return valueLines(it.Values, "— "+it.Title+": ")
	case "diary":
		return []string{"— " + it.Title + ": " + itemText(it)}
	case "list":
		return []string{it.Title + ":\n" + itemText(it)}
	}
	return []string{"— " + it.Title + ": " + scalarText(it)}
}

func headerText(h string) string {
	if h == "" {
		return ""
	}
	return "▸ " + h
}

func valueLines(vals []ValueView, prefix string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = prefix + valueText(v)
	}
	return out
}

func valueText(v ValueView) string {
	if v.Mark == "" {
		return v.Text
	}
	return v.Text + " (" + v.Mark + ")"
}

// scalarText — самое свежее значение и, если прежние отличались, «ранее: …».
func scalarText(it ItemView) string {
	out := valueText(it.Values[0])
	if len(it.Earlier) > 0 {
		earlier := make([]string, len(it.Earlier))
		for i, e := range it.Earlier {
			earlier[i] = valueText(e)
		}
		out += "; ранее: " + strings.Join(earlier, ", ")
	}
	return out
}

// FormatNumber печатает 7.5 как «7.5», а 1500 — как «1500».
func FormatNumber(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return strings.TrimRight(fmt.Sprintf("%.2f", f), "0")
}
