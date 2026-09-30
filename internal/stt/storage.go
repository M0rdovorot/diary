package stt

import (
	"context"
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Storage — приватный бакет Yandex Object Storage, откуда SpeechKit забирает аудио.
type Storage struct {
	client *minio.Client
	bucket string
}

type StorageConfig struct {
	Endpoint  string // например https://storage.yandexcloud.net
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

func NewStorage(c StorageConfig) (*Storage, error) {
	host, secure := trimScheme(c.Endpoint)
	cl, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
		Secure: secure,
		Region: c.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &Storage{client: cl, bucket: c.Bucket}, nil
}

func (s *Storage) Upload(ctx context.Context, key, path string) error {
	_, err := s.client.FPutObject(ctx, s.bucket, key, path, minio.PutObjectOptions{ContentType: "audio/ogg"})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}

// PresignedURL выдаёт временную ссылку на чтение приватного объекта.
func (s *Storage) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return u.String(), nil
}

func (s *Storage) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

func trimScheme(endpoint string) (host string, secure bool) {
	switch {
	case len(endpoint) >= 8 && endpoint[:8] == "https://":
		return endpoint[8:], true
	case len(endpoint) >= 7 && endpoint[:7] == "http://":
		return endpoint[7:], false
	}
	return endpoint, true
}
