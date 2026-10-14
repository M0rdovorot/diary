package webapp

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (hs *harness) addEntry(date, text string) int64 {
	hs.t.Helper()
	var created struct{ ID int64 }
	body := `{"date":"` + date + `","text":"` + text + `"}`
	if code := hs.do("POST", "/api/entries", body, &created); code != 202 {
		hs.t.Fatalf("add: %d", code)
	}
	hs.wait(created.ID)
	return created.ID
}

func (hs *harness) waitBulk() bulkState {
	hs.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hs.s.bulk.snapshot().State == "running" {
		if time.Now().After(deadline) {
			hs.t.Fatal("пересборка не закончилась")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return hs.s.bulk.snapshot()
}

func TestCategoriesAPI(t *testing.T) {
	hs := newHarness(t)

	var list categoriesResponse
	if code := hs.do("GET", "/api/categories", "", &list); code != 200 {
		t.Fatalf("list: %d", code)
	}
	byKey := map[string]categoryDTO{}
	for _, c := range list.Categories {
		byKey[c.Key] = c
	}
	for _, k := range []string{"cinema", "literature", "key_thoughts"} {
		if c, ok := byKey[k]; !ok || !c.Active {
			t.Errorf("нет категории по умолчанию %q: %+v", k, c)
		}
	}
	if byKey["key_thoughts"].Kind != "list" || !byKey["diary"].Protected {
		t.Errorf("свойства: %+v %+v", byKey["key_thoughts"], byKey["diary"])
	}

	// создание: ключ строится из названия, единица только у чисел
	var created categoryDTO
	if code := hs.do("POST", "/api/categories", `{"title":"  Бег  ","kind":"number","unit":"км","hint":"сколько пробежал","group":"Спорт"}`, &created); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if created.Key != "beg" || created.Title != "Бег" || created.Unit != "км" || !created.Active {
		t.Errorf("создана: %+v", created)
	}
	if n := hs.lastNote(); !strings.Contains(n, "добавлена категория «Бег»") {
		t.Errorf("уведомление: %q", n)
	}
	for name, body := range map[string]string{
		"дубль названия":    `{"title":"бег","kind":"text"}`,
		"плохой тип":        `{"title":"Х","kind":"matrix"}`,
		"пустое имя":        `{"title":"  ","kind":"text"}`,
		"длинная подсказка": `{"title":"Y","kind":"text","hint":"` + strings.Repeat("я", 301) + `"}`,
		"лишнее поле":       `{"title":"Z","kind":"text","key":"diary"}`,
	} {
		if code := hs.do("POST", "/api/categories", body, nil); code != 400 {
			t.Errorf("%s: %d, ждали 400", name, code)
		}
	}

	// правка
	if code := hs.do("PATCH", "/api/categories/beg", `{"title":"Пробежка","hint":"км за день"}`, nil); code != 200 {
		t.Fatalf("patch: %d", code)
	}
	if code := hs.do("PATCH", "/api/categories/mood", `{"unit":"ч"}`, nil); code != 400 {
		t.Errorf("единица у текстовой: %d", code)
	}
	if code := hs.do("PATCH", "/api/categories/nope", `{"title":"Х"}`, nil); code != 404 {
		t.Errorf("нет такой: %d", code)
	}
	if code := hs.do("PATCH", "/api/categories/diary", `{"active":false}`, nil); code != 400 {
		t.Errorf("скрытие diary: %d", code)
	}

	// порядок: сдвиг вверх меняет с соседней
	if code := hs.do("POST", "/api/categories/beg/move", `{"delta":-1}`, nil); code != 200 {
		t.Fatalf("move: %d", code)
	}
	if code := hs.do("POST", "/api/categories/beg/move", `{"delta":5}`, nil); code != 400 {
		t.Errorf("сдвиг на 5: %d", code)
	}
	hs.do("GET", "/api/categories", "", &list)
	pos := map[string]int{}
	for i, c := range list.Categories {
		pos[c.Key] = i
	}
	if pos["beg"] >= pos["for_psychologist"] {
		t.Errorf("после сдвига вверх «Бег» должна быть выше: %v", pos)
	}

	// удаление: сначала скрыть; данные стираются вместе с категорией
	if code := hs.do("DELETE", "/api/categories/beg", "", nil); code != 409 {
		t.Errorf("удаление активной: %d", code)
	}
	if code := hs.do("PATCH", "/api/categories/beg", `{"active":false}`, nil); code != 200 {
		t.Fatal("hide")
	}
	var del map[string]int
	if code := hs.do("DELETE", "/api/categories/beg", "", &del); code != 200 || del["removed"] != 0 {
		t.Errorf("delete: %d %v", code, del)
	}
	if code := hs.do("DELETE", "/api/categories/beg", "", nil); code != 404 {
		t.Errorf("повторное удаление: %d", code)
	}
	if code := hs.do("DELETE", "/api/categories/diary", "", nil); code != 400 {
		t.Errorf("удаление diary: %d", code)
	}
}

func TestCategoryDays(t *testing.T) {
	hs := newHarness(t)
	hs.addEntry("2026-09-05", "первое")
	hs.addEntry("2026-09-07", "второе")
	hs.addEntry("2026-09-07", "третье") // тот же день: значения сливаются в один день

	var res categoryDaysResponse
	if code := hs.do("GET", "/api/categories/mood/days", "", &res); code != 200 {
		t.Fatalf("days: %d", code)
	}
	if res.Category.Title != "Настроение" || res.Category.Days != 2 || len(res.Days) != 2 {
		t.Fatalf("ответ: %+v", res)
	}
	if res.Days[0].Date != "2026-09-07" || res.Days[1].Date != "2026-09-05" {
		t.Errorf("новые дни должны идти первыми: %s, %s", res.Days[0].Date, res.Days[1].Date)
	}
	if got := res.Days[0].Item.Values; len(got) != 2 || got[0].Text != "ок" || got[0].Voice == 0 {
		t.Errorf("значения дня: %+v", got)
	}
	if res.Days[0].Title != "7 сентября 2026, понедельник" {
		t.Errorf("заголовок: %q", res.Days[0].Title)
	}

	// скрытая категория тоже читается; несуществующая — 404
	hs.do("PATCH", "/api/categories/mood", `{"active":false}`, nil)
	if code := hs.do("GET", "/api/categories/mood/days", "", &res); code != 200 || len(res.Days) != 2 || res.Category.Active {
		t.Errorf("скрытая: %d %+v", code, res.Category)
	}
	if code := hs.do("GET", "/api/categories/nope/days", "", nil); code != 404 {
		t.Errorf("нет категории: %d", code)
	}
	// пока по категории нет данных — пустой список, не ошибка
	var empty categoryDaysResponse
	if code := hs.do("GET", "/api/categories/cinema/days", "", &empty); code != 200 || empty.Days == nil || len(empty.Days) != 0 {
		t.Errorf("пустая: %d %+v", code, empty)
	}
}

func TestRebuild(t *testing.T) {
	hs := newHarness(t)
	a := hs.addEntry("2026-09-05", "про пятое")
	hs.addEntry("2026-09-07", "про седьмое")

	// вручную уточнённая дата и скрытая категория попадают в предупреждение
	var m monthResponse
	hs.do("GET", "/api/month?m=2026-09", "", &m)
	if len(m.Pending) != 2 {
		t.Fatalf("pending: %+v", m.Pending)
	}
	hs.do("POST", "/api/segments/"+strconv.FormatInt(m.Pending[0].ID, 10)+"/date", `{"date":"2026-09-04"}`, nil)
	hs.do("PATCH", "/api/categories/mood", `{"active":false}`, nil)

	var plan planResponse
	if code := hs.do("GET", "/api/rebuild/plan?scope=all", "", &plan); code != 200 {
		t.Fatalf("plan: %d", code)
	}
	if plan.Voices != 2 || plan.UserDates != 1 || plan.HiddenObs != 2 || plan.Days != 3 {
		t.Errorf("план для всех: %+v", plan)
	}
	if code := hs.do("GET", "/api/rebuild/plan?scope=day&d=2026-09-07", "", &plan); code != 200 || plan.Voices != 1 || plan.Days != 1 {
		t.Errorf("план для дня: %d %+v", code, plan)
	}
	for _, q := range []string{"scope=week", "scope=day", "scope=day&d=вчера"} {
		if code := hs.do("GET", "/api/rebuild/plan?"+q, "", nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	if code := hs.do("POST", "/api/rebuild", `{"scope":"day","date":"2026-01-01"}`, nil); code != 404 {
		t.Errorf("день без записей: %d", code)
	}

	// пересборка одного дня: разбирается только запись этого дня, с заменой
	hs.an.mu.Lock()
	hs.an.runs = nil
	hs.an.mu.Unlock()
	if code := hs.do("POST", "/api/rebuild", `{"scope":"day","date":"2026-09-07"}`, nil); code != 202 {
		t.Fatalf("start: %d", code)
	}
	st := hs.waitBulk()
	if st.State != "done" || st.Total != 1 || st.Done != 1 || st.Failed != 0 {
		t.Errorf("итог дня: %+v", st)
	}
	if got := strings.Join(hs.an.runs, "|"); got != "про седьмое/true" {
		t.Errorf("разборы: %q", got)
	}
	if n := hs.lastNote(); !strings.Contains(n, "пересборка записей дня 07.09.2026 завершена — пересобрано 1 из 1") {
		t.Errorf("уведомление: %q", n)
	}

	// все записи; сбой модели у записи не стирает прежний разбор
	hs.an.mu.Lock()
	hs.an.fail = true
	hs.an.mu.Unlock()
	if code := hs.do("POST", "/api/rebuild", `{"scope":"all"}`, nil); code != 202 {
		t.Fatalf("start all: %d", code)
	}
	st = hs.waitBulk()
	if st.Done != 0 || st.Failed != 2 {
		t.Errorf("итог со сбоем: %+v", st)
	}
	if n := hs.lastNote(); !strings.Contains(n, "не удалось: 2") {
		t.Errorf("уведомление: %q", n)
	}
	var d dayResponse
	if code := hs.do("GET", "/api/day?d=2026-09-05", "", &d); code != 200 || len(d.Voices) != 1 || d.Voices[0].ID != a {
		t.Errorf("прежний разбор должен остаться: %d %+v", code, d.Voices)
	}

	hs.an.mu.Lock()
	hs.an.fail = false
	hs.an.mu.Unlock()
	if code := hs.do("POST", "/api/rebuild", `{"scope":"all"}`, nil); code != 202 {
		t.Fatalf("start all: %d", code)
	}
	if st = hs.waitBulk(); st.Done != 2 {
		t.Errorf("итог: %+v", st)
	}
	// пока шла пересборка, вручную уточнённая дата потерялась — часть снова без дня (как и обещано в предупреждении)
	hs.do("GET", "/api/month?m=2026-09", "", &m)
	if len(m.Pending) != 2 || st.Pending != 2 {
		t.Errorf("части без даты после пересборки: %d (итог %d)", len(m.Pending), st.Pending)
	}
	if code := hs.do("GET", "/api/rebuild", "", &st); code != 200 || st.State != "done" {
		t.Errorf("статус: %d %+v", code, st)
	}
}

func TestRebuildRejectsSecondRun(t *testing.T) {
	hs := newHarness(t)
	hs.addEntry("2026-09-05", "текст")
	hs.s.bulk.update(func(b *bulkState) { b.State = "running" }) // как будто пересборка уже идёт
	if code := hs.do("POST", "/api/rebuild", `{"scope":"all"}`, nil); code != 409 {
		t.Errorf("вторая пересборка: %d", code)
	}
}

func TestRecordingFlow(t *testing.T) {
	hs := newHarness(t)
	dir := t.TempDir()
	hs.s.DataDir = dir

	// без настройки — понятный отказ, а календарь не обещает запись
	if code := hs.do("POST", "/api/recordings?date=2026-09-05&duration=5", "звук", nil); code != 503 {
		t.Errorf("без ffmpeg: %d", code)
	}
	var m monthResponse
	hs.do("GET", "/api/month?m=2026-09", "", &m)
	if m.CanRecord {
		t.Error("canRecord без настройки")
	}

	var heard string
	transcript := "привет это тест"
	hs.s.Convert = func(_ context.Context, in, out string) error {
		b, err := os.ReadFile(in)
		heard = string(b)
		os.WriteFile(out, b, 0o644)
		return err
	}
	hs.s.Transcribe = func(_ context.Context, path string) (string, error) {
		if !strings.HasSuffix(path, ".ogg") {
			t.Errorf("в распознавание ушёл не ogg: %s", path)
		}
		return transcript, nil
	}
	hs.do("GET", "/api/month?m=2026-09", "", &m)
	if !m.CanRecord {
		t.Error("canRecord не выставлен")
	}

	waitRec := func(id string) recordingStatus {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			var st recordingStatus
			if hs.do("GET", "/api/recordings/"+id, "", &st) == 200 && st.State != "running" {
				return st
			}
		}
		t.Fatal("распознавание не закончилось")
		return recordingStatus{}
	}

	for name, url := range map[string]string{
		"день в будущем": "/api/recordings?date=2026-12-31&duration=5",
		"не дата":        "/api/recordings?date=вчера&duration=5",
	} {
		if code := hs.do("POST", url, "звук", nil); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code := hs.do("POST", "/api/recordings?date=2026-09-05&duration=5", "", nil); code != 400 {
		t.Errorf("пустое тело: %d", code)
	}

	var up struct{ ID string }
	if code := hs.do("POST", "/api/recordings?date=2026-09-05&duration=75", "байты звука", &up); code != 202 || up.ID == "" {
		t.Fatalf("upload: %d", code)
	}
	st := waitRec(up.ID)
	if st.State != "done" || st.Text != transcript || st.Voice == 0 || heard != "байты звука" {
		t.Fatalf("распознавание: %+v (звук %q)", st, heard)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Errorf("звук не удалён после распознавания: %v", files)
	}

	// текст сохранён сразу, но запись ещё не разобрана и не подтверждена
	var m2 monthResponse
	hs.do("GET", "/api/month?m=2026-09", "", &m2)
	if len(m2.Unanalyzed) != 1 || m2.Unanalyzed[0].ID != st.Voice || m2.Unconfirmed != 1 || len(m2.Days) != 0 {
		t.Fatalf("до сохранения: %+v", m2)
	}

	// пользователь поправил текст и сохранил: правка, подтверждение, разбор
	voice := strconv.FormatInt(st.Voice, 10)
	if code := hs.do("POST", "/api/entries/"+voice+"/save", `{"text":"  привет, это тест!  "}`, nil); code != 202 {
		t.Fatalf("save: %d", code)
	}
	if es := hs.wait(st.Voice); es.State != stateDone || len(es.Days) != 1 || es.Days[0] != "2026-09-05" {
		t.Fatalf("после сохранения: %+v", es)
	}
	var d dayResponse
	hs.do("GET", "/api/day?d=2026-09-05", "", &d)
	if v := d.Voices[0]; v.Text != "привет, это тест!" || !v.Edited || !v.Confirmed || v.Source != "voice" || !strings.HasPrefix(v.Label, "Голосовое") || !strings.Contains(v.Label, "1:15") {
		t.Errorf("запись: %+v", v)
	}
	if n := hs.lastNote(); !strings.Contains(n, "добавлена запись на 05.09.2026") || !strings.Contains(n, "(голос)") {
		t.Errorf("уведомление: %q", n)
	}
	if code := hs.do("POST", "/api/entries/"+voice+"/save", `{"text":""}`, nil); code != 400 {
		t.Errorf("пустой текст: %d", code)
	}
	if code := hs.do("POST", "/api/entries/9999/save", `{"text":"х"}`, nil); code != 404 {
		t.Errorf("нет записи: %d", code)
	}

	// тишина: ничего не сохраняется
	transcript = ""
	hs.do("POST", "/api/recordings?date=2026-09-05&duration=3", "тишина", &up)
	if st := waitRec(up.ID); st.State != "failed" || !strings.Contains(st.Error, "не распознана") {
		t.Errorf("тишина: %+v", st)
	}
	// сбой распознавания: понятная ошибка без подробностей наружу
	hs.s.Transcribe = func(context.Context, string) (string, error) { return "", errors.New("secret internals") }
	hs.do("POST", "/api/recordings?date=2026-09-05&duration=3", "звук", &up)
	if st := waitRec(up.ID); st.State != "failed" || strings.Contains(st.Error, "secret") {
		t.Errorf("сбой: %+v", st)
	}
	hs.do("GET", "/api/month?m=2026-09", "", &m2)
	if len(m2.Unanalyzed) != 0 {
		t.Errorf("при сбое не должно появиться записей: %+v", m2.Unanalyzed)
	}
	if code := hs.do("GET", "/api/recordings/nope", "", nil); code != 404 {
		t.Errorf("чужой id: %d", code)
	}
}
