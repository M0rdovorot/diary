package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"diary/internal/entry"
)

// DayObservations возвращает все наблюдения за день в хронологическом порядке отправки голосовых.
func (s *Store) DayObservations(ctx context.Context, date time.Time) ([]entry.Observation, error) {
	schema, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT o.field, o.value, o.relation, v.id, v.sent_at, v.reference_date, v.confirmed
		FROM observations o
		JOIN voice_messages v ON v.id = o.voice_id
		WHERE o.entry_date = $1
		ORDER BY v.sent_at, o.id`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []entry.Observation
	for rows.Next() {
		var (
			field, relation string
			raw             []byte
			src             entry.Source
		)
		if err := rows.Scan(&field, &raw, &relation, &src.VoiceID, &src.SentAt, &src.Logical, &src.Confirmed); err != nil {
			return nil, err
		}
		src.Relation = relation
		if v, ok := entry.DecodeValue(schema, field, raw); ok {
			out = append(out, entry.Observation{Field: field, Value: v, Src: src})
		}
	}
	return out, rows.Err()
}

// PendingSegment — сегмент, ждущий уточнения даты.
type PendingSegment struct {
	ID      int64
	RefDate time.Time // день, к которому отнесено голосовое
	Segment entry.Segment
}

func (s *Store) PendingSegments(ctx context.Context) ([]PendingSegment, error) {
	schema, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT sg.id, v.reference_date, sg.payload
		FROM segments sg JOIN voice_messages v ON v.id = sg.voice_id
		WHERE sg.status = 'pending_date'
		ORDER BY sg.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingSegment
	for rows.Next() {
		var p PendingSegment
		var payload []byte
		if err := rows.Scan(&p.ID, &p.RefDate, &payload); err != nil {
			return nil, err
		}
		p.Segment = entry.Segment{DateSource: entry.SourceUnknown, Values: entry.DecodeValues(schema, payload)}
		out = append(out, p)
	}
	return out, rows.Err()
}

var ErrNotPending = errors.New("сегмент не найден или дата уже уточнена")

// ResolveSegment закрепляет уточнённую пользователем дату за сегментом и создаёт наблюдения.
func (s *Store) ResolveSegment(ctx context.Context, segID int64, date time.Time) error {
	schema, err := s.Schema(ctx)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		voiceID int64
		ref     time.Time
		payload []byte
	)
	err = tx.QueryRow(ctx, `
		SELECT sg.voice_id, v.reference_date, sg.payload
		FROM segments sg JOIN voice_messages v ON v.id = sg.voice_id
		WHERE sg.id = $1 AND sg.status = 'pending_date'
		FOR UPDATE OF sg`, segID).Scan(&voiceID, &ref, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotPending
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE segments SET entry_date = $2, date_source = 'user', status = 'resolved' WHERE id = $1`,
		segID, date); err != nil {
		return fmt.Errorf("resolve segment: %w", err)
	}
	if err := insertObservations(ctx, tx, schema, segID, voiceID, date, ref, entry.DecodeValues(schema, payload)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DaySummary — день календаря, за который есть данные в карточке.
type DaySummary struct {
	Date        time.Time
	Entries     int // сколько записей попало в карточку дня
	Unconfirmed int // из них с неподтверждённой расшифровкой
}

// MonthDays возвращает дни из [from, to], у которых есть наблюдения (то есть непустая карточка),
// по возрастанию даты. Записи без уточнённой даты в карточки не попадают и здесь не считаются.
func (s *Store) MonthDays(ctx context.Context, from, to time.Time) ([]DaySummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.entry_date, count(DISTINCT v.id), count(DISTINCT v.id) FILTER (WHERE NOT v.confirmed)
		FROM observations o
		JOIN voice_messages v ON v.id = o.voice_id
		WHERE o.entry_date BETWEEN $1 AND $2
		GROUP BY o.entry_date
		ORDER BY o.entry_date`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DaySummary
	for rows.Next() {
		var d DaySummary
		if err := rows.Scan(&d.Date, &d.Entries, &d.Unconfirmed); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DayVoices — записи, из которых собрана карточка дня (есть наблюдения за этот день), с полной
// расшифровкой, в порядке отправки.
func (s *Store) DayVoices(ctx context.Context, date time.Time) ([]Voice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+voiceColumns+`, v.transcript
		FROM voice_messages v
		WHERE EXISTS (SELECT 1 FROM observations o WHERE o.voice_id = v.id AND o.entry_date = $1)
		ORDER BY v.sent_at, v.id`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Voice
	for rows.Next() {
		var v Voice
		if err := rows.Scan(&v.ID, &v.SentAt, &v.LogicalDate, &v.ReferenceDate, &v.DurationSec, &v.Preview,
			&v.Edited, &v.Source, &v.Confirmed, &v.Transcript); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
