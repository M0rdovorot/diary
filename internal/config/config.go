package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken string
	AllowedUserID int64
	DataDir       string
	// DataRetention — через сколько локальные .ogg, оставшиеся после неудачной обработки, удаляются.
	DataRetention time.Duration

	DatabaseURL string
	DiaryTZ     string // часовой пояс пользователя
	DayCutoff   int    // час, до которого момент относится к предыдущему дню дневника

	YCAPIKey      string
	YCFolderID    string
	YCGPTModel    string
	YCBucket      string
	YCS3AccessKey string
	YCS3SecretKey string
	YCS3Endpoint  string
	YCS3Region    string

	// WebAppURL — публичный HTTPS-адрес Mini App (календаря); пусто — веб-интерфейс выключен.
	WebAppURL  string
	WebAppAddr string // где слушает HTTP-сервер Mini App (за обратным прокси с TLS)
}

func Load() (*Config, error) {
	c := &Config{
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		DataDir:       os.Getenv("DATA_DIR"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		DiaryTZ:       os.Getenv("DIARY_TZ"),
		YCAPIKey:      os.Getenv("YC_API_KEY"),
		YCFolderID:    os.Getenv("YC_FOLDER_ID"),
		YCGPTModel:    os.Getenv("YC_GPT_MODEL"),
		YCBucket:      os.Getenv("YC_BUCKET"),
		YCS3AccessKey: os.Getenv("YC_S3_ACCESS_KEY_ID"),
		YCS3SecretKey: os.Getenv("YC_S3_SECRET_ACCESS_KEY"),
		YCS3Endpoint:  os.Getenv("YC_S3_ENDPOINT"),
		YCS3Region:    os.Getenv("YC_S3_REGION"),
		WebAppURL:     os.Getenv("WEBAPP_URL"),
		WebAppAddr:    os.Getenv("WEBAPP_ADDR"),
	}
	for name, v := range map[string]string{
		"DATABASE_URL":            c.DatabaseURL,
		"YC_API_KEY":              c.YCAPIKey,
		"YC_FOLDER_ID":            c.YCFolderID,
		"YC_BUCKET":               c.YCBucket,
		"YC_S3_ACCESS_KEY_ID":     c.YCS3AccessKey,
		"YC_S3_SECRET_ACCESS_KEY": c.YCS3SecretKey,
	} {
		if v == "" {
			return nil, errors.New(name + " is required")
		}
	}
	if c.DiaryTZ == "" {
		c.DiaryTZ = "Europe/Moscow"
	}
	c.DataRetention = 7 * 24 * time.Hour
	if v := os.Getenv("DATA_RETENTION_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 {
			return nil, errors.New("DATA_RETENTION_DAYS must be a positive integer")
		}
		c.DataRetention = time.Duration(d) * 24 * time.Hour
	}
	c.DayCutoff = 5
	if v := os.Getenv("DAY_CUTOFF_HOUR"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || h < 0 || h > 12 {
			return nil, errors.New("DAY_CUTOFF_HOUR must be an integer 0..12")
		}
		c.DayCutoff = h
	}
	if c.YCGPTModel == "" {
		c.YCGPTModel = "yandexgpt/latest" // YandexGPT Pro
	}
	if c.YCS3Endpoint == "" {
		c.YCS3Endpoint = "https://storage.yandexcloud.net"
	}
	if c.YCS3Region == "" {
		c.YCS3Region = "ru-central1"
	}
	if c.WebAppURL != "" && !strings.HasPrefix(c.WebAppURL, "https://") {
		return nil, errors.New("WEBAPP_URL must start with https:// (Telegram opens Mini Apps only over HTTPS)")
	}
	if c.WebAppAddr == "" {
		c.WebAppAddr = ":8080"
	}
	if c.TelegramToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	id, err := strconv.ParseInt(os.Getenv("ALLOWED_USER_ID"), 10, 64)
	if err != nil {
		return nil, errors.New("ALLOWED_USER_ID is required and must be an integer")
	}
	c.AllowedUserID = id
	return c, nil
}
