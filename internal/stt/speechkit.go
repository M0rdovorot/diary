package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	recognizeURL  = "https://transcribe.api.cloud.yandex.net/speech/stt/v2/longRunningRecognize"
	operationsURL = "https://operation.api.cloud.yandex.net/operations/"
)

type SpeechKit struct {
	APIKey       string
	HTTP         *http.Client
	PollInterval time.Duration // пауза между опросами статуса
}

func NewSpeechKit(apiKey string) *SpeechKit {
	return &SpeechKit{
		APIKey:       apiKey,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		PollInterval: 5 * time.Second,
	}
}

type recognizeRequest struct {
	Config struct {
		Specification struct {
			LanguageCode  string `json:"languageCode"`
			Model         string `json:"model"`
			AudioEncoding string `json:"audioEncoding"`
		} `json:"specification"`
	} `json:"config"`
	Audio struct {
		URI string `json:"uri"`
	} `json:"audio"`
}

type operation struct {
	ID       string `json:"id"`
	Done     bool   `json:"done"`
	Error    *opErr `json:"error"`
	Response struct {
		Chunks []struct {
			Alternatives []struct {
				Text string `json:"text"`
			} `json:"alternatives"`
		} `json:"chunks"`
	} `json:"response"`
}

type opErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Submit запускает асинхронное распознавание и возвращает ID операции.
func (s *SpeechKit) Submit(ctx context.Context, audioURL string) (string, error) {
	var req recognizeRequest
	req.Config.Specification.LanguageCode = "ru-RU"
	req.Config.Specification.Model = "general"
	req.Config.Specification.AudioEncoding = "OGG_OPUS"
	req.Audio.URI = audioURL

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	var op operation
	if err := s.do(ctx, http.MethodPost, recognizeURL, body, &op); err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	if op.ID == "" {
		return "", fmt.Errorf("submit: empty operation id")
	}
	return op.ID, nil
}

// Wait опрашивает операцию, пока она не завершится или не истечёт ctx.
func (s *SpeechKit) Wait(ctx context.Context, opID string) (string, error) {
	t := time.NewTicker(s.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.C:
		}

		var op operation
		if err := s.do(ctx, http.MethodGet, operationsURL+opID, nil, &op); err != nil {
			// сетевые сбои и 5xx не должны ронять длинное ожидание — пробуем ещё раз
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		if !op.Done {
			continue
		}
		if op.Error != nil {
			return "", fmt.Errorf("recognition failed: code %d: %s", op.Error.Code, op.Error.Message)
		}
		return joinText(op), nil
	}
}

// Recognize — Submit + Wait.
func (s *SpeechKit) Recognize(ctx context.Context, audioURL string) (string, error) {
	id, err := s.Submit(ctx, audioURL)
	if err != nil {
		return "", err
	}
	return s.Wait(ctx, id)
}

func joinText(op operation) string {
	var parts []string
	for _, c := range op.Response.Chunks {
		if len(c.Alternatives) > 0 {
			if t := strings.TrimSpace(c.Alternatives[0].Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, " ")
}

func (s *SpeechKit) do(ctx context.Context, method, url string, body []byte, out any) error {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Api-Key "+s.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}
