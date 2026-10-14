package webapp

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// bulkTimeout — предел на пересборку всех записей целиком.
const bulkTimeout = 3 * time.Hour

// bulkRebuild — пересборка набора записей (день или все) в фоне. Одновременно идёт не больше одной;
// состояние живёт в памяти — после перезапуска бота незаконченная пересборка прерывается (уже
// пересобранные записи остаются, остальные сохраняют прежний разбор).
type bulkRebuild struct {
	mu sync.Mutex
	st bulkState
}

type bulkState struct {
	State   string `json:"state"` // idle | running | done
	Scope   string `json:"scope"` // day | all
	Date    string `json:"date,omitempty"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"` // записи, которые в этот момент уже разбирались
	Pending int    `json:"pending"` // частей без дат после пересборки (во всей базе)
}

func (b *bulkRebuild) snapshot() bulkState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.st.State == "" {
		return bulkState{State: "idle"}
	}
	return b.st
}

func (b *bulkRebuild) update(fn func(*bulkState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(&b.st)
}

// scopeDay разбирает параметры охвата: «d=ГГГГ-ММ-ДД» — один день, без d — все записи.
func (s *Server) scopeDay(w http.ResponseWriter, scope, date string) (*time.Time, bool) {
	if scope == "all" {
		return nil, true
	}
	if scope != "day" {
		writeError(w, http.StatusBadRequest, "Охват пересборки: day или all.")
		return nil, false
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Дата в формате ГГГГ-ММ-ДД.")
		return nil, false
	}
	return &d, true
}

type planResponse struct {
	Voices    int `json:"voices"`
	Days      int `json:"days"`
	UserDates int `json:"userDates"`
	HiddenObs int `json:"hiddenValues"`
}

// rebuildPlan — что затронет пересборка: GET /api/rebuild/plan?scope=all | ?scope=day&d=2026-09-05.
func (s *Server) rebuildPlan(w http.ResponseWriter, r *http.Request) {
	day, ok := s.scopeDay(w, r.URL.Query().Get("scope"), r.URL.Query().Get("d"))
	if !ok {
		return
	}
	plan, err := s.Store.RebuildPlanFor(r.Context(), day)
	if err != nil {
		s.fail(w, "rebuild plan", err)
		return
	}
	writeJSON(w, planResponse{Voices: len(plan.VoiceIDs), Days: plan.Days, UserDates: plan.UserDates, HiddenObs: plan.HiddenObs})
}

type rebuildRequest struct {
	Scope string `json:"scope"`
	Date  string `json:"date"`
}

// rebuildStart запускает пересборку записей текущей схемой категорий: POST /api/rebuild {scope, date}.
// Каждая запись разбирается заново; если модель не справилась, прежний разбор записи остаётся.
func (s *Server) rebuildStart(w http.ResponseWriter, r *http.Request) {
	var req rebuildRequest
	if !readJSON(w, r, &req) {
		return
	}
	day, ok := s.scopeDay(w, req.Scope, req.Date)
	if !ok {
		return
	}
	plan, err := s.Store.RebuildPlanFor(r.Context(), day)
	if err != nil {
		s.fail(w, "rebuild plan", err)
		return
	}
	if len(plan.VoiceIDs) == 0 {
		writeError(w, http.StatusNotFound, "Нет записей для пересборки.")
		return
	}

	s.bulk.mu.Lock()
	if s.bulk.st.State == "running" {
		s.bulk.mu.Unlock()
		writeError(w, http.StatusConflict, "Пересборка уже идёт — дождитесь её окончания.")
		return
	}
	s.bulk.st = bulkState{State: "running", Scope: req.Scope, Total: len(plan.VoiceIDs)}
	if day != nil {
		s.bulk.st.Date = day.Format("2006-01-02")
	}
	s.bulk.mu.Unlock()

	go s.runBulk(plan.VoiceIDs, req.Scope, day)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, s.bulk.snapshot())
}

func (s *Server) runBulk(ids []int64, scope string, day *time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	s.Log.Info("webapp rebuild started", "scope", scope, "voices", len(ids))

	for _, id := range ids {
		v, err := s.Store.GetVoice(ctx, id)
		if err != nil { // запись удалили, пока шла очередь
			s.bulk.update(func(b *bulkState) { b.Skipped++ })
			continue
		}
		ran, err := s.jobs.run(ctx, id, func(ctx context.Context) error {
			_, err := s.Analyzer.Analyze(ctx, id, v.ReferenceDate, v.LogicalDate, v.Transcript, true)
			return err
		})
		switch {
		case !ran:
			s.bulk.update(func(b *bulkState) { b.Skipped++ })
		case err != nil:
			s.Log.Warn("webapp rebuild: запись не разобрана", "voice_id", id, "err", err)
			s.bulk.update(func(b *bulkState) { b.Failed++ })
		default:
			s.bulk.update(func(b *bulkState) { b.Done++ })
		}
	}

	pending := 0
	if parts, err := s.pendingParts(ctx, 0); err == nil {
		pending = len(parts)
	}
	s.bulk.update(func(b *bulkState) { b.State, b.Pending = "done", pending })
	st := s.bulk.snapshot()
	s.Log.Info("webapp rebuild finished", "done", st.Done, "failed", st.Failed, "skipped", st.Skipped)

	what := "всех записей"
	if day != nil {
		what = "записей дня " + day.Format("02.01.2006")
	}
	msg := fmt.Sprintf("↻ Календарь: пересборка %s завершена — пересобрано %d из %d", what, st.Done, st.Total)
	if st.Failed > 0 {
		msg += fmt.Sprintf(", не удалось: %d (их прежний разбор остался)", st.Failed)
	}
	msg += "."
	if st.Pending > 0 {
		msg += fmt.Sprintf(" Частей без даты: %d — уточните в календаре или /pending.", st.Pending)
	}
	s.notify(context.Background(), msg)
}

// rebuildStatus — ход пересборки: GET /api/rebuild.
func (s *Server) rebuildStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.bulk.snapshot())
}
