package webapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"diary/internal/store"
)

const (
	// maxRecordingBytes — предел размера присланной записи (20 минут при 32 кбит/с — около 5 МБ).
	maxRecordingBytes = 80 << 20
	// maxRecordingSec — предел длины записи (асинхронный SpeechKit принимает до 4 часов).
	maxRecordingSec = 4 * 3600
	// uploadTimeout — сколько ждать, пока телефон дольёт запись (общий ReadTimeout сервера — 30 с).
	uploadTimeout = 5 * time.Minute
	// transcribeTimeout — общий лимит на конвертацию и распознавание записи.
	transcribeTimeout = 30 * time.Minute
)

// recTask — распознавание записи голоса в фоне.
type recTask struct {
	state    string // running | done | failed
	err      string
	voice    int64
	text     string
	finished time.Time
}

type recordings struct {
	mu sync.Mutex
	m  map[string]*recTask
}

func (r *recordings) add() (string, *recTask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*recTask{}
	}
	for k, t := range r.m {
		if t.state != "running" && time.Since(t.finished) > time.Hour {
			delete(r.m, k)
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	t := &recTask{state: "running"}
	r.m[id] = t
	return id, t
}

func (r *recordings) finish(t *recTask, voice int64, text string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.finished = time.Now()
	if err != nil {
		t.state, t.err = "failed", err.Error()
		return
	}
	t.state, t.voice, t.text = "done", voice, text
}

func (r *recordings) get(id string) (recTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.m[id]
	if !ok {
		return recTask{}, false
	}
	return *t, true
}

// uploadRecording принимает запись голоса из браузера: POST /api/recordings?date=ГГГГ-ММ-ДД&duration=сек,
// тело — аудио как есть (webm, mp4, ogg). Распознавание идёт в фоне (GET /api/recordings/{id}); текст
// сохраняется сразу, как неподтверждённая запись без разбора — ничего не потеряется, даже если
// пользователь закроет приложение, не дождавшись.
func (s *Server) uploadRecording(w http.ResponseWriter, r *http.Request) {
	if s.Transcribe == nil || s.Convert == nil {
		writeError(w, http.StatusServiceUnavailable, "Запись голоса на сервере не настроена. Отправьте голосовое прямо боту в чат.")
		return
	}
	now := s.clock()
	today := s.Clock.LogicalDate(now)
	ref, msg := s.parseDay(r.URL.Query().Get("date"), today)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	dur, err := strconv.Atoi(r.URL.Query().Get("duration"))
	if err != nil || dur < 1 {
		dur = 1
	}
	if dur > maxRecordingSec {
		writeError(w, http.StatusBadRequest, "Слишком длинная запись.")
		return
	}

	// дольше, чем общий ReadTimeout: запись может литься с телефона по мобильной сети
	// (ответ уходит после приёма всего тела, поэтому срок записи продлеваем так же)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTimeout))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadTimeout + time.Minute))
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		s.fail(w, "recording dir", err)
		return
	}
	in, err := os.CreateTemp(s.DataDir, "rec_*.src")
	if err != nil {
		s.fail(w, "recording temp", err)
		return
	}
	n, err := io.Copy(in, http.MaxBytesReader(w, r.Body, maxRecordingBytes))
	in.Close()
	if err != nil || n == 0 {
		os.Remove(in.Name())
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "Запись слишком большая.")
		} else {
			writeError(w, http.StatusBadRequest, "Запись не получена. Попробуйте ещё раз.")
		}
		return
	}

	id, task := s.recs.add()
	s.Log.Info("webapp recording received", "bytes", n, "duration_s", dur, "reference_date", ref.Format("2006-01-02"))
	go s.transcribeRecording(task, in.Name(), now, today, ref, dur)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"id": id})
}

func (s *Server) transcribeRecording(task *recTask, src string, sent, today, ref time.Time, dur int) {
	ctx, cancel := context.WithTimeout(context.Background(), transcribeTimeout)
	defer cancel()
	defer os.Remove(src)

	voice, text, err := func() (int64, string, error) {
		ogg := src + ".ogg"
		defer os.Remove(ogg) // после распознавания (и при сбое) аудио не храним: сохраняется только текст
		if err := s.Convert(ctx, src, ogg); err != nil {
			s.Log.Error("webapp recording convert", "err", err)
			return 0, "", errors.New("Не удалось обработать звук. Попробуйте записать ещё раз.")
		}
		text, err := s.Transcribe(ctx, ogg)
		if err != nil {
			s.Log.Error("webapp recording transcribe", "err", err)
			return 0, "", errors.New("Не удалось распознать речь. Попробуйте ещё раз.")
		}
		if text == "" {
			return 0, "", errors.New("Речь не распознана. Попробуйте записать ещё раз.")
		}
		id, err := s.Store.SaveVoice(ctx, store.VoiceMessage{
			ChatID: s.AllowedUserID, SentAt: sent, LogicalDate: today, ReferenceDate: ref, DurationSec: dur,
			Transcript: text, Source: "voice", Confirmed: false,
		})
		if err != nil {
			s.Log.Error("webapp recording save", "err", err)
			return 0, "", errors.New("Не удалось сохранить расшифровку.")
		}
		return id, text, nil
	}()
	s.recs.finish(task, voice, text, err)
}

type recordingStatus struct {
	State string `json:"state"` // running | done | failed
	Error string `json:"error,omitempty"`
	Voice int64  `json:"voice,omitempty"`
	Text  string `json:"text,omitempty"`
}

// recordingStatus — ход распознавания: GET /api/recordings/{id}.
func (s *Server) recordingStatus(w http.ResponseWriter, r *http.Request) {
	t, ok := s.recs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Запись не найдена — возможно, сервер перезапускался. Запишите ещё раз.")
		return
	}
	writeJSON(w, recordingStatus{State: t.state, Error: t.err, Voice: t.voice, Text: t.text})
}

// saveRecording завершает запись голоса: пользователь проверил и при необходимости исправил текст —
// сохраняем его (прежний вариант остаётся в истории правок), подтверждаем и разбираем моделью:
// POST /api/entries/{id}/save {text}. Дальше клиент ждёт разбор так же, как у текстовой записи.
func (s *Server) saveRecording(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusConflict, "Запись уже разбирается.")
		return
	}
	if err := s.Store.UpdateTranscript(r.Context(), v.ID, text); err != nil { // тот же текст — просто подтверждение
		s.fail(w, "update transcript", err)
		return
	}
	name := s.entryName(v)
	ref, today := v.ReferenceDate, v.LogicalDate
	s.jobs.start(v.ID, func(ctx context.Context) error {
		saved, err := s.Analyzer.Analyze(ctx, v.ID, ref, today, text, true)
		if err != nil {
			s.notify(ctx, "🎙 Календарь: "+name+" сохранена, но разобрать её не удалось — повторите разбор в календаре.")
			return err
		}
		s.notify(ctx, fmt.Sprintf("🎙 Календарь: добавлена %s (голос).%s", name, describeSaved(saved, ref)))
		return nil
	})
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]int64{"id": v.ID})
}
