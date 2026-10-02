package webapp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"diary/internal/diaryday"
	"diary/internal/entry"
	"diary/internal/store"
)

type fakeStore struct {
	from, to time.Time
}

func (f *fakeStore) Schema(context.Context) (entry.Schema, error) {
	return entry.NewSchema(entry.DefaultCategories()), nil
}

func (f *fakeStore) DayObservations(_ context.Context, d time.Time) ([]entry.Observation, error) {
	if d.Day() != 5 {
		return nil, nil
	}
	src := entry.Source{VoiceID: 1, SentAt: time.Date(2026, 9, 5, 18, 40, 0, 0, time.UTC), Logical: d, Relation: "same_day", Confirmed: false}
	return []entry.Observation{
		{Field: "diary", Value: "Сходил в зал.", Src: src},
		{Field: "mood", Value: "бодро", Src: src},
		{Field: "sweets", Value: false, Src: src},
	}, nil
}

func (f *fakeStore) DayVoices(context.Context, time.Time) ([]store.Voice, error) {
	return []store.Voice{{ID: 1, SentAt: time.Date(2026, 9, 5, 18, 40, 0, 0, time.UTC), DurationSec: 83,
		Source: "voice", Transcript: "Сходил в зал, настроение бодрое, сладкого не ел."}}, nil
}

func (f *fakeStore) MonthDays(_ context.Context, from, to time.Time) ([]store.DaySummary, error) {
	f.from, f.to = from, to
	return []store.DaySummary{{Date: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Entries: 1, Unconfirmed: 1}}, nil
}

func (f *fakeStore) PendingSegments(context.Context) ([]store.PendingSegment, error) {
	return []store.PendingSegment{{ID: 7}}, nil
}

func (f *fakeStore) CountUnconfirmed(context.Context) (int, error) { return 1, nil }

func testServer(t *testing.T) (*Server, *fakeStore, time.Time) {
	t.Helper()
	clock, err := diaryday.NewClock("Europe/Moscow", 5)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{}
	return &Server{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: fs, Clock: clock,
		BotToken: testToken, AllowedUserID: 42, now: func() time.Time { return now },
	}, fs, now
}

func get(t *testing.T, h http.Handler, path, initData string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if initData != "" {
		req.Header.Set("Authorization", "tma "+initData)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIAuth(t *testing.T) {
	s, _, now := testServer(t)
	h := s.Handler()
	if rec := get(t, h, "/api/month", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("без подписи: %d", rec.Code)
	}
	if rec := get(t, h, "/api/month", signedInitData("999:other", 42, now)); rec.Code != http.StatusUnauthorized {
		t.Errorf("чужой токен: %d", rec.Code)
	}
	if rec := get(t, h, "/api/day?d=2026-09-05", signedInitData(testToken, 43, now)); rec.Code != http.StatusForbidden {
		t.Errorf("чужой пользователь: %d", rec.Code)
	}
	rec := get(t, h, "/api/month", signedInitData(testToken, 42, now))
	if rec.Code != http.StatusOK {
		t.Fatalf("владелец: %d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control=%q", cc)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self' https://telegram.org") {
		t.Errorf("CSP=%q", csp)
	}
}

func TestAPIMonth(t *testing.T) {
	s, fs, now := testServer(t)
	h := s.Handler()
	auth := signedInitData(testToken, 42, now)

	rec := get(t, h, "/api/month", auth)
	var m monthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Month != "2026-10" || m.Today != "2026-10-02" || m.Pending != 1 || m.Unconfirmed != 1 ||
		len(m.Days) != 1 || m.Days[0] != (monthDay{Date: "2026-09-05", Entries: 1, Unconfirmed: 1}) {
		t.Errorf("month: %+v", m)
	}

	get(t, h, "/api/month?m=2026-02", auth)
	if fs.from.Format("2006-01-02") != "2026-02-01" || fs.to.Format("2006-01-02") != "2026-02-28" {
		t.Errorf("границы месяца: %v — %v", fs.from, fs.to)
	}
	for _, bad := range []string{"2026-13", "26-02", "1999-01", "x"} {
		if rec := get(t, h, "/api/month?m="+bad, auth); rec.Code != http.StatusBadRequest {
			t.Errorf("m=%s: %d", bad, rec.Code)
		}
	}
}

func TestAPIDay(t *testing.T) {
	s, _, now := testServer(t)
	h := s.Handler()
	auth := signedInitData(testToken, 42, now)

	rec := get(t, h, "/api/day?d=2026-09-05", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("day: %d %s", rec.Code, rec.Body)
	}
	var d dayResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Title != "5 сентября 2026, суббота" || !d.Card.Unconfirmed || len(d.Voices) != 1 ||
		d.Voices[0].Label != "Голосовое · 05.09 21:40 · 1:23" || !strings.HasPrefix(d.Voices[0].Text, "Сходил") {
		t.Errorf("day: %+v", d)
	}
	found := map[string]bool{}
	for _, sec := range d.Card.Sections {
		for _, it := range sec.Items {
			if it.Mentioned() {
				found[it.Key] = true
			}
		}
	}
	if !found["diary"] || !found["mood"] || !found["sweets"] || found["food"] {
		t.Errorf("упомянутые категории: %v", found)
	}

	if rec := get(t, h, "/api/day?d=2026-09-06", auth); rec.Code != http.StatusNotFound {
		t.Errorf("пустой день: %d", rec.Code)
	}
	if rec := get(t, h, "/api/day?d=05.09", auth); rec.Code != http.StatusBadRequest {
		t.Errorf("плохая дата: %d", rec.Code)
	}
}

func TestStatic(t *testing.T) {
	s, _, _ := testServer(t)
	h := s.Handler()
	for _, p := range []string{"/", "/app.js", "/style.css"} {
		if rec := get(t, h, p, ""); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}
