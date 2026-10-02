// Package entry описывает структуру записи дня: поля, разбор JSON от модели, отрисовку.
package entry

type Kind int

const (
	Text   Kind = iota // строка; при слиянии дополняется
	List               // список строк; при слиянии объединяется
	Number             // число; побеждает самое свежее
	Bool               // да/нет; побеждает самое свежее
)

type Field struct {
	Key  string
	Kind Kind
}

// Fields — все поля записи дня. Порядок не важен, порядок вывода задан в render.go.
// Ключи нейтральные намеренно (фильтр YandexGPT): gonenie_lysogo и love.
var Fields = []Field{
	{"diary", Text},
	{"mood", Text},
	{"food", Text},
	{"stool", Text},
	{"sleep_hours", Number},
	{"sleep_quality", Text},
	{"physical", Text},
	{"work", Text},
	{"katya", Text},
	{"done", List},
	{"not_done", List},
	{"water_ml", Number},
	{"sweets", Bool},
	{"alcohol", Bool},
	{"smoking", Bool},
	{"gonenie_lysogo", Bool},
	{"love", Bool},
	{"tea", Bool},
	{"to_think", List},
	{"for_psychologist", List},
}

var byKey = func() map[string]Field {
	m := make(map[string]Field, len(Fields))
	for _, f := range Fields {
		m[f.Key] = f
	}
	return m
}()

func Lookup(key string) (Field, bool) {
	f, ok := byKey[key]
	return f, ok
}
