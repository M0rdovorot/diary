package entry

import (
	"fmt"
	"strings"
	"time"
)

const notMentioned = "не упоминалось"

// Render — текст сообщения для Telegram по одному сегменту (обычный текст, без markdown).
// date == nil — дата ещё не определена.
func Render(seg Segment, date *time.Time) string {
	return RenderCard(CardFromSegment(seg, date))
}

// RenderCard печатает карточку дня. Правила слияния:
// текст — дополняется (все порции), списки — объединяются, числа и да/нет — побеждает самое
// свежее, при расхождении показывается «ранее». Порции не из основной записи помечаются источником.
func RenderCard(c Card) string {
	var b strings.Builder
	if c.Date.IsZero() {
		b.WriteString("Дата: не определена")
	} else {
		fmt.Fprintf(&b, "Дата: %s", c.Date.Format("02.01.2006"))
	}
	if len(c.Voices) > 1 {
		b.WriteString("\nЗаписи: " + c.voicesLine())
	}
	b.WriteString("\n\n")

	section := func(title, body string) { fmt.Fprintf(&b, "%s\n%s\n\n", title, body) }

	section("Дневниковая запись", c.diary())
	section("Настроение", c.text("mood"))
	section("Питание", c.text("food"))
	section("Стул", c.text("stool"))

	var sleep []string
	if l := c.scalar("sleep_hours", func(v any) string { return FormatNumber(v.(float64)) + " ч" }); l != "" {
		sleep = append(sleep, "— "+l)
	}
	if q := c.textLines("sleep_quality"); len(q) > 0 {
		for _, l := range q {
			sleep = append(sleep, "— "+l)
		}
	}
	if len(sleep) == 0 {
		sleep = []string{notMentioned}
	}
	section("Сон", strings.Join(sleep, "\n"))

	section("Физическое состояние", c.text("physical"))
	section("Работа", c.text("work"))
	section("Катя", c.text("katya"))
	section("Основные занятия", "Успел:\n"+c.list("done")+"\nНе успел:\n"+c.list("not_done"))

	yesNo := func(v any) string {
		if v.(bool) {
			return "да"
		}
		return "нет"
	}
	habit := func(label, key string) string {
		if l := c.scalar(key, yesNo); l != "" {
			return "— " + label + ": " + l
		}
		return "— " + label + ": " + notMentioned
	}
	water := "— Вода и другие жидкости: " + notMentioned
	if l := c.scalar("water_ml", func(v any) string { return FormatNumber(v.(float64)) + " мл" }); l != "" {
		water = "— Вода и другие жидкости: " + l
	}
	section("Привычки", strings.Join([]string{
		water,
		habit("Сладкое", "sweets"),
		habit("Алкоголь", "alcohol"),
		habit("Курение", "smoking"),
		habit("Гонение лысого", "gonenie_lysogo"),
		habit("Занятие любовью", "love"),
		habit("Китайский чай", "tea"),
	}, "\n"))

	section("Мысли на подумать", c.list("to_think"))
	section("Мысли для психолога", c.list("for_psychologist"))

	return strings.TrimSpace(b.String())
}

func (c Card) loc() *time.Location {
	if c.Loc != nil {
		return c.Loc
	}
	return time.UTC
}

// mark — пометка происхождения порции; у основной записи её нет.
func (c Card) mark(s Source) string {
	if s.VoiceID == c.Primary {
		return ""
	}
	if s.Relation == "retro" {
		return " (из записи от " + s.Logical.Format("02.01") + ")"
	}
	return " (доп. запись " + s.SentAt.In(c.loc()).Format("15:04") + ")"
}

func (c Card) voicesLine() string {
	var parts []string
	for _, v := range c.Voices {
		at := v.SentAt.In(c.loc())
		switch {
		case v.VoiceID == c.Primary:
			parts = append(parts, "основная "+at.Format("02.01 15:04"))
		case v.Relation == "retro":
			parts = append(parts, "из записи от "+v.Logical.Format("02.01")+" "+at.Format("15:04"))
		default:
			parts = append(parts, "доп. "+at.Format("15:04"))
		}
	}
	return strings.Join(parts, "; ")
}

// diary печатает дневниковую запись. Если порций несколько, каждая идёт отдельным блоком
// с заголовком, чтобы было видно, где основная запись, а где дополнение:
//
//	▸ Основная запись · 21:40
//	…
//	▸ Дополнение · 23:10
//	…
//	▸ Дополнение из записи от 05.09 · 08:15
//	…
//
// Единственная порция из основной записи печатается без заголовка.
func (c Card) diary() string {
	type block struct {
		src   Source
		texts []string
	}
	var blocks []block
	for _, p := range c.Parts["diary"] {
		t, _ := p.Value.(string)
		if t == "" {
			continue
		}
		// подряд идущие порции одного голосового — один блок
		if n := len(blocks); n > 0 && blocks[n-1].src.VoiceID == p.Src.VoiceID {
			blocks[n-1].texts = append(blocks[n-1].texts, t)
			continue
		}
		blocks = append(blocks, block{src: p.Src, texts: []string{t}})
	}
	switch {
	case len(blocks) == 0:
		return notMentioned
	case len(blocks) == 1 && blocks[0].src.VoiceID == c.Primary:
		return strings.Join(blocks[0].texts, "\n")
	}

	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, c.blockHeader(b.src)+"\n"+strings.Join(b.texts, "\n"))
	}
	return strings.Join(out, "\n\n")
}

func (c Card) blockHeader(s Source) string {
	at := s.SentAt.In(c.loc())
	clock := at.Format("15:04")
	switch {
	case s.VoiceID == c.Primary:
		// дату показываем, только если запись отправлена не в сам день карточки
		if !c.Date.IsZero() && at.Format("2006-01-02") != c.Date.Format("2006-01-02") {
			clock = at.Format("02.01 15:04")
		}
		return "▸ Основная запись · " + clock
	case s.Relation == "retro":
		return "▸ Дополнение из записи от " + s.Logical.Format("02.01") + " · " + clock
	default:
		return "▸ Дополнение · " + clock
	}
}

func (c Card) textLines(key string) []string {
	var out []string
	for _, p := range c.Parts[key] {
		if s, ok := p.Value.(string); ok {
			out = append(out, s+c.mark(p.Src))
		}
	}
	return out
}

func (c Card) text(key string) string {
	lines := c.textLines(key)
	if len(lines) == 0 {
		return notMentioned
	}
	return strings.Join(lines, "\n")
}

func (c Card) list(key string) string {
	var lines []string
	seen := map[string]bool{}
	for _, p := range c.Parts[key] {
		items, _ := p.Value.([]string)
		for _, it := range items {
			if seen[it] {
				continue
			}
			seen[it] = true
			lines = append(lines, "— "+it+c.mark(p.Src))
		}
	}
	if len(lines) == 0 {
		return notMentioned
	}
	return strings.Join(lines, "\n")
}

// scalar печатает самое свежее значение; если прежние значения отличались — добавляет «ранее».
// Пустая строка — поле не упоминалось.
func (c Card) scalar(key string, format func(any) string) string {
	parts := c.Parts[key]
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	out := format(last.Value) + c.mark(last.Src)

	var earlier []string
	seen := map[string]bool{format(last.Value): true}
	for _, p := range parts[:len(parts)-1] {
		if s := format(p.Value); !seen[s] {
			seen[s] = true
			earlier = append(earlier, s+c.mark(p.Src))
		}
	}
	if len(earlier) > 0 {
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
