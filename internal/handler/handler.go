package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/diaryday"
	"diary/internal/entry"
	"diary/internal/store"
	"diary/internal/stt"
	"diary/internal/summary"
)

type Handler struct {
	Log           *slog.Logger
	AllowedUserID int64
	DataDir       string
	Storage       *stt.Storage
	SpeechKit     *stt.SpeechKit
	Summarizer    *summary.YandexGPT
	Store         *store.Store
	Clock         diaryday.Clock

	mu    sync.Mutex
	input map[int64]pendingInput // chatID -> что сейчас ждём от пользователя текстом
}

// pendingInput — ожидаемый текстовый ввод: дата для сегмента или правка расшифровки.
type pendingInput struct {
	kind inputKind
	id   int64 // id сегмента (inputDate) или голосового (inputReplace, inputWhole)
}

type inputKind int

const (
	inputDate    inputKind = iota // дата для сегмента без даты
	inputReplace                  // список замен «было => стало»
	inputWhole                    // новый текст расшифровки целиком
)

const (
	// recognizeTimeout — общий лимит на загрузку и распознавание одного голосового.
	recognizeTimeout = 30 * time.Minute
	// summarizeTimeout — лимит на разбор записи моделью.
	summarizeTimeout = 3 * time.Minute
	// referenceDateKey — настройка «рабочего дня» (/date).
	referenceDateKey = "reference_date"
)

// Register добавляет хендлеры. Порядок важен: срабатывает первый подходящий.
func (h *Handler) Register(b *bot.Bot) {
	b.RegisterHandlerMatchFunc(func(u *models.Update) bool {
		return u.Message != nil && u.Message.Voice != nil
	}, h.onVoice)
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommandStartOnly, h.onHelp)
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommandStartOnly, h.onHelp)
	b.RegisterHandler(bot.HandlerTypeMessageText, "day", bot.MatchTypeCommandStartOnly, h.onDay)
	b.RegisterHandler(bot.HandlerTypeMessageText, "date", bot.MatchTypeCommandStartOnly, h.onDate)
	b.RegisterHandler(bot.HandlerTypeMessageText, "edit", bot.MatchTypeCommandStartOnly, h.onEdit)
	b.RegisterHandler(bot.HandlerTypeMessageText, "pending", bot.MatchTypeCommandStartOnly, h.onPending)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, dateCallbackPrefix, bot.MatchTypePrefix, h.onDateCallback)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, editCallbackPrefix, bot.MatchTypePrefix, h.onEditCallback)
	b.RegisterHandlerMatchFunc(h.isAwaitedInput, h.onAwaitedInput)
}

func (h *Handler) onVoice(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if m.From == nil || m.From.ID != h.AllowedUserID {
		h.Log.Warn("voice from unauthorized user")
		return
	}

	sent := time.Unix(int64(m.Date), 0)
	// день записи фиксируем в момент получения: выбранный /date или день отправки
	ref := h.referenceDate(ctx, h.Clock.LogicalDate(sent))

	path, err := h.download(ctx, b, m.Voice.FileID, m.ID)
	if err != nil {
		h.Log.Error("download voice", "err", err)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: "Не удалось скачать голосовое"})
		return
	}
	h.Log.Info("voice downloaded", "path", path, "duration_s", m.Voice.Duration)

	ack := fmt.Sprintf("Голосовое получено (%d с), распознаю…", m.Voice.Duration)
	if !ref.Equal(h.Clock.LogicalDate(sent)) {
		ack += "\nДень записи: " + ref.Format("02.01.2006") + " (выбран командой /date, сбросить: /date сброс)"
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: ack})

	text, err := h.transcribe(ctx, path)
	if err != nil {
		// файл остаётся на диске: его подметёт cleanup по сроку хранения
		h.Log.Error("transcribe", "err", err)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: "Не удалось распознать голосовое"})
		return
	}
	// аудио больше не нужно: текст получен, а в Telegram оригинал остаётся
	if err := os.Remove(path); err != nil {
		h.Log.Warn("remove audio", "path", path, "err", err)
	}
	h.Log.Info("transcribed", "chars", len(text))
	if text == "" {
		h.send(ctx, b, m.Chat.ID, "Речь не распознана")
		return
	}

	h.processTranscript(ctx, b, m.Chat.ID, m.ID, sent, ref, m.Voice.Duration, text)
}

// referenceDate возвращает выбранный /date рабочий день, а если он не выбран — fallback.
func (h *Handler) referenceDate(ctx context.Context, fallback time.Time) time.Time {
	v, ok, err := h.Store.Setting(ctx, referenceDateKey)
	if err != nil {
		h.Log.Error("read reference date", "err", err)
		return fallback
	}
	if !ok {
		return fallback
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return fallback
	}
	return d
}

// processTranscript — всё, что происходит после распознавания: сохранить расшифровку,
// разобрать её моделью по дням, сохранить сегменты и ответить в чат.
// ref — день, к которому отнесено голосовое (от него считаются «сегодня»/«вчера»).
func (h *Handler) processTranscript(ctx context.Context, b *bot.Bot, chatID int64, msgID int, sent, ref time.Time, durationSec int, text string) {
	logical := h.Clock.LogicalDate(sent)

	// сначала сохраняем расшифровку: что бы ни случилось дальше, запись не потеряется
	voiceID, err := h.Store.SaveVoice(ctx, store.VoiceMessage{
		ChatID:        chatID,
		MessageID:     int64(msgID),
		SentAt:        sent,
		LogicalDate:   logical,
		ReferenceDate: ref,
		DurationSec:   durationSec,
		Transcript:    text,
	})
	if err != nil {
		h.Log.Error("save voice", "err", err)
	} else {
		h.Log.Info("voice stored", "voice_id", voiceID, "reference_date", ref.Format("2006-01-02"))
	}

	h.analyze(ctx, b, chatID, voiceID, ref, logical, text, false)
}

// analyze разбирает расшифровку моделью, сохраняет сегменты и отвечает в чат.
// replace — заменить уже сохранённые результаты этого голосового (после правки расшифровки).
// voiceID == 0 — голосовое не удалось сохранить в базу: результат только показываем.
func (h *Handler) analyze(ctx context.Context, b *bot.Bot, chatID, voiceID int64, ref, today time.Time, text string, replace bool) {
	sctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
	defer cancel()
	raw, segs, err := h.Summarizer.Extract(sctx, text, ref)
	if err != nil {
		// не теряем запись: при сбое разбора отдаём сырую расшифровку (она уже в базе)
		h.Log.Error("extract", "err", err)
		if replace {
			h.send(ctx, b, chatID, "Расшифровка сохранена, но пересобрать запись не удалось: прежняя версия записи осталась без изменений.")
		} else {
			h.send(ctx, b, chatID, "Не удалось разобрать запись, вот расшифровка:")
			h.send(ctx, b, chatID, text)
		}
		if voiceID != 0 {
			h.offerRedo(ctx, b, chatID, voiceID)
		}
		return
	}

	in := store.ExtractionInput{
		VoiceID: voiceID, RefDate: ref, Today: today,
		Model: h.Summarizer.Model, PromptVersion: summary.PromptVersion, Raw: raw, Segments: segs,
	}
	var saved []store.SavedSegment
	if voiceID != 0 {
		if replace {
			saved, err = h.Store.ReplaceExtraction(ctx, in)
		} else {
			saved, err = h.Store.SaveExtraction(ctx, in)
		}
		if err != nil {
			h.Log.Error("save extraction", "err", err)
			saved = nil
		}
	}
	if saved == nil {
		// база недоступна: всё равно показываем результат, но без сохранения
		h.send(ctx, b, chatID, "Внимание: запись не удалось сохранить в базу.")
		for _, seg := range segs {
			sg := store.SavedSegment{Segment: seg}
			if d, ok := seg.ResolvedDate(today); ok {
				sg.Date = &d
			}
			saved = append(saved, sg)
		}
	}
	h.Log.Info("extracted", "segments", len(saved), "replace", replace)

	// вопросов о дате может быть несколько: нумеруем их и даём выдержку, чтобы различать
	totalAsks := 0
	for _, sg := range saved {
		if sg.Date == nil && sg.ID != 0 {
			totalAsks++
		}
	}
	asked := 0
	for _, sg := range saved {
		out := entry.Render(sg.Segment, sg.Date)
		if sg.Date != nil && !sg.Date.Equal(ref) {
			out = "Запись о другом дне — добавлена в карточку этого дня.\n\n" + out
		}
		h.send(ctx, b, chatID, out)
		if sg.Date == nil && sg.ID != 0 {
			asked++
			hint := ""
			if totalAsks > 1 {
				hint = preview(sg.Segment, 140)
			}
			h.askDate(ctx, b, chatID, sg.ID, ref, asked, totalAsks, hint)
		}
	}
}

// offerRedo предлагает повторить разбор сохранённой расшифровки (например, после сбоя модели).
func (h *Handler) offerRedo(ctx context.Context, b *bot.Bot, chatID, voiceID int64) {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Повторить разбор записи?",
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Повторить разбор", CallbackData: fmt.Sprintf("%s%d", editRedoPrefix, voiceID)},
		}}},
	})
	if err != nil {
		h.Log.Error("offer redo", "err", err)
	}
}

// send отправляет текст, разбивая его на части под лимит Telegram (4096 символов).
func (h *Handler) send(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	for _, part := range splitMessage(text, 4000) {
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: part}); err != nil {
			h.Log.Error("send message", "err", err)
			return
		}
	}
}

// splitMessage режет текст на куски не длиннее limit рун, по возможности по границе строки или слова.
func splitMessage(text string, limit int) []string {
	runes := []rune(text)
	var parts []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
			if runes[i-1] == ' ' && cut == limit {
				cut = i
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if s := strings.TrimSpace(string(runes)); s != "" {
		parts = append(parts, s)
	}
	return parts
}

// transcribe загружает файл в Object Storage, распознаёт через SpeechKit и чистит бакет.
func (h *Handler) transcribe(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, recognizeTimeout)
	defer cancel()

	key := "voice/" + filepath.Base(path)
	if err := h.Storage.Upload(ctx, key, path); err != nil {
		return "", err
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dcancel()
		if err := h.Storage.Delete(dctx, key); err != nil {
			h.Log.Warn("delete from bucket", "key", key, "err", err)
		}
	}()

	url, err := h.Storage.PresignedURL(ctx, key, time.Hour)
	if err != nil {
		return "", err
	}
	return h.SpeechKit.Recognize(ctx, url)
}

func (h *Handler) download(ctx context.Context, b *bot.Bot, fileID string, msgID int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	f, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("getFile: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.FileDownloadLink(f), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}

	if err := os.MkdirAll(h.DataDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(h.DataDir, fmt.Sprintf("%d_%d.ogg", time.Now().Unix(), msgID))
	out, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}
	return path, nil
}
