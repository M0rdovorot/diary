package entry

import (
	"strings"
	"time"
)

// View — карточка дня в виде структуры: всё, что печатает RenderCard, но без склейки в текст.
// Слияние порций (дополнить текст, объединить списки, взять свежее число) происходит здесь,
// поэтому текст для Telegram и веб-интерфейс показывают одно и то же.
type View struct {
	Date        string        `json:"date"` // ГГГГ-ММ-ДД; пусто — дата не определена
	Voices      []VoiceView   `json:"voices"`
	Unconfirmed bool          `json:"unconfirmed"` // есть неподтверждённые расшифровки
	Sections    []SectionView `json:"sections"`
}

// VoiceView — запись, из которой собрана карточка.
type VoiceView struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"` // «основная 05.09 21:40», «доп. 23:10», «из записи от 05.09 08:15»
	Confirmed bool   `json:"confirmed"`
}

// SectionView — раздел карточки: одна категория или группа категорий.
type SectionView struct {
	Title     string     `json:"title"`
	Group     bool       `json:"group"`
	Mentioned bool       `json:"mentioned"` // хотя бы одна категория раздела упомянута
	Items     []ItemView `json:"items"`
}

// ItemView — значение одной категории за день.
type ItemView struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Kind  string `json:"kind"` // diary | text | list | number | bool
	// Blocks — дневниковая запись по порциям (только diary).
	Blocks []BlockView `json:"blocks,omitempty"`
	// Values — текст: все порции; список: элементы без дублей; число и да/нет: самое свежее.
	Values []ValueView `json:"values,omitempty"`
	// Earlier — число и да/нет: прежние значения, отличавшиеся от свежего (конфликт).
	Earlier []ValueView `json:"earlier,omitempty"`
}

func (it ItemView) Mentioned() bool { return len(it.Blocks) > 0 || len(it.Values) > 0 }

// BlockView — порция дневниковой записи из одной записи. Пустой Header — единственная
// порция из основной записи, заголовок ей не нужен.
type BlockView struct {
	Header string   `json:"header"` // «Основная запись · 21:40», «Дополнение · 23:10», «Дополнение из записи от 05.09 · 08:15»
	Texts  []string `json:"texts"`
}

// ValueView — значение с пометкой происхождения; у основной записи пометки нет.
type ValueView struct {
	Text string `json:"text"`
	Mark string `json:"mark,omitempty"` // «доп. запись 23:10», «из записи от 05.09»
}

// View собирает карточку по активным категориям схемы.
func (c Card) View(schema Schema) View {
	v := View{Unconfirmed: c.hasUnconfirmed(), Voices: []VoiceView{}, Sections: []SectionView{}}
	if !c.Date.IsZero() {
		v.Date = c.Date.Format("2006-01-02")
	}
	for _, s := range c.Voices {
		v.Voices = append(v.Voices, VoiceView{ID: s.VoiceID, Label: c.voiceLabel(s), Confirmed: s.Confirmed})
	}
	for _, s := range sections(schema) {
		sv := SectionView{Title: s.title, Group: s.group}
		for _, cat := range s.cats {
			it := c.item(cat)
			sv.Mentioned = sv.Mentioned || it.Mentioned()
			sv.Items = append(sv.Items, it)
		}
		v.Sections = append(v.Sections, sv)
	}
	return v
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

// item сливает порции одной категории. Правила: текст — дополняется (все порции), списки —
// объединяются, числа и да/нет — побеждает самое свежее, прежние отличавшиеся идут в Earlier.
func (c Card) item(cat Category) ItemView {
	it := ItemView{Key: cat.Key, Title: cat.Title, Kind: KindName(cat.Kind)}
	switch {
	case cat.Key == DiaryKey:
		it.Kind = "diary"
		it.Blocks = c.diaryBlocks()
	case cat.Kind == Text:
		for _, p := range c.Parts[cat.Key] {
			if s, ok := p.Value.(string); ok {
				it.Values = append(it.Values, ValueView{Text: s, Mark: c.mark(p.Src)})
			}
		}
	case cat.Kind == List:
		seen := map[string]bool{}
		for _, p := range c.Parts[cat.Key] {
			items, _ := p.Value.([]string)
			for _, s := range items {
				if !seen[s] {
					seen[s] = true
					it.Values = append(it.Values, ValueView{Text: s, Mark: c.mark(p.Src)})
				}
			}
		}
	default:
		parts := c.Parts[cat.Key]
		if len(parts) == 0 {
			break
		}
		format := scalarFormat(cat)
		last := parts[len(parts)-1]
		it.Values = []ValueView{{Text: format(last.Value), Mark: c.mark(last.Src)}}
		seen := map[string]bool{format(last.Value): true}
		for _, p := range parts[:len(parts)-1] {
			if s := format(p.Value); !seen[s] {
				seen[s] = true
				it.Earlier = append(it.Earlier, ValueView{Text: s, Mark: c.mark(p.Src)})
			}
		}
	}
	return it
}

// diaryBlocks раскладывает дневниковую запись по порциям: подряд идущие порции одного
// голосового — один блок. Единственная порция из основной записи — без заголовка.
func (c Card) diaryBlocks() []BlockView {
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
		if n := len(blocks); n > 0 && blocks[n-1].src.VoiceID == p.Src.VoiceID {
			blocks[n-1].texts = append(blocks[n-1].texts, t)
			continue
		}
		blocks = append(blocks, block{src: p.Src, texts: []string{t}})
	}
	if len(blocks) == 1 && blocks[0].src.VoiceID == c.Primary {
		return []BlockView{{Texts: blocks[0].texts}}
	}
	var out []BlockView
	for _, b := range blocks {
		out = append(out, BlockView{Header: c.blockHeader(b.src), Texts: b.texts})
	}
	return out
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
		return "Основная запись · " + clock
	case s.Relation == "retro":
		return "Дополнение из записи от " + s.Logical.Format("02.01") + " · " + clock
	default:
		return "Дополнение · " + clock
	}
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
		return "из записи от " + s.Logical.Format("02.01")
	}
	return "доп. запись " + s.SentAt.In(c.loc()).Format("15:04")
}

func (c Card) voiceLabel(v Source) string {
	at := v.SentAt.In(c.loc())
	switch {
	case v.VoiceID == c.Primary:
		return "основная " + at.Format("02.01 15:04")
	case v.Relation == "retro":
		return "из записи от " + v.Logical.Format("02.01") + " " + at.Format("15:04")
	default:
		return "доп. " + at.Format("15:04")
	}
}

// joinNonEmpty — вспомогательная склейка для текстовой отрисовки.
func joinNonEmpty(parts []string, sep string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
