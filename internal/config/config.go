package config

import (
	"errors"
	"os"
	"strconv"
)

type Config struct {
	TelegramToken string
	AllowedUserID int64
	DataDir       string

	YCAPIKey      string
	YCBucket      string
	YCS3AccessKey string
	YCS3SecretKey string
	YCS3Endpoint  string
	YCS3Region    string
}

func Load() (*Config, error) {
	c := &Config{
		TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		DataDir:       os.Getenv("DATA_DIR"),
		YCAPIKey:      os.Getenv("YC_API_KEY"),
		YCBucket:      os.Getenv("YC_BUCKET"),
		YCS3AccessKey: os.Getenv("YC_S3_ACCESS_KEY_ID"),
		YCS3SecretKey: os.Getenv("YC_S3_SECRET_ACCESS_KEY"),
		YCS3Endpoint:  os.Getenv("YC_S3_ENDPOINT"),
		YCS3Region:    os.Getenv("YC_S3_REGION"),
	}
	for name, v := range map[string]string{
		"YC_API_KEY":              c.YCAPIKey,
		"YC_BUCKET":               c.YCBucket,
		"YC_S3_ACCESS_KEY_ID":     c.YCS3AccessKey,
		"YC_S3_SECRET_ACCESS_KEY": c.YCS3SecretKey,
	} {
		if v == "" {
			return nil, errors.New(name + " is required")
		}
	}
	if c.YCS3Endpoint == "" {
		c.YCS3Endpoint = "https://storage.yandexcloud.net"
	}
	if c.YCS3Region == "" {
		c.YCS3Region = "ru-central1"
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
