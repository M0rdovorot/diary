package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"diary/internal/entry"
)

// Интеграционные тесты идут против реального Postgres: TEST_DATABASE_URL (см. make test).
func openTest(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL не задан")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// чистая схема на каждый тест
	if _, err := s.pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateIdempotent(t *testing.T) {
	s := openTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestSaveExtraction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	logical := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	voiceID, err := s.SaveVoice(ctx, VoiceMessage{
		ChatID: 1, MessageID: 1, SentAt: time.Now(), LogicalDate: logical, ReferenceDate: logical, DurationSec: 10, Transcript: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	segs := []entry.Segment{
		{Date: "2026-09-05", DateSource: entry.SourceRelative, Values: map[string]any{
			"mood": "спокойно", "alcohol": false, "done": []string{"отчёт", "зал"},
		}},
		{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"sleep_hours": 6.0}},
		{Date: "", DateSource: entry.SourceUnknown, Values: map[string]any{"katya": "скучаю"}},
	}
	saved, err := s.SaveExtraction(ctx, ExtractionInput{VoiceID: voiceID, RefDate: logical, Today: logical,
		Model: "m", PromptVersion: "v", Raw: "raw", Segments: segs})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 || saved[0].Date == nil || saved[1].Date == nil || saved[2].Date != nil {
		t.Fatalf("saved: %+v", saved)
	}

	rows, err := s.pool.Query(ctx, `SELECT entry_date::text, field, relation FROM observations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var d, f, r string
		if err := rows.Scan(&d, &f, &r); err != nil {
			t.Fatal(err)
		}
		got = append(got, d+" "+f+" "+r)
	}
	want := []string{
		"2026-09-05 mood same_day", "2026-09-05 done same_day", "2026-09-05 alcohol same_day",
		"2026-09-01 sleep_hours retro",
	}
	if len(got) != len(want) {
		t.Fatalf("observations: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("observation %d: got %q, want %q", i, got[i], want[i])
		}
	}

	var pending int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM segments WHERE status='pending_date' AND entry_date IS NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
}

func TestSaveVoiceUpsert(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	v := VoiceMessage{
		ChatID: 1, MessageID: 10,
		SentAt:        time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC),
		LogicalDate:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		ReferenceDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		DurationSec:   42,
		Transcript:    "первый вариант",
	}
	id1, err := s.SaveVoice(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	v.Transcript = "второй вариант"
	id2, err := s.SaveVoice(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("дубль: %d != %d", id1, id2)
	}

	var got string
	var d time.Time
	if err := s.pool.QueryRow(ctx, `SELECT transcript, logical_date FROM voice_messages WHERE id=$1`, id1).Scan(&got, &d); err != nil {
		t.Fatal(err)
	}
	if got != "второй вариант" || d.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("got %q %s", got, d)
	}
}

func TestPendingAndResolve(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	logical := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	voiceID, err := s.SaveVoice(ctx, VoiceMessage{
		ChatID: 1, MessageID: 1, SentAt: time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC),
		LogicalDate: logical, ReferenceDate: logical, DurationSec: 10, Transcript: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveExtraction(ctx, ExtractionInput{VoiceID: voiceID, RefDate: logical, Today: logical,
		Model: "m", PromptVersion: "v", Raw: "raw", Segments: []entry.Segment{
			{DateSource: entry.SourceUnknown, Values: map[string]any{"katya": "скучаю", "done": []string{"зал"}, "water_ml": 1500.0}},
		}})
	if err != nil || saved[0].Date != nil {
		t.Fatalf("err=%v saved=%+v", err, saved)
	}

	pend, err := s.PendingSegments(ctx)
	if err != nil || len(pend) != 1 || pend[0].RefDate.Format("2006-01-02") != "2026-09-05" {
		t.Fatalf("pending: %+v err=%v", pend, err)
	}
	if pend[0].Segment.Values["katya"] != "скучаю" || pend[0].Segment.Values["water_ml"] != 1500.0 {
		t.Fatalf("payload: %+v", pend[0].Segment.Values)
	}

	day := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) // уточнили: это про 03.09 (retro)
	if err := s.ResolveSegment(ctx, saved[0].ID, day); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveSegment(ctx, saved[0].ID, day); !errors.Is(err, ErrNotPending) {
		t.Fatalf("повторное уточнение: %v", err)
	}
	if pend, _ := s.PendingSegments(ctx); len(pend) != 0 {
		t.Fatalf("pending после resolve: %v", pend)
	}

	obs, err := s.DayObservations(ctx, day)
	if err != nil || len(obs) != 3 {
		t.Fatalf("obs=%v err=%v", obs, err)
	}
	for _, o := range obs {
		if o.Src.Relation != "retro" || o.Src.VoiceID != voiceID {
			t.Errorf("источник: %+v", o)
		}
	}
	if other, _ := s.DayObservations(ctx, logical); len(other) != 0 {
		t.Errorf("лишние наблюдения: %v", other)
	}
}

func TestReferenceDateSetsRelation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	sent := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	logical := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	ref := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) // выбран рабочий день 01.09
	voiceID, err := s.SaveVoice(ctx, VoiceMessage{ChatID: 1, MessageID: 1, SentAt: sent,
		LogicalDate: logical, ReferenceDate: ref, DurationSec: 5, Transcript: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveExtraction(ctx, ExtractionInput{VoiceID: voiceID, RefDate: ref, Today: logical, Model: "m", PromptVersion: "v", Raw: "r",
		Segments: []entry.Segment{
			{Date: "2026-09-01", DateSource: entry.SourceRelative, Values: map[string]any{"mood": "ок"}},  // день записи
			{Date: "2026-08-30", DateSource: entry.SourceExplicit, Values: map[string]any{"mood": "так"}}, // другой день
		}})
	if err != nil {
		t.Fatal(err)
	}
	for day, want := range map[time.Time]string{ref: "same_day", time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC): "retro"} {
		obs, err := s.DayObservations(ctx, day)
		if err != nil || len(obs) != 1 || obs[0].Src.Relation != want {
			t.Fatalf("%s: %+v err=%v, want %s", day.Format("2006-01-02"), obs, err, want)
		}
		if !obs[0].Src.Logical.Equal(ref) {
			t.Errorf("Source.Logical должен быть днём записи: %v", obs[0].Src.Logical)
		}
	}
}

func TestEditTranscriptAndReplaceExtraction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	voiceID, err := s.SaveVoice(ctx, VoiceMessage{ChatID: 1, MessageID: 1, SentAt: time.Now(),
		LogicalDate: day, ReferenceDate: day, DurationSec: 5, Transcript: "я встретил кате"})
	if err != nil {
		t.Fatal(err)
	}
	in := ExtractionInput{VoiceID: voiceID, RefDate: day, Today: day, Model: "m", PromptVersion: "v", Raw: "r1",
		Segments: []entry.Segment{{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"diary": "старое", "mood": "старое"}}}}
	if _, err := s.SaveExtraction(ctx, in); err != nil {
		t.Fatal(err)
	}

	v, _ := s.GetVoice(ctx, voiceID)
	if v.Edited || v.Transcript != "я встретил кате" {
		t.Fatalf("до правки: %+v", v)
	}

	if err := s.UpdateTranscript(ctx, voiceID, "я встретил Катю"); err != nil {
		t.Fatal(err)
	}
	v, _ = s.GetVoice(ctx, voiceID)
	if !v.Edited || v.Transcript != "я встретил Катю" {
		t.Fatalf("после правки: %+v", v)
	}
	var old, newText string
	if err := s.pool.QueryRow(ctx, `SELECT old_text, new_text FROM transcript_edits WHERE voice_id=$1`, voiceID).Scan(&old, &newText); err != nil ||
		old != "я встретил кате" || newText != "я встретил Катю" {
		t.Fatalf("история: %q -> %q (%v)", old, newText, err)
	}
	// та же правка повторно историю не плодит
	if err := s.UpdateTranscript(ctx, voiceID, "я встретил Катю"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_edits`).Scan(&n)
	if n != 1 {
		t.Fatalf("записей истории: %d", n)
	}

	// пересборка заменяет прежние извлечение, сегменты и наблюдения
	in.Raw = "r2"
	in.Segments = []entry.Segment{{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"diary": "новое"}}}
	if _, err := s.ReplaceExtraction(ctx, in); err != nil {
		t.Fatal(err)
	}
	obs, _ := s.DayObservations(ctx, day)
	if len(obs) != 1 || obs[0].Value != "новое" {
		t.Fatalf("наблюдения после замены: %+v", obs)
	}
	var ext, seg int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM extractions`).Scan(&ext)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM segments`).Scan(&seg)
	if ext != 1 || seg != 1 {
		t.Fatalf("extractions=%d segments=%d", ext, seg)
	}

	if err := s.UpdateTranscript(ctx, 9999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("несуществующее голосовое: %v", err)
	}
}

func TestVoiceListsAndSettings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	d1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	save := func(msg int64, sent time.Time, logical, ref time.Time, text string) {
		if _, err := s.SaveVoice(ctx, VoiceMessage{ChatID: 1, MessageID: msg, SentAt: sent, LogicalDate: logical,
			ReferenceDate: ref, DurationSec: 1, Transcript: text}); err != nil {
			t.Fatal(err)
		}
	}
	save(1, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), d1, d1, "первое")
	save(2, time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), d2, d1, "второе: отправлено 02.09, но про 01.09")
	save(3, time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC), d2, d2, "третье")

	recent, err := s.RecentVoices(ctx, 2)
	if err != nil || len(recent) != 2 || recent[0].Preview != "третье" || !strings.HasPrefix(recent[1].Preview, "второе") {
		t.Fatalf("recent: %+v err=%v", recent, err)
	}
	for _, v := range recent {
		if v.Transcript != "" {
			t.Error("в списках Transcript не заполняется")
		}
	}
	day1, err := s.VoicesForDay(ctx, d1, 10) // первое (отправлено и отнесено) + второе (отнесено)
	if err != nil || len(day1) != 2 {
		t.Fatalf("day1: %+v err=%v", day1, err)
	}

	if _, ok, err := s.Setting(ctx, "k"); ok || err != nil {
		t.Fatalf("пустая настройка: ok=%v err=%v", ok, err)
	}
	if err := s.SetSetting(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "v2"); err != nil { // upsert
		t.Fatal(err)
	}
	if v, ok, _ := s.Setting(ctx, "k"); !ok || v != "v2" {
		t.Fatalf("setting: %q %v", v, ok)
	}
	if err := s.DeleteSetting(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Setting(ctx, "k"); ok {
		t.Fatal("настройка не удалилась")
	}
}

func TestCategoriesSeedAndCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	cats, err := s.Categories(ctx)
	if err != nil || len(cats) != len(entry.DefaultCategories()) {
		t.Fatalf("seed: %d categories, err=%v", len(cats), err)
	}
	keys := map[string]bool{}
	for _, c := range cats {
		keys[c.Key] = true
	}
	for _, want := range []string{"hobby", "pet_project", "for_psychologist", "diary"} {
		if !keys[want] {
			t.Errorf("нет категории %s", want)
		}
	}

	// повторная миграция не затирает правки пользователя и не возвращает скрытые категории
	title := "Моё настроение"
	if err := s.UpdateCategory(ctx, "mood", CategoryUpdate{Title: &title}); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := s.UpdateCategory(ctx, "tea", CategoryUpdate{Active: &off}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	schema, _ := s.Schema(ctx)
	if c, _ := schema.Lookup("mood"); c.Title != "Моё настроение" {
		t.Errorf("правка затёрта: %+v", c)
	}
	if c, _ := schema.Lookup("tea"); c.Active {
		t.Error("скрытая категория снова активна")
	}

	// добавление: в конец списка, дубль ключа отклоняется
	added, err := s.AddCategory(ctx, entry.Category{Key: "running", Title: "Бег", Kind: entry.Number, Unit: "км", Hint: "сколько пробежал"})
	if err != nil || !added.Active {
		t.Fatalf("add: %+v %v", added, err)
	}
	schema, _ = s.Schema(ctx)
	if last := schema.All[len(schema.All)-1]; last.Key != "running" || last.Kind != entry.Number || last.Unit != "км" {
		t.Errorf("добавленная категория: %+v", last)
	}
	if _, err := s.AddCategory(ctx, entry.Category{Key: "running", Title: "Другой", Kind: entry.Text}); !errors.Is(err, ErrCategoryExists) {
		t.Errorf("дубль ключа: %v", err)
	}

	// группа и подсказка
	group, hint := "Спорт", ""
	if err := s.UpdateCategory(ctx, "running", CategoryUpdate{Group: &group, Hint: &hint}); err != nil {
		t.Fatal(err)
	}
	schema, _ = s.Schema(ctx)
	if c, _ := schema.Lookup("running"); c.Group != "Спорт" || c.Hint != "" {
		t.Errorf("group/hint: %+v", c)
	}

	// diary скрыть нельзя; несуществующую — ErrNotFound
	if err := s.UpdateCategory(ctx, "diary", CategoryUpdate{Active: &off}); !errors.Is(err, ErrProtected) {
		t.Errorf("diary: %v", err)
	}
	if err := s.UpdateCategory(ctx, "нет", CategoryUpdate{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("несуществующая: %v", err)
	}
}

func TestMoveCategory(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	order := func() []string {
		schema, _ := s.Schema(ctx)
		var keys []string
		for _, c := range schema.Active() {
			keys = append(keys, c.Key)
		}
		return keys
	}
	before := order()
	if before[0] != "diary" || before[1] != "mood" {
		t.Fatalf("исходный порядок: %v", before[:3])
	}
	if err := s.MoveCategory(ctx, "mood", -1); err != nil { // mood выше diary
		t.Fatal(err)
	}
	after := order()
	if after[0] != "mood" || after[1] != "diary" || len(after) != len(before) {
		t.Fatalf("после сдвига вверх: %v", after[:3])
	}
	if err := s.MoveCategory(ctx, "mood", -1); err != nil { // уже первая — без изменений
		t.Fatal(err)
	}
	if order()[0] != "mood" {
		t.Error("на краю порядок не должен меняться")
	}
	if err := s.MoveCategory(ctx, "mood", +1); err != nil {
		t.Fatal(err)
	}
	if order()[0] != "diary" {
		t.Error("сдвиг вниз вернул порядок")
	}
	if err := s.MoveCategory(ctx, "нет", +1); !errors.Is(err, ErrNotFound) {
		t.Errorf("несуществующая: %v", err)
	}
	// скрытые категории в перестановке не участвуют
	off := false
	_ = s.UpdateCategory(ctx, "food", CategoryUpdate{Active: &off})
	if err := s.MoveCategory(ctx, "stool", -1); err != nil { // над stool была food (скрыта) — меняемся с mood
		t.Fatal(err)
	}
	if got := order(); got[1] != "stool" || got[2] != "mood" {
		t.Errorf("порядок с учётом скрытых: %v", got[:4])
	}
}

func TestCustomCategoryObservationsAndHidden(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.AddCategory(ctx, entry.Category{Key: "running", Title: "Бег", Kind: entry.Number, Unit: "км"}); err != nil {
		t.Fatal(err)
	}
	voiceID, _ := s.SaveVoice(ctx, VoiceMessage{ChatID: 1, MessageID: 1, SentAt: time.Now(), LogicalDate: day, ReferenceDate: day,
		DurationSec: 1, Transcript: "t", Confirmed: true})
	_, err := s.SaveExtraction(ctx, ExtractionInput{VoiceID: voiceID, RefDate: day, Today: day, Model: "m", PromptVersion: "v", Raw: "r",
		Segments: []entry.Segment{{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"running": 5.5, "mood": "ок"}}}})
	if err != nil {
		t.Fatal(err)
	}
	obs, _ := s.DayObservations(ctx, day)
	if len(obs) != 2 {
		t.Fatalf("наблюдения: %+v", obs)
	}
	// после скрытия данные остаются в БД и читаются (в карточку их не пустит схема)
	off := false
	_ = s.UpdateCategory(ctx, "running", CategoryUpdate{Active: &off})
	obs, _ = s.DayObservations(ctx, day)
	if len(obs) != 2 {
		t.Fatalf("данные скрытой категории потеряны: %+v", obs)
	}
	schema, _ := s.Schema(ctx)
	if c, _ := schema.Lookup("running"); c.Active {
		t.Error("категория должна быть скрыта")
	}
}

func TestConfirmationAndSource(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	save := func(msg int64, source string, confirmed bool) int64 {
		id, err := s.SaveVoice(ctx, VoiceMessage{ChatID: 1, MessageID: msg, SentAt: time.Date(2026, 9, 1, 10, int(msg), 0, 0, time.UTC),
			LogicalDate: day, ReferenceDate: day, DurationSec: 1, Transcript: "текст", Source: source, Confirmed: confirmed})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	v1 := save(1, "voice", false)
	v2 := save(2, "voice", false)
	_ = save(3, "text", true)

	un, err := s.UnconfirmedVoices(ctx, 10)
	if err != nil || len(un) != 2 || un[0].ID != v2 || un[0].Source != "voice" || un[0].Confirmed {
		t.Fatalf("unconfirmed: %+v err=%v", un, err)
	}
	if n, _ := s.CountUnconfirmed(ctx); n != 2 {
		t.Fatalf("count=%d", n)
	}

	if err := s.ConfirmVoice(ctx, v1); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountUnconfirmed(ctx); n != 1 {
		t.Fatalf("после подтверждения count=%d", n)
	}
	// правка расшифровки тоже подтверждает
	if err := s.UpdateTranscript(ctx, v2, "исправленный текст"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountUnconfirmed(ctx); n != 0 {
		t.Fatalf("после правки count=%d", n)
	}
	if err := s.ConfirmVoice(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("подтверждение несуществующей: %v", err)
	}

	v, _ := s.GetVoice(ctx, v2)
	if v.Source != "voice" || !v.Confirmed || !v.Edited {
		t.Errorf("voice: %+v", v)
	}
	recent, _ := s.RecentVoices(ctx, 1)
	if recent[0].Source != "text" {
		t.Errorf("источник текстовой записи: %+v", recent[0])
	}

	// Source.Confirmed доходит до карточки дня
	_, _ = s.SaveExtraction(ctx, ExtractionInput{VoiceID: save(4, "voice", false), RefDate: day, Today: day, Model: "m", PromptVersion: "v", Raw: "r",
		Segments: []entry.Segment{{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"mood": "ок"}}}})
	obs, _ := s.DayObservations(ctx, day)
	if len(obs) != 1 || obs[0].Src.Confirmed {
		t.Errorf("Confirmed в наблюдении: %+v", obs)
	}
}

func TestDeleteVoiceCascades(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	mk := func(msg int64, text string, segs ...entry.Segment) int64 {
		id, err := s.SaveVoice(ctx, VoiceMessage{ChatID: 1, MessageID: msg, SentAt: time.Date(2026, 9, 1, 10, int(msg), 0, 0, time.UTC),
			LogicalDate: day, ReferenceDate: day, DurationSec: 1, Transcript: text, Confirmed: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveExtraction(ctx, ExtractionInput{VoiceID: id, RefDate: day, Today: day2, Model: "m", PromptVersion: "v", Raw: "r", Segments: segs}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := mk(1, "удаляемая",
		entry.Segment{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"mood": "A", "food": "A"}},
		entry.Segment{Date: "2026-09-02", DateSource: entry.SourceExplicit, Values: map[string]any{"mood": "A2"}},
		entry.Segment{DateSource: entry.SourceUnknown, Values: map[string]any{"katya": "без даты"}})
	b := mk(2, "остающаяся",
		entry.Segment{Date: "2026-09-01", DateSource: entry.SourceExplicit, Values: map[string]any{"mood": "B"}})
	if err := s.UpdateTranscript(ctx, a, "удаляемая (правка)"); err != nil {
		t.Fatal(err)
	}

	imp, err := s.VoiceImpact(ctx, a)
	if err != nil || imp.Segments != 3 || imp.Observations != 3 || imp.Edits != 1 || len(imp.Days) != 2 ||
		imp.Days[0].Format("2006-01-02") != "2026-09-01" || imp.Days[1].Format("2006-01-02") != "2026-09-02" {
		t.Fatalf("impact: %+v err=%v", imp, err)
	}

	deleted, err := s.DeleteVoice(ctx, a)
	if err != nil || deleted.Observations != 3 || deleted.Segments != 3 {
		t.Fatalf("delete: %+v err=%v", deleted, err)
	}

	count := func(table string) int {
		var n int
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// всё, что связано с удалённой записью, исчезло; запись b цела
	for table, want := range map[string]int{"voice_messages": 1, "extractions": 1, "segments": 1, "observations": 1, "transcript_edits": 0} {
		if got := count(table); got != want {
			t.Errorf("%s: %d, want %d", table, got, want)
		}
	}
	obs, _ := s.DayObservations(ctx, day)
	if len(obs) != 1 || obs[0].Value != "B" || obs[0].Src.VoiceID != b {
		t.Fatalf("карточка дня после удаления: %+v", obs)
	}
	if obs2, _ := s.DayObservations(ctx, day2); len(obs2) != 0 {
		t.Fatalf("данные удалённой записи за другой день остались: %+v", obs2)
	}
	if pend, _ := s.PendingSegments(ctx); len(pend) != 0 {
		t.Fatalf("сегмент без даты удалённой записи остался в очереди: %+v", pend)
	}

	// повторное удаление и несуществующая запись
	if _, err := s.DeleteVoice(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("повторное удаление: %v", err)
	}
	if _, err := s.VoiceImpact(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("impact несуществующей: %v", err)
	}
}
