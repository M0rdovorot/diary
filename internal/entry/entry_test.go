package entry

import (
	"strings"
	"testing"
	"time"
)

func TestParseExtraction(t *testing.T) {
	raw := "Вот результат:\n```json\n" + `{
  "segments": [
    {
      "date": "2026-09-01",
      "date_source": "relative",
      "diary": "Сегодня был тяжёлый день.",
      "mood": "не упоминалось",
      "sleep_hours": "7,5",
      "water_ml": 1500,
      "alcohol": false,
      "sweets": null,
      "done": ["отчёт", "отчёт", "  ", "зал"],
      "to_think": "переезд",
      "unknown_field": "игнорируется"
    },
    {"date": null, "date_source": "unknown", "katya": "скучаю"},
    {"date": "2026-09-02", "date_source": "explicit"}
  ]
}` + "\n```"

	segs, err := ParseExtraction(raw, DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 { // третий сегмент без данных отброшен
		t.Fatalf("segments: %d", len(segs))
	}
	s := segs[0]
	if s.Date != "2026-09-01" || s.DateSource != SourceRelative {
		t.Fatalf("date: %+v", s)
	}
	if _, ok := s.Values["mood"]; ok {
		t.Error("заглушка «не упоминалось» должна стать отсутствием поля")
	}
	if _, ok := s.Values["sweets"]; ok {
		t.Error("null должен быть отсутствием поля")
	}
	if v, ok := s.Values["alcohol"].(bool); !ok || v {
		t.Errorf("alcohol=false должен сохраниться: %v", s.Values["alcohol"])
	}
	if s.Values["sleep_hours"] != 7.5 {
		t.Errorf("sleep_hours: %v", s.Values["sleep_hours"])
	}
	if done, _ := s.Values["done"].([]string); len(done) != 2 {
		t.Errorf("done: %v", s.Values["done"])
	}
	if tt, _ := s.Values["to_think"].([]string); len(tt) != 1 || tt[0] != "переезд" {
		t.Errorf("to_think из строки: %v", s.Values["to_think"])
	}
	if _, ok := s.Values["unknown_field"]; ok {
		t.Error("неизвестные поля должны отбрасываться")
	}
	if segs[1].DateSource != SourceUnknown || segs[1].Date != "" {
		t.Errorf("unknown segment: %+v", segs[1])
	}
}

func TestParseExtractionErrors(t *testing.T) {
	for _, raw := range []string{"", "просто текст", `{"segments": []}`, `{"segments": [{"date":"2026-09-01"}]}`, `{oops}`} {
		if _, err := ParseExtraction(raw, DefaultSchema()); err == nil {
			t.Errorf("ожидалась ошибка для %q", raw)
		}
	}
}

func TestResolvedDate(t *testing.T) {
	ref := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		seg Segment
		ok  bool
	}{
		{Segment{Date: "2026-09-10", DateSource: SourceExplicit}, true},
		{Segment{Date: "2026-09-01", DateSource: SourceRelative}, true},
		{Segment{Date: "2026-09-11", DateSource: SourceExplicit}, false}, // будущее
		{Segment{Date: "2020-01-01", DateSource: SourceExplicit}, false}, // слишком давно
		{Segment{Date: "", DateSource: SourceUnknown}, false},
		{Segment{Date: "2026-09-01", DateSource: SourceUnknown}, false},
	}
	for _, tc := range cases {
		if _, ok := tc.seg.ResolvedDate(ref); ok != tc.ok {
			t.Errorf("%+v: got %v", tc.seg, ok)
		}
	}
}

func TestRender(t *testing.T) {
	seg := Segment{Values: map[string]any{
		"diary":       "Я поработал.",
		"sleep_hours": 7.5,
		"water_ml":    1500.0,
		"alcohol":     false,
		"love":        true,
		"done":        []string{"отчёт"},
	}}
	d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	out := Render(seg, &d, DefaultSchema())
	for _, want := range []string{
		"Дата: 01.09.2026", "Я поработал.", "— Часы сна: 7.5 ч", "— Вода и другие жидкости: 1500 мл",
		"— Алкоголь: нет", "— Занятие любовью: да", "— Сладкое: не упоминалось", "Успел:\n— отчёт",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в:\n%s", want, out)
		}
	}
	if !strings.Contains(Render(seg, nil, DefaultSchema()), "Дата: не определена") {
		t.Error("дата не определена")
	}
}

func TestBuildCardMerge(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	primary := Source{VoiceID: 1, SentAt: time.Date(2026, 9, 1, 21, 40, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: true}
	extra := Source{VoiceID: 2, SentAt: time.Date(2026, 9, 1, 23, 10, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: true}
	retro := Source{VoiceID: 3, SentAt: time.Date(2026, 9, 5, 8, 15, 0, 0, msk),
		Logical: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Relation: "retro", Confirmed: true}

	obs := []Observation{
		{"mood", "спокойно", primary},
		{"sleep_hours", 7.0, primary},
		{"alcohol", false, primary},
		{"done", []string{"отчёт"}, primary},
		{"mood", "к вечеру устал", extra},
		{"done", []string{"отчёт", "зал"}, extra},
		{"sleep_hours", 6.0, retro},
		{"alcohol", true, retro},
		{"katya", "вспомнил разговор", retro},
	}
	out := RenderCard(BuildCard(day, msk, obs), DefaultSchema())

	for _, want := range []string{
		"Записи: основная 01.09 21:40; доп. 23:10; из записи от 05.09 08:15",
		"спокойно\nк вечеру устал (доп. запись 23:10)",     // текст дополняется
		"Успел:\n— отчёт\n— зал (доп. запись 23:10)",       // список без дублей
		"— Часы сна: 6 ч (из записи от 05.09); ранее: 7 ч", // число: свежее + ранее
		"— Алкоголь: да (из записи от 05.09); ранее: нет",  // да/нет: свежее + ранее
		"вспомнил разговор (из записи от 05.09)",           // источник retro
		"— Сладкое: не упоминалось",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в:\n%s", want, out)
		}
	}
	if strings.Contains(out, "спокойно (") {
		t.Error("у основной записи не должно быть пометки")
	}
}

func TestParseUserDate(t *testing.T) {
	ref := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	ok := map[string]string{
		"05.09": "2026-09-05", "05.09.2026": "2026-09-05", "5.9.26": "", // последнее — неверный формат
		"вчера": "2026-09-09", " Сегодня ": "2026-09-10", "позавчера": "2026-09-08",
		"25.12": "2025-12-25", // без года и в будущем — прошлый год
	}
	for in, want := range ok {
		got, err := ParseUserDate(in, ref)
		if want == "" {
			if err == nil {
				t.Errorf("%q: ожидалась ошибка", in)
			}
			continue
		}
		if err != nil || got.Format("2006-01-02") != want {
			t.Errorf("%q: got %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"11.09.2026", "01.01.2015", "абвг", ""} {
		if _, err := ParseUserDate(in, ref); err == nil {
			t.Errorf("%q: ожидалась ошибка", in)
		}
	}
}

func TestDecodeValues(t *testing.T) {
	v := DecodeValues(DefaultSchema(), []byte(`{"mood":"ок","done":["а","б"],"water_ml":1500,"alcohol":false,"junk":1}`))
	if v["mood"] != "ок" || v["water_ml"] != 1500.0 || v["alcohol"] != false {
		t.Fatalf("%v", v)
	}
	if d, _ := v["done"].([]string); len(d) != 2 {
		t.Fatalf("done: %v", v["done"])
	}
	if _, ok := v["junk"]; ok {
		t.Error("junk")
	}
}

func TestDiaryBlocks(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	primary := Source{VoiceID: 1, SentAt: time.Date(2026, 9, 1, 21, 40, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: true}
	extra := Source{VoiceID: 2, SentAt: time.Date(2026, 9, 1, 23, 10, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: true}
	retro := Source{VoiceID: 3, SentAt: time.Date(2026, 9, 5, 8, 15, 0, 0, msk),
		Logical: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Relation: "retro", Confirmed: true}

	// одна основная порция — без заголовка
	one := RenderCard(BuildCard(day, msk, []Observation{{"diary", "Я поработал.", primary}}), DefaultSchema())
	if strings.Contains(one, "▸") || !strings.Contains(one, "Дневниковая запись\nЯ поработал.") {
		t.Errorf("одна порция:\n%s", one)
	}

	// несколько порций — явные блоки
	out := RenderCard(BuildCard(day, msk, []Observation{
		{"diary", "Первая часть.", primary},
		{"diary", "Вторая часть того же голосового.", primary},
		{"diary", "Вечером добавил.", extra},
		{"diary", "Вспомнил позже.", retro},
	}), DefaultSchema())
	for _, want := range []string{
		"▸ Основная запись · 21:40\nПервая часть.\nВторая часть того же голосового.\n\n",
		"▸ Дополнение · 23:10\nВечером добавил.\n\n",
		"▸ Дополнение из записи от 05.09 · 08:15\nВспомнил позже.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в:\n%s", want, out)
		}
	}
	if strings.Count(out, "▸") != 3 {
		t.Errorf("ожидалось 3 блока:\n%s", out)
	}

	// только дополнение из другого дня, основной записи нет — тоже с заголовком
	only := RenderCard(BuildCard(day, msk, []Observation{{"diary", "Вспомнил позже.", retro}}), DefaultSchema())
	if !strings.Contains(only, "▸ Дополнение из записи от 05.09") {
		t.Errorf("только retro:\n%s", only)
	}

	// основная запись отправлена в другой день (выбран рабочий день) — дата в заголовке
	late := Source{VoiceID: 9, SentAt: time.Date(2026, 9, 5, 10, 0, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: true}
	out = RenderCard(BuildCard(day, msk, []Observation{{"diary", "А", late}, {"diary", "Б", extra}}), DefaultSchema())
	if !strings.Contains(out, "▸ Основная запись · 05.09 10:00") {
		t.Errorf("дата основной записи:\n%s", out)
	}
}

func TestCustomCategories(t *testing.T) {
	cats := DefaultCategories()
	for i := range cats {
		if cats[i].Key == "tea" { // скрываем «Китайский чай»
			cats[i].Active = false
		}
	}
	cats = append(cats,
		Category{Key: "running", Title: "Бег", Kind: Number, Unit: "км", Position: 1000, Active: true},
		Category{Key: "books", Title: "Книги", Kind: List, Group: "Привычки", Position: 1010, Active: true}, // в существующую группу
		Category{Key: "gratitude", Title: "Благодарность", Kind: Text, Position: 5, Active: true},           // в самое начало
	)
	schema := NewSchema(cats)

	// разбор: активные категории принимаются, скрытая отбрасывается
	segs, err := ParseExtraction(`{"segments":[{"date":"2026-09-01","date_source":"explicit",
		"diary":"д","running":"5,5","books":["Дюна"],"gratitude":"спасибо","tea":true}]}`, schema)
	if err != nil {
		t.Fatal(err)
	}
	v := segs[0].Values
	if v["running"] != 5.5 || v["gratitude"] != "спасибо" {
		t.Errorf("values: %v", v)
	}
	if b, _ := v["books"].([]string); len(b) != 1 {
		t.Errorf("books: %v", v["books"])
	}
	if _, ok := v["tea"]; ok {
		t.Error("скрытая категория не должна разбираться")
	}

	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	out := Render(segs[0], &day, schema)
	for _, want := range []string{
		"Бег\n5.5 км",            // единица измерения
		"Книги:\n— Дюна",         // категория добавлена в существующую группу «Привычки»
		"Благодарность\nспасибо", // порядок: Position 5 — раньше всех
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Китайский чай") {
		t.Errorf("скрытая категория напечатана:\n%s", out)
	}
	if strings.Index(out, "Благодарность") > strings.Index(out, "Дневниковая запись") {
		t.Errorf("порядок категорий не учтён:\n%s", out)
	}

	// скрытая категория: данные в БД читаются (Lookup видит её), но в карточку не попадают
	if c, ok := schema.Lookup("tea"); !ok || c.Active {
		t.Errorf("Lookup скрытой: %+v %v", c, ok)
	}
	if got := schema.Groups(); len(got) != 3 || got[0] != "Сон" {
		t.Errorf("groups: %v", got)
	}
	if c, ok := schema.FindByTitle("  бег "); !ok || c.Key != "running" {
		t.Errorf("FindByTitle: %+v %v", c, ok)
	}
	if _, ok := schema.FindByTitle("Китайский чай"); ok {
		t.Error("FindByTitle не должен находить скрытые")
	}
}

func TestGroupSectionLayout(t *testing.T) {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	schema := DefaultSchema()

	// в группе ничего не упомянуто — одна строка
	seg := Segment{Values: map[string]any{"diary": "Я."}}
	out := Render(seg, &day, schema)
	if !strings.Contains(out, "Сон\nне упоминалось") || !strings.Contains(out, "Привычки\nне упоминалось") {
		t.Errorf("пустые группы:\n%s", out)
	}
	// упомянуто одно — остальные члены группы показываются как «не упоминалось»
	seg = Segment{Values: map[string]any{"sleep_hours": 7.0}}
	out = Render(seg, &day, schema)
	if !strings.Contains(out, "Сон\n— Часы сна: 7 ч\n— Качество сна: не упоминалось") {
		t.Errorf("частично заполненная группа:\n%s", out)
	}
}

func TestUnconfirmedMarks(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ok := Source{VoiceID: 1, SentAt: time.Date(2026, 9, 1, 21, 40, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: true}
	bad := Source{VoiceID: 2, SentAt: time.Date(2026, 9, 1, 23, 10, 0, 0, msk), Logical: day, Relation: "same_day", Confirmed: false}

	// одна неподтверждённая запись — предупреждение есть
	one := RenderCard(BuildCard(day, msk, []Observation{{"mood", "ок", bad}}), DefaultSchema())
	if !strings.Contains(one, "⚠ Есть неподтверждённые расшифровки") {
		t.Errorf("нет предупреждения:\n%s", one)
	}
	// несколько записей — ⚠ только у неподтверждённой
	two := RenderCard(BuildCard(day, msk, []Observation{{"mood", "ок", ok}, {"food", "суп", bad}}), DefaultSchema())
	if !strings.Contains(two, "основная 01.09 21:40; доп. 23:10 ⚠") {
		t.Errorf("пометка в списке записей:\n%s", two)
	}
	// все подтверждены — предупреждения нет
	clean := RenderCard(BuildCard(day, msk, []Observation{{"mood", "ок", ok}}), DefaultSchema())
	if strings.Contains(clean, "⚠") {
		t.Errorf("лишнее предупреждение:\n%s", clean)
	}
	// ответ на голосовое (CardFromSegment) предупреждений не содержит
	if strings.Contains(Render(Segment{Values: map[string]any{"mood": "ок"}}, &day, DefaultSchema()), "⚠") {
		t.Error("в ответе на голосовое предупреждения быть не должно")
	}
}

func TestKindNames(t *testing.T) {
	for _, k := range []Kind{Text, List, Number, Bool} {
		got, ok := ParseKind(KindName(k))
		if !ok || got != k {
			t.Errorf("kind %v: %v %v", k, got, ok)
		}
		if KindLabel(k) == "" {
			t.Errorf("нет подписи для %v", k)
		}
	}
	if _, ok := ParseKind("xxx"); ok {
		t.Error("неизвестный тип")
	}
}

func TestSlugKey(t *testing.T) {
	taken := map[string]bool{"hobby": true, "hobby_2": true}
	free := func(k string) bool { return taken[k] }
	cases := map[string]string{
		"Бег":                   "beg",
		"Пэт-проект":            "pet_proekt",
		"Чтение книг (научных)": "chtenie_knig_nauchnyh",
		"hobby":                 "hobby_3", // занято дважды
		"Щи!":                   "schi",
		"???":                   "cat",
		"Date":                  "c_date", // зарезервировано форматом ответа
		"Йога 2":                "yoga_2",
	}
	for in, want := range cases {
		if got := SlugKey(in, free); got != want {
			t.Errorf("SlugKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SlugKey(strings.Repeat("а", 50), free); len(got) > 30 {
		t.Errorf("слишком длинный ключ: %q", got)
	}
}
