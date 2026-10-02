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
