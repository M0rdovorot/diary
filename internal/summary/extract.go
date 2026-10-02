package summary

import (
	"context"
	"fmt"
	"time"

	"diary/internal/entry"
)

// Extract просит модель разложить расшифровку на сегменты по дням.
// refDate — день, к которому отнесено голосовое: выбранный /date или день отправки (для пересчёта «вчера»/«сегодня»).
// schema — текущий набор категорий: по нему строится промпт и разбирается ответ.
// Возвращает сырой ответ модели (его сохраняем в БД как есть) и версию промпта. Если ответ не
// разобрался, запрос повторяется один раз.
func (g *YandexGPT) Extract(ctx context.Context, transcript string, refDate time.Time, schema entry.Schema) (raw string, segs []entry.Segment, version string, err error) {
	prompt, version := BuildExtractPrompt(schema)
	user := fmt.Sprintf("Опорная дата (день, к которому автор относит это голосовое; «сегодня» = эта дата): %s.\n\nРасшифровка:\n%s",
		refDate.Format("2006-01-02"), transcript)

	for attempt := 0; attempt < 2; attempt++ {
		raw, err = g.Complete(ctx, prompt, user)
		if err != nil {
			return "", nil, version, err
		}
		segs, err = entry.ParseExtraction(raw, schema)
		if err == nil {
			return raw, segs, version, nil
		}
	}
	return raw, nil, version, fmt.Errorf("ответ модели не разобрался: %w", err)
}
