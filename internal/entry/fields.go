// Package entry описывает структуру записи дня: категории, разбор JSON от модели, отрисовку.
package entry

import (
	"fmt"
	"sort"
	"strings"
)

type Kind int

const (
	Text   Kind = iota // строка; при слиянии дополняется
	List               // список строк; при слиянии объединяется
	Number             // число; побеждает самое свежее
	Bool               // да/нет; побеждает самое свежее
)

// KindName — имя типа в БД и в промпте.
func KindName(k Kind) string {
	switch k {
	case List:
		return "list"
	case Number:
		return "number"
	case Bool:
		return "bool"
	}
	return "text"
}

// ParseKind разбирает имя типа из БД.
func ParseKind(s string) (Kind, bool) {
	switch s {
	case "text":
		return Text, true
	case "list":
		return List, true
	case "number":
		return Number, true
	case "bool":
		return Bool, true
	}
	return Text, false
}

// KindLabel — название типа для пользователя.
func KindLabel(k Kind) string {
	switch k {
	case List:
		return "список"
	case Number:
		return "число"
	case Bool:
		return "да/нет"
	}
	return "текст"
}

// DiaryKey — категория с рассказом от первого лица. Для неё особая отрисовка (блоки по
// источникам), поэтому её нельзя скрыть.
const DiaryKey = "diary"

// Пределы длины пользовательских свойств категории (они попадают в системный промпт модели).
const (
	MaxTitleRunes = 40
	MaxHintRunes  = 300
	MaxUnitRunes  = 12
	MaxGroupRunes = 30
)

// Category — категория записи дня, которую выделяет модель. Набор категорий редактирует пользователь.
type Category struct {
	Key      string // стабильный идентификатор (латиница): ключ JSON и поле в observations
	Title    string // название для пользователя
	Kind     Kind
	Hint     string // что модель должна записывать в эту категорию
	Unit     string // единица измерения для чисел («ч», «мл»)
	Group    string // название группы: категории одной группы печатаются одним разделом
	Position int
	Active   bool // false — категория скрыта: не попадает в промпт и карточки, данные сохраняются
}

// Schema — набор категорий в порядке отображения.
type Schema struct {
	All   []Category
	byKey map[string]Category
}

func NewSchema(cats []Category) Schema {
	all := append([]Category(nil), cats...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Position < all[j].Position })
	s := Schema{All: all, byKey: make(map[string]Category, len(all))}
	for _, c := range all {
		s.byKey[c.Key] = c
	}
	return s
}

// Lookup находит категорию по ключу, в том числе скрытую.
func (s Schema) Lookup(key string) (Category, bool) {
	c, ok := s.byKey[key]
	return c, ok
}

// Active — активные категории в порядке отображения.
func (s Schema) Active() []Category {
	var out []Category
	for _, c := range s.All {
		if c.Active {
			out = append(out, c)
		}
	}
	return out
}

// Groups — названия групп активных категорий в порядке первого появления.
func (s Schema) Groups() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range s.Active() {
		if c.Group != "" && !seen[c.Group] {
			seen[c.Group] = true
			out = append(out, c.Group)
		}
	}
	return out
}

// FindByTitle ищет активную категорию по названию без учёта регистра.
func (s Schema) FindByTitle(title string) (Category, bool) {
	title = strings.TrimSpace(title)
	for _, c := range s.Active() {
		if strings.EqualFold(c.Title, title) {
			return c, true
		}
	}
	return Category{}, false
}

// DefaultCategories — набор по умолчанию. Записывается в БД при первом запуске (недостающие ключи
// добавляются и позже); дальше пользователь редактирует его командой /categories.
func DefaultCategories() []Category {
	c := func(key, title string, kind Kind, group, unit, hint string) Category {
		return Category{Key: key, Title: title, Kind: kind, Group: group, Unit: unit, Hint: hint, Active: true}
	}
	cats := []Category{
		c("diary", "Дневниковая запись", Text, "", "", "связный рассказ от первого лица («я») о том, что автор рассказал про этот день. Если сегмент — дополнение к уже рассказанному дню, пиши только добавленное."),
		c("mood", "Настроение", Text, "", "", "эмоциональное состояние."),
		c("food", "Питание", Text, "", "", "что ел."),
		c("stool", "Стул", Text, "", "", "что сказано про стул."),
		c("sleep_hours", "Часы сна", Number, "Сон", "ч", "сколько часов спал (например 7.5)."),
		c("sleep_quality", "Качество сна", Text, "Сон", "", "как спал: качество сна, пробуждения, самочувствие утром."),
		c("physical", "Физическое состояние", Text, "", "", "самочувствие, боль, усталость, энергия, спорт и нагрузка."),
		c("work", "Работа", Text, "", "", "что сделано, как ощущается качество работы, впечатления и эмоции от работы."),
		c("hobby", "Хобби", Text, "", "", "чем занимался в рамках хобби, впечатления и эмоции."),
		c("pet_project", "Пэт-проект", Text, "", "", "что сделано по личному (пэт-)проекту, прогресс, впечатления и эмоции."),
		c("cinema", "Кино", Text, "", "", "какие фильмы, сериалы или мультфильмы смотрел: название, впечатления, что понравилось или нет, мысли о них."),
		c("literature", "Литература", Text, "", "", "что читал или слушал (книги, статьи, стихи): название, автор, впечатления, мысли о прочитанном."),
		c("katya", "Катя", Text, "", "", "мысли автора о Кате и его состояние по отношению к ней."),
		c("done", "Успел", List, "Основные занятия", "", "что автор успел сделать за день."),
		c("not_done", "Не успел", List, "Основные занятия", "", "что автор не успел или отложил."),
		c("water_ml", "Вода и другие жидкости", Number, "Привычки", "мл", "сколько воды и других жидкостей выпил, в миллилитрах (1 литр = 1000)."),
		c("sweets", "Сладкое", Bool, "Привычки", "", "было ли сладкое."),
		c("alcohol", "Алкоголь", Bool, "Привычки", "", "пил ли алкоголь."),
		c("smoking", "Курение", Bool, "Привычки", "", "курил ли."),
		c("gonenie_lysogo", "Гонение лысого", Bool, "Привычки", "", "см. правило про интимные темы."),
		c("love", "Занятие любовью", Bool, "Привычки", "", "см. правило про интимные темы."),
		c("tea", "Китайский чай", Bool, "Привычки", "", "пил ли китайский чай."),
		c("to_think", "Мысли на подумать", List, "", "", "мысли и вопросы, к которым автор хочет вернуться и обдумать."),
		c("key_thoughts", "Критические мысли", List, "", "", "ТОЛЬКО основные мысли из рассуждений, которые автор сам явно пометил как важные и подлежащие сохранению (например: «важная мысль», «критическая мысль», «запомни/сохрани эту мысль»). Формулировка автора, мысль понятна без контекста. Не помечал — null. Сам не выбирай."),
		c("for_psychologist", "Мысли для психолога", List, "", "", "ТОЛЬКО то, что автор сам прямо назвал мыслью или вопросом для психолога либо сказал обсудить с психологом. Не додумывай и не относи сюда ничего сам. Если автор этого не говорил — null. Сохраняй формулировку автора."),
	}
	for i := range cats {
		cats[i].Position = (i + 1) * 10
	}
	return cats
}

// DefaultSchema — схема по умолчанию (для тестов и как запасной вариант).
func DefaultSchema() Schema { return NewSchema(DefaultCategories()) }

// CleanTitle проверяет название категории. Возвращает очищенное название или текст ошибки.
// exceptKey — ключ редактируемой категории (её собственное название дублем не считается).
func CleanTitle(schema Schema, title, exceptKey string) (string, string) {
	title = strings.Join(strings.Fields(title), " ")
	switch n := len([]rune(title)); {
	case n == 0:
		return "", "Название не может быть пустым."
	case n > MaxTitleRunes:
		return "", fmt.Sprintf("Слишком длинно (%d символов, максимум %d). Сократите, пожалуйста.", n, MaxTitleRunes)
	}
	if c, ok := schema.FindByTitle(title); ok && c.Key != exceptKey {
		return "", "Категория с таким названием уже есть. Придумайте другое название."
	}
	return title, ""
}
