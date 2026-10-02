package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/cleanup"
	"diary/internal/config"
	"diary/internal/diaryday"
	"diary/internal/handler"
	"diary/internal/store"
	"diary/internal/stt"
	"diary/internal/summary"
	"diary/internal/webapp"
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

	clock, err := diaryday.NewClock(cfg.DiaryTZ, cfg.DayCutoff)
	if err != nil {
		log.Error("timezone", "tz", cfg.DiaryTZ, "err", err)
		os.Exit(1)
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}

	s3, err := stt.NewStorage(stt.StorageConfig{
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

	go cleanup.Run(ctx, log, cfg.DataDir, cfg.DataRetention, 24*time.Hour)

	h := &handler.Handler{
		Log:           log,
		AllowedUserID: cfg.AllowedUserID,
		DataDir:       cfg.DataDir,
		Storage:       s3,
		Store:         db,
		Clock:         clock,
		SpeechKit:     stt.NewSpeechKit(cfg.YCAPIKey),
		Summarizer:    summary.NewYandexGPT(cfg.YCAPIKey, cfg.YCFolderID, cfg.YCGPTModel),
		WebAppURL:     cfg.WebAppURL,
	}

	b, err := bot.New(cfg.TelegramToken, bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, u *models.Update) {}))
	if err != nil {
		log.Error("bot init", "err", err)
		os.Exit(1)
	}
	h.Register(b)

	// меню команд в Telegram; сбой не критичен
	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "help", Description: "Все команды и как всё работает"},
		{Command: "day", Description: "Карточка дня: /day, /day 01.09, /day вчера"},
		{Command: "calendar", Description: "Календарь дней с записями"},
		{Command: "date", Description: "Выбрать день для новых голосовых: /date 01.09, /date сброс"},
		{Command: "edit", Description: "Исправить расшифровку голосового"},
		{Command: "categories", Description: "Категории записи: добавить, изменить, скрыть"},
		{Command: "pending", Description: "Записи без даты и неподтверждённые расшифровки"},
	}}); err != nil {
		log.Warn("set commands", "err", err)
	}

	// кнопка меню слева от поля ввода: календарь, если он настроен, иначе список команд
	var menu models.InputMenuButton = models.MenuButtonCommands{Type: models.MenuButtonTypeCommands}
	if cfg.WebAppURL != "" {
		menu = models.MenuButtonWebApp{Type: models.MenuButtonTypeWebApp, Text: "Календарь", WebApp: models.WebAppInfo{URL: cfg.WebAppURL}}
	}
	if _, err := b.SetChatMenuButton(ctx, &bot.SetChatMenuButtonParams{MenuButton: menu}); err != nil {
		log.Warn("set menu button", "err", err)
	}

	if cfg.WebAppURL != "" {
		web := &webapp.Server{Log: log, Store: db, Clock: clock, BotToken: cfg.TelegramToken, AllowedUserID: cfg.AllowedUserID}
		go func() {
			if err := web.Run(ctx, cfg.WebAppAddr); err != nil {
				log.Error("webapp", "err", err)
			}
		}()
	}

	log.Info("bot started")
	b.Start(ctx)
	log.Info("bot stopped")
}
