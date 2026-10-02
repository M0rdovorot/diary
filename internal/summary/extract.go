package summary

import (
	"context"
	"fmt"
	"time"

	"diary/internal/entry"
)

// Extract просит модель разложить расшифровку на сегменты по дням.
// refDate — день, к которому отнесено голосовое: выбранный /date или день отправки (для пересчёта «вчера»/«сегодня»).
// Возвращает и сырой ответ модели (его сохраняем в БД как есть). Если ответ не разобрался,
// запрос повторяется один раз.
func (g *YandexGPT) Extract(ctx context.Context, transcript string, refDate time.Time) (raw string, segs []entry.Segment, err error) {
	user := fmt.Sprintf("Опорная дата (день, к которому автор относит это голосовое; «сегодня» = эта дата): %s.\n\nРасшифровка:\n%s",
		refDate.Format("2006-01-02"), transcript)

	for attempt := 0; attempt < 2; attempt++ {
		raw, err = g.Complete(ctx, ExtractPrompt, user)
		if err != nil {
			return "", nil, err
		}
		segs, err = entry.ParseExtraction(raw)
		if err == nil {
			return raw, segs, nil
		}
	}
	return raw, nil, fmt.Errorf("ответ модели не разобрался: %w", err)
}
