package summary

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

const completionURL = "https://llm.api.cloud.yandex.net/foundationModels/v1/completion"

type YandexGPT struct {
	APIKey   string
	FolderID string
	Model    string // например "yandexgpt/latest" (Pro) или "yandexgpt-lite/latest"
	HTTP     *http.Client
}

func NewYandexGPT(apiKey, folderID, model string) *YandexGPT {
	return &YandexGPT{
		APIKey:   apiKey,
		FolderID: folderID,
		Model:    model,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
	}
}

type message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type completionRequest struct {
	ModelURI          string `json:"modelUri"`
	CompletionOptions struct {
		Stream      bool    `json:"stream"`
		Temperature float64 `json:"temperature"`
		MaxTokens   string  `json:"maxTokens"`
	} `json:"completionOptions"`
	Messages []message `json:"messages"`
}

type completionResponse struct {
	Result struct {
		Alternatives []struct {
			Message message `json:"message"`
			Status  string  `json:"status"`
		} `json:"alternatives"`
	} `json:"result"`
}

// Summarize превращает расшифровку голосового в структурированную дневниковую запись.
// sentAt — время отправки голосового, по нему модель пересчитывает «вчера»/«сегодня» в дату.
func (g *YandexGPT) Summarize(ctx context.Context, transcript string, sentAt time.Time) (string, error) {
	var req completionRequest
	req.ModelURI = fmt.Sprintf("gpt://%s/%s", g.FolderID, g.Model)
	req.CompletionOptions.Temperature = 0.3
	req.CompletionOptions.MaxTokens = "3000"
	req.Messages = []message{
		{Role: "system", Text: SystemPrompt},
		{Role: "user", Text: fmt.Sprintf("Дата отправки голосового: %s.\n\nРасшифровка:\n%s", sentAt.Format("02.01.2006"), transcript)},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Authorization", "Api-Key "+g.APIKey)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("x-folder-id", g.FolderID)
	// просим не логировать запросы на стороне Яндекса (дневник — личные данные)
	hreq.Header.Set("x-data-logging-enabled", "false")

	resp, err := g.HTTP.Do(hreq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("yandexgpt status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var out completionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if len(out.Result.Alternatives) == 0 {
		return "", fmt.Errorf("yandexgpt: empty response")
	}
	alt := out.Result.Alternatives[0]
	if alt.Status == "ALTERNATIVE_STATUS_CONTENT_FILTER" {
		return "", fmt.Errorf("yandexgpt: response blocked by content filter")
	}
	text := strings.TrimSpace(alt.Message.Text)
	if text == "" {
		return "", fmt.Errorf("yandexgpt: empty text")
	}
	return text, nil
}
