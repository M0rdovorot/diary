package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/stt"
)

type Handler struct {
	Log           *slog.Logger
	AllowedUserID int64
	DataDir       string
	Storage       *stt.Storage
	SpeechKit     *stt.SpeechKit
}

// recognizeTimeout — общий лимит на загрузку и распознавание одного голосового.
const recognizeTimeout = 30 * time.Minute

func (h *Handler) Register(b *bot.Bot) {
	b.RegisterHandlerMatchFunc(func(u *models.Update) bool {
		return u.Message != nil && u.Message.Voice != nil
	}, h.onVoice)
}

func (h *Handler) onVoice(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if m.From == nil || m.From.ID != h.AllowedUserID {
		h.Log.Warn("voice from unauthorized user")
		return
	}

	path, err := h.download(ctx, b, m.Voice.FileID, m.ID)
	if err != nil {
		h.Log.Error("download voice", "err", err)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: "Не удалось скачать голосовое"})
		return
	}
	h.Log.Info("voice saved", "path", path, "duration_s", m.Voice.Duration)

	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: m.Chat.ID,
		Text:   fmt.Sprintf("Голосовое получено (%d с), распознаю…", m.Voice.Duration),
	})

	text, err := h.transcribe(ctx, path)
	if err != nil {
		h.Log.Error("transcribe", "err", err)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: "Не удалось распознать голосовое"})
		return
	}
	h.Log.Info("transcribed", "chars", len(text))
	if text == "" {
		text = "(речь не распознана)"
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: text})
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
