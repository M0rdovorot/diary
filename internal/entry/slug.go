package entry

import (
	"strconv"
	"strings"
)

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// Ключи, которые заняты форматом ответа модели.
var reservedKeys = map[string]bool{"date": true, "date_source": true, "segments": true}

// SlugKey строит ключ категории из названия: латиница, цифры и «_». taken сообщает, занят ли ключ
// (учитываются и скрытые категории: ключи не переиспользуются, чтобы старые данные не смешались).
func SlugKey(title string, taken func(string) bool) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case translit[r] != "":
			b.WriteString(translit[r])
		default:
			if _, isCyr := translit[r]; !isCyr { // пробелы, знаки и прочее — разделитель
				b.WriteByte('_')
			}
		}
	}
	key := strings.Trim(collapseUnderscores(b.String()), "_")
	if len(key) > 30 {
		key = strings.Trim(key[:30], "_")
	}
	if key == "" {
		key = "cat"
	}
	if reservedKeys[key] {
		key = "c_" + key
	}
	if !taken(key) {
		return key
	}
	for i := 2; ; i++ {
		if k := key + "_" + strconv.Itoa(i); !taken(k) {
			return k
		}
	}
}

func collapseUnderscores(s string) string {
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return s
}
