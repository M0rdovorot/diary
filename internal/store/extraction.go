package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"diary/internal/entry"
)

// SavedSegment — сегмент после сохранения. Date == nil, если день ещё надо уточнить у пользователя.
type SavedSegment struct {
	ID      int64
	Segment entry.Segment
	Date    *time.Time
}

// ExtractionInput — результат разбора голосового моделью.
type ExtractionInput struct {
	VoiceID       int64
	RefDate       time.Time // день, к которому отнесено голосовое (от него считаются «сегодня»/«вчера»)
	Today         time.Time // день дневника на момент отправки: даты позже него считаются ошибкой модели
	Model         string
	PromptVersion string
	Raw           string // сырой ответ модели
	Segments      []entry.Segment
}

// SaveExtraction в одной транзакции сохраняет ответ модели, его сегменты и, для сегментов
// с надёжной датой, наблюдения. Сегменты без надёжной даты сохраняются как pending_date.
func (s *Store) SaveExtraction(ctx context.Context, in ExtractionInput) ([]SavedSegment, error) {
	return s.saveExtraction(ctx, in, false)
}

// ReplaceExtraction делает то же, но сначала удаляет всё, что было получено из этого голосового
// раньше (извлечения, сегменты и наблюдения). Нужен после правки расшифровки. Даты, которые
// пользователь уточнял у прежних сегментов, при этом теряются.
func (s *Store) ReplaceExtraction(ctx context.Context, in ExtractionInput) ([]SavedSegment, error) {
	return s.saveExtraction(ctx, in, true)
}

func (s *Store) saveExtraction(ctx context.Context, in ExtractionInput, replace bool) ([]SavedSegment, error) {
	schema, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit откат — no-op

	if replace {
		// сегменты и наблюдения удаляются каскадом
		if _, err := tx.Exec(ctx, `DELETE FROM extractions WHERE voice_id = $1`, in.VoiceID); err != nil {
			return nil, fmt.Errorf("delete old extractions: %w", err)
		}
	}

	var extID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO extractions (voice_id, model, prompt_version, raw_response)
		VALUES ($1, $2, $3, $4) RETURNING id`, in.VoiceID, in.Model, in.PromptVersion, in.Raw).Scan(&extID); err != nil {
		return nil, fmt.Errorf("insert extraction: %w", err)
	}

	saved := make([]SavedSegment, 0, len(in.Segments))
	for _, seg := range in.Segments {
		payload, err := json.Marshal(seg.Values)
		if err != nil {
			return nil, err
		}

		date, ok := seg.ResolvedDate(in.Today)
		status, source := "pending_date", seg.DateSource
		var dateArg any
		if ok {
			status, dateArg = "resolved", date
		}

		var segID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO segments (extraction_id, voice_id, entry_date, date_source, status, payload)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			extID, in.VoiceID, dateArg, source, status, payload).Scan(&segID); err != nil {
			return nil, fmt.Errorf("insert segment: %w", err)
		}

		out := SavedSegment{ID: segID, Segment: seg}
		if ok {
			if err := insertObservations(ctx, tx, schema, segID, in.VoiceID, date, in.RefDate, seg.Values); err != nil {
				return nil, err
			}
			out.Date = &date
		}
		saved = append(saved, out)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return saved, nil
}

// insertObservations создаёт по строке на каждое упомянутое поле сегмента.
// relation: same_day — рассказ о дне, к которому отнесено голосовое (ref), retro — о другом дне.
func insertObservations(ctx context.Context, tx pgx.Tx, schema entry.Schema, segID, voiceID int64, date, ref time.Time, values map[string]any) error {
	relation := "retro"
	if date.Equal(ref) {
		relation = "same_day"
	}
	for _, f := range schema.All { // порядок схемы — детерминированный
		v, ok := values[f.Key]
		if !ok {
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO observations (segment_id, voice_id, entry_date, field, value, relation)
			VALUES ($1, $2, $3, $4, $5, $6)`, segID, voiceID, date, f.Key, b, relation); err != nil {
			return fmt.Errorf("insert observation %s: %w", f.Key, err)
		}
	}
	return nil
}
