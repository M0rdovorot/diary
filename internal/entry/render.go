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

// section — раздел карточки: одна категория или группа категорий.
type section struct {
	title string
	group bool
	cats  []Category
}

// sections раскладывает активные категории по разделам. Категории одной группы попадают в один
// раздел, который стоит там, где встретилась первая из них.
func sections(schema Schema) []section {
	var out []section
	byGroup := map[string]int{}
	for _, c := range schema.Active() {
		if c.Group == "" {
			out = append(out, section{title: c.Title, cats: []Category{c}})
			continue
		}
		if i, ok := byGroup[c.Group]; ok {
			out[i].cats = append(out[i].cats, c)
			continue
		}
		byGroup[c.Group] = len(out)
		out = append(out, section{title: c.Group, group: true, cats: []Category{c}})
	}
	return out
}

// RenderCard печатает карточку дня по активным категориям схемы. Правила слияния:
// текст — дополняется (все порции), списки — объединяются, числа и да/нет — побеждает самое
// свежее, при расхождении показывается «ранее». Порции не из основной записи помечаются источником.
func RenderCard(c Card, schema Schema) string {
	var b strings.Builder
	if c.Date.IsZero() {
		b.WriteString("Дата: не определена")
	} else {
		fmt.Fprintf(&b, "Дата: %s", c.Date.Format("02.01.2006"))
	}
	if len(c.Voices) > 1 {
		b.WriteString("\nЗаписи: " + c.voicesLine())
	}
	if c.hasUnconfirmed() {
		b.WriteString("\n⚠ Есть неподтверждённые расшифровки — желательно проверить: /pending")
	}
	b.WriteString("\n\n")

	for _, s := range sections(schema) {
		body := c.sectionBody(s)
		fmt.Fprintf(&b, "%s\n%s\n\n", s.title, body)
	}
	return strings.TrimSpace(b.String())
}

// sectionBody печатает содержимое раздела.
func (c Card) sectionBody(s section) string {
	if !s.group {
		return c.categoryBody(s.cats[0])
	}

	// группа: если ничего не упомянуто — одна строка, иначе строка на каждую категорию
	mentioned := false
	for _, cat := range s.cats {
		if len(c.Parts[cat.Key]) > 0 {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return notMentioned
	}
	var lines []string
	for _, cat := range s.cats {
		lines = append(lines, c.groupLines(cat)...)
	}
	return strings.Join(lines, "\n")
}

// categoryBody — содержимое одиночной категории (раздел без группы).
func (c Card) categoryBody(cat Category) string {
	switch {
	case cat.Key == DiaryKey:
		return c.diary()
	case cat.Kind == Text:
		return c.text(cat.Key)
	case cat.Kind == List:
		return c.list(cat.Key)
	}
	if l := c.scalar(cat.Key, scalarFormat(cat)); l != "" {
		return l
	}
	return notMentioned
}

// groupLines — строки категории внутри группы: «— Название: значение».
func (c Card) groupLines(cat Category) []string {
	switch cat.Kind {
	case Text:
		lines := c.textLines(cat.Key)
		if len(lines) == 0 {
			return []string{"— " + cat.Title + ": " + notMentioned}
		}
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = "— " + cat.Title + ": " + l
		}
		return out
	case List:
		body := c.list(cat.Key)
		if body == notMentioned {
			return []string{"— " + cat.Title + ": " + notMentioned}
		}
		return []string{cat.Title + ":\n" + body}
	}
	if l := c.scalar(cat.Key, scalarFormat(cat)); l != "" {
		return []string{"— " + cat.Title + ": " + l}
	}
	return []string{"— " + cat.Title + ": " + notMentioned}
}

// scalarFormat — как печатать значение числа или да/нет.
func scalarFormat(cat Category) func(any) string {
	if cat.Kind == Bool {
		return func(v any) string {
			if b, _ := v.(bool); b {
				return "да"
			}
			return "нет"
		}
	}
	return func(v any) string {
		f, _ := v.(float64)
		s := FormatNumber(f)
		if cat.Unit != "" {
			s += " " + cat.Unit
		}
		return s
	}
}

func (c Card) hasUnconfirmed() bool {
	for _, v := range c.Voices {
		if !v.Confirmed {
			return true
		}
	}
	return false
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
		var p string
		switch {
		case v.VoiceID == c.Primary:
			p = "основная " + at.Format("02.01 15:04")
		case v.Relation == "retro":
			p = "из записи от " + v.Logical.Format("02.01") + " " + at.Format("15:04")
		default:
			p = "доп. " + at.Format("15:04")
		}
		if !v.Confirmed {
			p += " ⚠"
		}
		parts = append(parts, p)
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
	for _, p := range c.Parts[DiaryKey] {
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
