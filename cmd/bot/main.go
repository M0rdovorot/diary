package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/config"
	"diary/internal/handler"
	"diary/internal/stt"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := stt.NewStorage(stt.StorageConfig{
		Endpoint:  cfg.YCS3Endpoint,
		Region:    cfg.YCS3Region,
		Bucket:    cfg.YCBucket,
		AccessKey: cfg.YCS3AccessKey,
		SecretKey: cfg.YCS3SecretKey,
	})
	if err != nil {
		log.Error("storage init", "err", err)
		os.Exit(1)
	}

	h := &handler.Handler{
		Log:           log,
		AllowedUserID: cfg.AllowedUserID,
		DataDir:       cfg.DataDir,
		Storage:       store,
		SpeechKit:     stt.NewSpeechKit(cfg.YCAPIKey),
	}

	b, err := bot.New(cfg.TelegramToken, bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, u *models.Update) {}))
	if err != nil {
		log.Error("bot init", "err", err)
		os.Exit(1)
	}
	h.Register(b)

	log.Info("bot started")
	b.Start(ctx)
	log.Info("bot stopped")
}
