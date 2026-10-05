package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"diary/internal/diaryday"
	"diary/internal/entry"
	"diary/internal/store"
)

// fakeAnalyzer заменяет модель: часть записи о дне записи и часть без даты; fail — модель не справилась.
type fakeAnalyzer struct {
	st   *store.Store
	mu   sync.Mutex
	fail bool
	runs []string // «текст/replace»
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, id int64, ref, today time.Time, text string, replace bool) ([]store.SavedSegment, error) {
	f.mu.Lock()
	f.runs = append(f.runs, text+"/"+strconv.FormatBool(replace))
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return nil, errors.New("модель недоступна")
	}
	in := store.ExtractionInput{VoiceID: id, RefDate: ref, Today: today, Model: "m", PromptVersion: "v", Raw: "r", Segments: []entry.Segment{
		{Date: ref.Format("2006-01-02"), DateSource: entry.SourceExplicit, Values: map[string]any{"diary": text, "mood": "ок"}},
		{DateSource: entry.SourceUnknown, Values: map[string]any{"katya": "скучаю"}},
	}}
	if replace {
		return f.st.ReplaceExtraction(ctx, in)
	}
	return f.st.SaveExtraction(ctx, in)
}

type harness struct {
	t     *testing.T
	s     *Server
	h     http.Handler
	an    *fakeAnalyzer
	auth  string
	mu    sync.Mutex
	notes []string
}

// newHarness — сервер на настоящем Postgres (TEST_DATABASE_URL, см. make test) с поддельной моделью.
func newHarness(t *testing.T) *harness {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL не задан")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock, _ := diaryday.NewClock("Europe/Moscow", 5)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	hs := &harness{t: t, an: &fakeAnalyzer{st: st}, auth: signedInitData(testToken, 42, now)}
	hs.s = &Server{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: st, Analyzer: hs.an, Clock: clock,
		BotToken: testToken, AllowedUserID: 42, now: func() time.Time { return now },
		Notify: func(_ context.Context, text string) { hs.mu.Lock(); hs.notes = append(hs.notes, text); hs.mu.Unlock() },
	}
	hs.h = hs.s.Handler()
	return hs
}

// do выполняет запрос и разбирает JSON-ответ в out (если не nil).
func (hs *harness) do(method, path, body string, out any) int {
	hs.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "tma "+hs.auth)
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			hs.t.Fatalf("%s %s: %v: %s", method, path, err, rec.Body)
		}
	}
	return rec.Code
}

func (hs *harness) wait(id int64) entryStatusResponse {
	hs.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hs.s.jobs.running(id) {
		if time.Now().After(deadline) {
			hs.t.Fatal("разбор не закончился")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var st entryStatusResponse
	if code := hs.do("GET", "/api/entries/"+strconv.FormatInt(id, 10), "", &st); code != 200 {
		hs.t.Fatalf("status: %d", code)
	}
	return st
}

func (hs *harness) lastNote() string {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if len(hs.notes) == 0 {
		return ""
	}
	return hs.notes[len(hs.notes)-1]
}

func TestEntryLifecycle(t *testing.T) {
	hs := newHarness(t)

	// добавление на прошедший день
	var created struct{ ID int64 }
	if code := hs.do("POST", "/api/entries", `{"date":"2026-09-05","text":"  Сходил в зал.  "}`, &created); code != http.StatusAccepted {
		t.Fatalf("add: %d", code)
	}
	id := strconv.FormatInt(created.ID, 10)
	st := hs.wait(created.ID)
	if st.State != stateDone || st.RefDate != "2026-09-05" || len(st.Days) != 1 || st.Days[0] != "2026-09-05" ||
		len(st.Pending) != 1 || st.Pending[0].Preview != "скучаю" || st.Pending[0].Entry != "Запись на 05.09.2026 (15:00)" {
		t.Fatalf("status: %+v", st)
	}
	if n := hs.lastNote(); !strings.Contains(n, "добавлена запись на 05.09.2026") || !strings.Contains(n, "Частей без даты: 1") {
		t.Errorf("уведомление: %q", n)
	}

	// день виден в календаре, карточка собрана, текст обрезан по краям
	var m monthResponse
	hs.do("GET", "/api/month?m=2026-09", "", &m)
	if len(m.Days) != 1 || m.Days[0].Date != "2026-09-05" || len(m.Pending) != 1 {
		t.Fatalf("month: %+v", m)
	}
	var d dayResponse
	if code := hs.do("GET", "/api/day?d=2026-09-05", "", &d); code != 200 || len(d.Voices) != 1 ||
		d.Voices[0].Text != "Сходил в зал." || d.Voices[0].Source != "text" || !d.Voices[0].Confirmed || d.Voices[0].RefDate != "2026-09-05" {
		t.Fatalf("day: %d %+v", code, d)
	}
	if v := d.Card.Sections[0].Items[0]; len(v.Blocks) != 1 || v.Blocks[0].Voice != created.ID {
		t.Errorf("в карточке нет ссылки на запись: %+v", v)
	}

	// день для части без даты
	seg := strconv.FormatInt(m.Pending[0].ID, 10)
	if code := hs.do("POST", "/api/segments/"+seg+"/date", `{"date":"2026-09-04"}`, nil); code != 200 {
		t.Fatalf("resolve: %d", code)
	}
	if code := hs.do("POST", "/api/segments/"+seg+"/date", `{"date":"2026-09-04"}`, nil); code != 404 {
		t.Errorf("повторное уточнение: %d", code)
	}
	if n := hs.lastNote(); !strings.Contains(n, "назначен день 04.09.2026") {
		t.Errorf("уведомление: %q", n)
	}
	hs.do("GET", "/api/month?m=2026-09", "", &m)
	if len(m.Days) != 2 || len(m.Pending) != 0 {
		t.Fatalf("после уточнения: %+v", m)
	}

	// правка текста: история, подтверждение, пересборка с заменой
	if code := hs.do("PUT", "/api/entries/"+id+"/text", `{"text":"Сходил в бассейн."}`, nil); code != http.StatusAccepted {
		t.Fatalf("edit: %d", code)
	}
	if st := hs.wait(created.ID); st.State != stateDone || len(st.Pending) != 1 {
		t.Fatalf("после правки: %+v", st)
	}
	hs.do("GET", "/api/day?d=2026-09-05", "", &d)
	if d.Voices[0].Text != "Сходил в бассейн." || !d.Voices[0].Edited {
		t.Errorf("после правки: %+v", d.Voices[0])
	}
	// тот же текст — не пересобирается
	var res map[string]bool
	if code := hs.do("PUT", "/api/entries/"+id+"/text", `{"text":"Сходил в бассейн."}`, &res); code != 200 || res["changed"] {
		t.Errorf("тот же текст: %d %v", code, res)
	}

	// пересборка и подтверждение
	if code := hs.do("POST", "/api/entries/"+id+"/reanalyze", "", nil); code != http.StatusAccepted {
		t.Fatalf("reanalyze: %d", code)
	}
	hs.wait(created.ID)
	if code := hs.do("POST", "/api/entries/"+id+"/confirm", "", nil); code != 200 {
		t.Errorf("confirm: %d", code)
	}
	hs.an.mu.Lock()
	runs := strings.Join(hs.an.runs, " | ")
	hs.an.mu.Unlock()
	if runs != "Сходил в зал./false | Сходил в бассейн./true | Сходил в бассейн./true" {
		t.Errorf("разборы: %s", runs)
	}

	// удаление
	var imp impactResponse
	if code := hs.do("GET", "/api/entries/"+id+"/impact", "", &imp); code != 200 || imp.Edits != 1 || imp.Observations == 0 || len(imp.Days) != 1 {
		t.Fatalf("impact: %d %+v", code, imp)
	}
	if code := hs.do("DELETE", "/api/entries/"+id, "", nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := hs.do("GET", "/api/entries/"+id, "", nil); code != 404 {
		t.Errorf("после удаления: %d", code)
	}
	if code := hs.do("GET", "/api/day?d=2026-09-05", "", nil); code != 404 {
		t.Errorf("день после удаления: %d", code)
	}
	if n := hs.lastNote(); !strings.Contains(n, "удалена запись на 05.09.2026") {
		t.Errorf("уведомление: %q", n)
	}
}

func TestEntryAnalyzeFails(t *testing.T) {
	hs := newHarness(t)
	hs.an.fail = true
	var a, b struct{ ID int64 }
	// две записи из Mini App подряд: без сообщения Telegram, но не конфликтуют
	hs.do("POST", "/api/entries", `{"date":"2026-10-02","text":"первая"}`, &a)
	hs.do("POST", "/api/entries", `{"date":"2026-10-02","text":"вторая"}`, &b)
	if a.ID == 0 || b.ID == 0 || a.ID == b.ID {
		t.Fatalf("ids: %d %d", a.ID, b.ID)
	}
	if st := hs.wait(a.ID); st.State != stateFailed || st.Error == "" {
		t.Fatalf("status: %+v", st)
	}
	hs.wait(b.ID)
	if n := hs.lastNote(); !strings.Contains(n, "разобрать её не удалось") {
		t.Errorf("уведомление: %q", n)
	}
	var m monthResponse
	hs.do("GET", "/api/month", "", &m)
	if len(m.Unanalyzed) != 2 || len(m.Days) != 0 {
		t.Fatalf("month: %+v", m)
	}

	// повтор разбора после восстановления модели
	hs.an.mu.Lock()
	hs.an.fail = false
	hs.an.mu.Unlock()
	hs.do("POST", "/api/entries/"+strconv.FormatInt(a.ID, 10)+"/reanalyze", "", nil)
	if st := hs.wait(a.ID); st.State != stateDone {
		t.Fatalf("повтор: %+v", st)
	}
	hs.do("GET", "/api/month", "", &m)
	if len(m.Unanalyzed) != 1 || len(m.Days) != 1 {
		t.Fatalf("после повтора: %+v", m)
	}
}

func TestEntryValidation(t *testing.T) {
	hs := newHarness(t)
	cases := []struct{ body, want string }{
		{`{"date":"2026-10-03","text":"x"}`, "будущем"},
		{`{"date":"2024-10-01","text":"x"}`, "два года"},
		{`{"date":"05.09","text":"x"}`, "ГГГГ-ММ-ДД"},
		{`{"date":"2026-10-02","text":"   "}`, "пустая"},
		{`{"date":"2026-10-02","text":"` + strings.Repeat("я", maxEntryRunes+1) + `"}`, "длинная"},
		{`{"date":"2026-10-02","text":"x","extra":1}`, "Некорректный"},
		{`не json`, "Некорректный"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/api/entries", strings.NewReader(c.body))
		req.Header.Set("Authorization", "tma "+hs.auth)
		rec := httptest.NewRecorder()
		hs.h.ServeHTTP(rec, req)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%.40s: %d %s", c.body, rec.Code, rec.Body)
		}
	}
	// граница: ровно два года назад и сегодня — можно
	var created struct{ ID int64 }
	if code := hs.do("POST", "/api/entries", `{"date":"2024-10-02","text":"давно"}`, &created); code != http.StatusAccepted {
		t.Errorf("2 года назад: %d", code)
	}
	hs.wait(created.ID)
	for _, p := range []string{"/api/entries/999", "/api/entries/abc", "/api/entries/999/impact"} {
		if code := hs.do("GET", p, "", nil); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
	if code := hs.do("POST", "/api/segments/999/date", `{"date":"2026-10-01"}`, nil); code != 404 {
		t.Errorf("несуществующая часть: %d", code)
	}
	// без подписи писать нельзя
	req := httptest.NewRequest("POST", "/api/entries", strings.NewReader(`{"date":"2026-10-02","text":"x"}`))
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("без подписи: %d", rec.Code)
	}
}
