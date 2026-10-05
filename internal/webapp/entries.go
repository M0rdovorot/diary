package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"diary/internal/entry"
	"diary/internal/store"
)

const (
	// maxEntryRunes — предел длины текста записи из Mini App (в чате предел задаёт Telegram — 4096).
	maxEntryRunes = 10000
	// analyzeTimeout — сколько ждать фоновый разбор записи моделью (у самого разбора лимит 3 минуты).
	analyzeTimeout = 5 * time.Minute
	// maxPastYears — насколько далеко в прошлое можно отнести запись (как у дат, которые называет модель).
	maxPastYears = 2
)

// Состояние фонового разбора записи.
const (
	stateIdle    = "idle"    // разбора в этом запуске бота не было
	stateRunning = "running" // идёт
	stateDone    = "done"
	stateFailed  = "failed"
)

type job struct {
	state    string
	err      string
	finished time.Time
}

// jobs — фоновые разборы записей. Живут в памяти: после перезапуска бота состояние idle, а запись
// без разбора видна в списке «не разобраны» с кнопкой повторить.
type jobs struct {
	mu sync.Mutex
	m  map[int64]*job
}

// start запускает fn в фоне для записи id; false — по этой записи разбор уже идёт.
func (j *jobs) start(id int64, fn func(ctx context.Context) error) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.m == nil {
		j.m = map[int64]*job{}
	}
	if cur, ok := j.m[id]; ok && cur.state == stateRunning {
		return false
	}
	for k, v := range j.m { // завершённые давно никому не нужны
		if v.state != stateRunning && time.Since(v.finished) > time.Hour {
			delete(j.m, k)
		}
	}
	jb := &job{state: stateRunning}
	j.m[id] = jb
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), analyzeTimeout)
		defer cancel()
		err := fn(ctx)
		j.mu.Lock()
		defer j.mu.Unlock()
		jb.state, jb.finished = stateDone, time.Now()
		if err != nil {
			jb.state, jb.err = stateFailed, err.Error()
		}
	}()
	return true
}

func (j *jobs) get(id int64) (state, errText string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if jb, ok := j.m[id]; ok {
		return jb.state, jb.err
	}
	return stateIdle, ""
}

func (j *jobs) running(id int64) bool {
	st, _ := j.get(id)
	return st == stateRunning
}

// ---------- добавление ----------

type addRequest struct {
	Date string `json:"date"`
	Text string `json:"text"`
}

// addEntry — новая текстовая запись на выбранный день: POST /api/entries {date, text}.
// Текст сохраняется сразу, разбор идёт в фоне (статус — GET /api/entries/{id}).
func (s *Server) addEntry(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if !readJSON(w, r, &req) {
		return
	}
	now := s.clock()
	today := s.Clock.LogicalDate(now)
	ref, msg := s.parseDay(req.Date, today)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	text, msg := cleanText(req.Text)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	id, err := s.Store.SaveVoice(r.Context(), store.VoiceMessage{
		ChatID: s.AllowedUserID, SentAt: now, LogicalDate: today, ReferenceDate: ref,
		Transcript: text, Source: "text", Confirmed: true,
	})
	if err != nil {
		s.fail(w, "save entry", err)
		return
	}
	s.Log.Info("webapp entry stored", "voice_id", id, "reference_date", ref.Format("2006-01-02"))

	name := "запись на " + ref.Format("02.01.2006")
	s.jobs.start(id, func(ctx context.Context) error {
		saved, err := s.Analyzer.Analyze(ctx, id, ref, today, text, false)
		if err != nil {
			s.notify(ctx, "📅 Календарь: "+name+" сохранена, но разобрать её не удалось — повторите разбор в календаре.")
			return err
		}
		s.notify(ctx, "📅 Календарь: добавлена "+name+"."+describeSaved(saved, ref))
		return nil
	})
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]int64{"id": id})
}

// describeSaved — « Записано также в: 04.09. Частей без даты: 1 — уточните в календаре или /pending.»
func describeSaved(saved []store.SavedSegment, ref time.Time) string {
	var other []string
	pending := 0
	for _, sg := range saved {
		switch {
		case sg.Date == nil:
			pending++
		case !sg.Date.Equal(ref):
			other = append(other, sg.Date.Format("02.01"))
		}
	}
	out := ""
	if len(other) > 0 {
		out += " Записано также в: " + strings.Join(other, ", ") + "."
	}
	if pending > 0 {
		out += fmt.Sprintf(" Частей без даты: %d — уточните в календаре или /pending.", pending)
	}
	return out
}

// ---------- статус ----------

type entryStatusResponse struct {
	ID      int64         `json:"id"`
	State   string        `json:"state"` // idle | running | done | failed
	Error   string        `json:"error,omitempty"`
	RefDate string        `json:"refDate"`
	Days    []string      `json:"days"`    // дни, в карточки которых попали данные записи
	Pending []pendingPart `json:"pending"` // части без даты
}

// entryStatus — состояние разбора записи: GET /api/entries/{id}.
func (s *Server) entryStatus(w http.ResponseWriter, r *http.Request) {
	v, ok := s.voice(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	state, errText := s.jobs.get(v.ID)
	resp := entryStatusResponse{ID: v.ID, State: state, RefDate: v.ReferenceDate.Format("2006-01-02"), Days: []string{}}
	if state == stateFailed {
		resp.Error = "Модель не смогла разобрать запись. Текст сохранён — можно повторить разбор."
		s.Log.Warn("webapp analyze failed", "voice_id", v.ID, "err", errText)
	}
	imp, err := s.Store.VoiceImpact(ctx, v.ID)
	if err != nil {
		s.fail(w, "voice impact", err)
		return
	}
	for _, d := range imp.Days {
		resp.Days = append(resp.Days, d.Format("2006-01-02"))
	}
	if resp.Pending, err = s.pendingParts(ctx, v.ID); err != nil {
		s.fail(w, "pending segments", err)
		return
	}
	writeJSON(w, resp)
}

type impactResponse struct {
	Name         string   `json:"name"` // «запись на 05.09.2026 (21:40)»
	Observations int      `json:"observations"`
	Edits        int      `json:"edits"`
	Days         []string `json:"days"`
}

// entryImpact — что удалится вместе с записью: GET /api/entries/{id}/impact.
func (s *Server) entryImpact(w http.ResponseWriter, r *http.Request) {
	v, ok := s.voice(w, r)
	if !ok {
		return
	}
	imp, err := s.Store.VoiceImpact(r.Context(), v.ID)
	if err != nil {
		s.fail(w, "voice impact", err)
		return
	}
	resp := impactResponse{Name: s.entryName(v), Observations: imp.Observations, Edits: imp.Edits, Days: []string{}}
	for _, d := range imp.Days {
		resp.Days = append(resp.Days, d.Format("02.01.2006"))
	}
	writeJSON(w, resp)
}

// ---------- правка ----------

type textRequest struct {
	Text string `json:"text"`
}

// editEntry заменяет текст записи (прежний остаётся в истории, правка = подтверждение) и
// пересобирает её в фоне: PUT /api/entries/{id}/text {text}.
func (s *Server) editEntry(w http.ResponseWriter, r *http.Request) {
	var req textRequest
	if !readJSON(w, r, &req) {
		return
	}
	text, msg := cleanText(req.Text)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	v, ok := s.voice(w, r)
	if !ok {
		return
	}
	if s.jobs.running(v.ID) {
		writeError(w, http.StatusConflict, "Запись ещё разбирается — подождите немного.")
		return
	}
	if text == v.Transcript {
		if err := s.Store.ConfirmVoice(r.Context(), v.ID); err != nil {
			s.fail(w, "confirm voice", err)
			return
		}
		writeJSON(w, map[string]bool{"changed": false})
		return
	}
	if err := s.Store.UpdateTranscript(r.Context(), v.ID, text); err != nil {
		s.fail(w, "update transcript", err)
		return
	}
	name := s.entryName(v)
	s.startReanalyze(v, text, "✎ Календарь: изменён текст: "+name+".", "текст сохранён, но ")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]bool{"changed": true})
}

// reanalyzeEntry пересобирает запись по текущему тексту: POST /api/entries/{id}/reanalyze.
func (s *Server) reanalyzeEntry(w http.ResponseWriter, r *http.Request) {
	v, ok := s.voice(w, r)
	if !ok {
		return
	}
	if !s.startReanalyze(v, v.Transcript, "↻ Календарь: пересобрана "+s.entryName(v)+".", "") {
		writeError(w, http.StatusConflict, "Запись уже разбирается.")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]bool{"ok": true})
}

// startReanalyze запускает фоновый разбор с заменой прежнего; при сбое прежний разбор остаётся.
func (s *Server) startReanalyze(v store.Voice, text, doneMsg, failPrefix string) bool {
	name := s.entryName(v)
	return s.jobs.start(v.ID, func(ctx context.Context) error {
		saved, err := s.Analyzer.Analyze(ctx, v.ID, v.ReferenceDate, v.LogicalDate, text, true)
		if err != nil {
			s.notify(ctx, "Календарь: "+name+" — "+failPrefix+"пересобрать запись не удалось, прежний разбор остался.")
			return err
		}
		s.notify(ctx, doneMsg+describeSaved(saved, v.ReferenceDate))
		return nil
	})
}

// confirmEntry подтверждает расшифровку: POST /api/entries/{id}/confirm.
func (s *Server) confirmEntry(w http.ResponseWriter, r *http.Request) {
	v, ok := s.voice(w, r)
	if !ok {
		return
	}
	if err := s.Store.ConfirmVoice(r.Context(), v.ID); err != nil {
		s.fail(w, "confirm voice", err)
		return
	}
	s.notify(r.Context(), "✓ Календарь: подтверждена расшифровка — "+s.entryName(v)+".")
	writeJSON(w, map[string]bool{"ok": true})
}

// deleteEntry удаляет запись целиком и безвозвратно: DELETE /api/entries/{id}.
func (s *Server) deleteEntry(w http.ResponseWriter, r *http.Request) {
	v, ok := s.voice(w, r)
	if !ok {
		return
	}
	if s.jobs.running(v.ID) {
		writeError(w, http.StatusConflict, "Запись ещё разбирается — удалить можно после окончания разбора.")
		return
	}
	imp, err := s.Store.DeleteVoice(r.Context(), v.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Запись уже удалена.")
		return
	}
	if err != nil {
		s.fail(w, "delete voice", err)
		return
	}
	s.Log.Info("webapp entry deleted", "voice_id", v.ID, "observations", imp.Observations)
	s.notify(r.Context(), "🗑 Календарь: удалена "+s.entryName(v)+".")
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------- даты частей записи ----------

// resolveSegment назначает день части записи без даты: POST /api/segments/{id}/date {date}.
func (s *Server) resolveSegment(w http.ResponseWriter, r *http.Request) {
	segID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "Часть записи не найдена.")
		return
	}
	var req addRequest
	if !readJSON(w, r, &req) {
		return
	}
	date, msg := s.parseDay(req.Date, s.Clock.LogicalDate(s.clock()))
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ctx := r.Context()
	parts, err := s.pendingParts(ctx, 0)
	if err != nil {
		s.fail(w, "pending segments", err)
		return
	}
	var part *pendingPart
	for i := range parts {
		if parts[i].ID == segID {
			part = &parts[i]
		}
	}
	if part == nil {
		writeError(w, http.StatusNotFound, "Дата этой части уже уточнена или запись удалена.")
		return
	}
	if err := s.Store.ResolveSegment(ctx, segID, date); errors.Is(err, store.ErrNotPending) {
		writeError(w, http.StatusNotFound, "Дата этой части уже уточнена или запись удалена.")
		return
	} else if err != nil {
		s.fail(w, "resolve segment", err)
		return
	}
	s.notify(ctx, "📅 Календарь: "+lowerFirst(part.Entry)+" — части без даты назначен день "+date.Format("02.01.2006")+".")
	writeJSON(w, map[string]string{"date": date.Format("2006-01-02")})
}

// pendingParts — части записей без даты; voiceID != 0 — только части этой записи.
func (s *Server) pendingParts(ctx context.Context, voiceID int64) ([]pendingPart, error) {
	segs, err := s.Store.PendingSegments(ctx)
	if err != nil {
		return nil, err
	}
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		return nil, err
	}
	out := []pendingPart{}
	names := map[int64]string{}
	for _, p := range segs {
		if voiceID != 0 && p.VoiceID != voiceID {
			continue
		}
		name, ok := names[p.VoiceID]
		if !ok {
			name = "Запись на " + p.RefDate.Format("02.01.2006")
			if v, err := s.Store.GetVoice(ctx, p.VoiceID); err == nil {
				name = upperFirst(s.entryName(v))
			}
			names[p.VoiceID] = name
		}
		out = append(out, pendingPart{
			ID: p.ID, Voice: p.VoiceID, RefDate: p.RefDate.Format("2006-01-02"), Entry: name,
			Preview: entry.Preview(p.Segment, schema, 160),
		})
	}
	return out, nil
}

// ---------- помощники ----------

// voice достаёт запись по {id} из пути; при ошибке сам отвечает клиенту.
func (s *Server) voice(w http.ResponseWriter, r *http.Request) (store.Voice, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "Запись не найдена.")
		return store.Voice{}, false
	}
	v, err := s.Store.GetVoice(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Запись не найдена — возможно, она уже удалена.")
		return store.Voice{}, false
	}
	if err != nil {
		s.fail(w, "get voice", err)
		return store.Voice{}, false
	}
	return v, true
}

// entryName — «запись на 05.09.2026 (21:40)»: день записи и время, когда её сделали.
func (s *Server) entryName(v store.Voice) string {
	return "запись на " + v.ReferenceDate.Format("02.01.2006") + " (" + v.SentAt.In(s.Clock.Loc).Format("15:04") + ")"
}

func (s *Server) minDate(today time.Time) time.Time { return today.AddDate(-maxPastYears, 0, 0) }

// parseDay разбирает день ГГГГ-ММ-ДД: не позже текущего дня дневника и не раньше чем 2 года назад.
// Вторым значением — сообщение об ошибке для пользователя.
func (s *Server) parseDay(v string, today time.Time) (time.Time, string) {
	d, err := time.Parse("2006-01-02", v)
	switch {
	case err != nil:
		return time.Time{}, "Дата в формате ГГГГ-ММ-ДД."
	case d.After(today):
		return time.Time{}, "Нельзя выбрать день в будущем."
	case d.Before(s.minDate(today)):
		return time.Time{}, "Можно выбрать день не раньше чем два года назад."
	}
	return d, ""
}

func cleanText(t string) (string, string) {
	t = strings.TrimSpace(t)
	switch {
	case t == "":
		return "", "Запись пустая."
	case utf8.RuneCountInString(t) > maxEntryRunes:
		return "", fmt.Sprintf("Слишком длинная запись: больше %d символов. Разбейте её на несколько.", maxEntryRunes)
	}
	return t, ""
}

// readJSON читает тело запроса (не больше 64 КБ); при ошибке сам отвечает 400.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный запрос.")
		return false
	}
	return true
}

func (s *Server) notify(ctx context.Context, text string) {
	if s.Notify != nil {
		s.Notify(ctx, text)
	}
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	}
	return string(r)
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = []rune(strings.ToLower(string(r[0])))[0]
	}
	return string(r)
}
