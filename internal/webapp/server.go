// Package webapp — Telegram Mini App: календарь дней и карточка выбранного дня (только чтение).
// HTTP-сервер отдаёт вшитую статику и JSON API; каждый запрос к API подписан Telegram
// (initData), чужие и неподписанные запросы отклоняются.
package webapp

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"diary/internal/diaryday"
	"diary/internal/entry"
	"diary/internal/store"
)

//go:embed static
var staticFiles embed.FS

// initDataMaxAge — сколько живёт подпись initData. Telegram выдаёт её при открытии Mini App;
// если приложение открыто дольше, его нужно переоткрыть.
const initDataMaxAge = 24 * time.Hour

// Store — данные, которые читает веб-интерфейс.
type Store interface {
	Schema(ctx context.Context) (entry.Schema, error)
	DayObservations(ctx context.Context, date time.Time) ([]entry.Observation, error)
	DayVoices(ctx context.Context, date time.Time) ([]store.Voice, error)
	MonthDays(ctx context.Context, from, to time.Time) ([]store.DaySummary, error)
	PendingSegments(ctx context.Context) ([]store.PendingSegment, error)
	CountUnconfirmed(ctx context.Context) (int, error)
}

type Server struct {
	Log           *slog.Logger
	Store         Store
	Clock         diaryday.Clock
	BotToken      string
	AllowedUserID int64
	now           func() time.Time // для тестов
}

// Handler — все маршруты: статика на «/», API на «/api/».
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }) //nolint:errcheck
	mux.Handle("GET /api/month", s.auth(s.month))
	mux.Handle("GET /api/day", s.auth(s.day))
	return s.logged(secure(mux))
}

// Run слушает addr, пока не отменён ctx.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown) //nolint:errcheck
	}()
	s.Log.Info("webapp listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// secure ставит заголовки безопасности. Страница ничего не грузит, кроме своих файлов и
// скрипта Telegram; данные не кэшируются ни браузером, ни прокси.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self' https://telegram.org; "+
			"style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; "+
			"frame-ancestors https://web.telegram.org")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// logged пишет в лог метод, путь, статус и время — без параметров и содержимого.
func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}

// auth пропускает только запросы с верной подписью Telegram от владельца бота.
// Заголовок: «Authorization: tma <initData>».
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initData, _ := strings.CutPrefix(r.Header.Get("Authorization"), "tma ")
		id, err := ValidateInitData(initData, s.BotToken, initDataMaxAge, s.clock())
		if err != nil {
			s.Log.Warn("webapp auth", "err", err, "remote", r.RemoteAddr)
			writeError(w, http.StatusUnauthorized, "Откройте календарь заново через бота в Telegram.")
			return
		}
		if id != s.AllowedUserID {
			s.Log.Warn("webapp: чужой пользователь", "user_id", id)
			writeError(w, http.StatusForbidden, "Нет доступа.")
			return
		}
		next(w, r)
	})
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

type monthResponse struct {
	Month       string     `json:"month"` // ГГГГ-ММ
	Today       string     `json:"today"` // текущий день дневника, ГГГГ-ММ-ДД
	Days        []monthDay `json:"days"`
	Pending     int        `json:"pending"`     // записей без даты (во всей базе)
	Unconfirmed int        `json:"unconfirmed"` // неподтверждённых расшифровок (во всей базе)
}

type monthDay struct {
	Date        string `json:"date"`
	Entries     int    `json:"entries"`
	Unconfirmed int    `json:"unconfirmed"`
}

// month — дни месяца с записями: GET /api/month?m=2026-09 (без m — текущий месяц).
func (s *Server) month(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	today := s.Clock.LogicalDate(s.clock())
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if m := r.URL.Query().Get("m"); m != "" {
		t, err := time.Parse("2006-01", m)
		if err != nil || t.Year() < 2000 || t.Year() > 2100 {
			writeError(w, http.StatusBadRequest, "Месяц в формате ГГГГ-ММ.")
			return
		}
		first = t
	}
	last := first.AddDate(0, 1, -1)

	days, err := s.Store.MonthDays(ctx, first, last)
	if err != nil {
		s.fail(w, "month days", err)
		return
	}
	pending, err := s.Store.PendingSegments(ctx)
	if err != nil {
		s.fail(w, "pending segments", err)
		return
	}
	unconfirmed, err := s.Store.CountUnconfirmed(ctx)
	if err != nil {
		s.fail(w, "count unconfirmed", err)
		return
	}

	resp := monthResponse{
		Month: first.Format("2006-01"), Today: today.Format("2006-01-02"),
		Days: []monthDay{}, Pending: len(pending), Unconfirmed: unconfirmed,
	}
	for _, d := range days {
		resp.Days = append(resp.Days, monthDay{Date: d.Date.Format("2006-01-02"), Entries: d.Entries, Unconfirmed: d.Unconfirmed})
	}
	writeJSON(w, resp)
}

type dayResponse struct {
	Date   string     `json:"date"`
	Title  string     `json:"title"` // «5 сентября 2026, суббота»
	Card   entry.View `json:"card"`
	Voices []dayVoice `json:"voices"`
}

type dayVoice struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"` // «Голосовое · 05.09 21:40 · 1:23» / «Текст · 05.09 21:40»
	Confirmed bool   `json:"confirmed"`
	Edited    bool   `json:"edited"`
	Text      string `json:"text"`
}

// day — карточка дня и расшифровки записей, из которых она собрана: GET /api/day?d=2026-09-05.
func (s *Server) day(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	date, err := time.Parse("2006-01-02", r.URL.Query().Get("d"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Дата в формате ГГГГ-ММ-ДД.")
		return
	}
	obs, err := s.Store.DayObservations(ctx, date)
	if err != nil {
		s.fail(w, "day observations", err)
		return
	}
	if len(obs) == 0 {
		writeError(w, http.StatusNotFound, "За этот день записей нет.")
		return
	}
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		s.fail(w, "schema", err)
		return
	}
	voices, err := s.Store.DayVoices(ctx, date)
	if err != nil {
		s.fail(w, "day voices", err)
		return
	}

	resp := dayResponse{
		Date:   date.Format("2006-01-02"),
		Title:  dayTitle(date),
		Card:   entry.BuildCard(date, s.Clock.Loc, obs).View(schema),
		Voices: []dayVoice{},
	}
	for _, v := range voices {
		resp.Voices = append(resp.Voices, dayVoice{
			ID: v.ID, Label: s.voiceLabel(v), Confirmed: v.Confirmed, Edited: v.Edited, Text: v.Transcript,
		})
	}
	writeJSON(w, resp)
}

func (s *Server) voiceLabel(v store.Voice) string {
	at := v.SentAt.In(s.Clock.Loc).Format("02.01 15:04")
	if v.Source == "text" {
		return "Текст · " + at
	}
	return fmt.Sprintf("Голосовое · %s · %d:%02d", at, v.DurationSec/60, v.DurationSec%60)
}

var (
	monthsGen = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	weekdays  = [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}
)

func dayTitle(d time.Time) string {
	return fmt.Sprintf("%d %s %d, %s", d.Day(), monthsGen[d.Month()-1], d.Year(), weekdays[d.Weekday()])
}

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.Log.Error("webapp "+what, "err", err)
	writeError(w, http.StatusInternalServerError, "Не удалось получить данные.")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
