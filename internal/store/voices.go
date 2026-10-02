package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("не найдено")

// Voice — сохранённое голосовое. В списках Transcript пуст, заполнен только Preview.
type Voice struct {
	ID            int64
	SentAt        time.Time
	LogicalDate   time.Time
	ReferenceDate time.Time
	DurationSec   int
	Transcript    string
	Preview       string // начало расшифровки (для списков)
	Edited        bool   // расшифровку правили вручную
}

const voiceColumns = `v.id, v.sent_at, v.logical_date, v.reference_date, v.duration_sec,
	left(v.transcript, 120), EXISTS (SELECT 1 FROM transcript_edits e WHERE e.voice_id = v.id)`

func scanVoice(row pgx.Row) (Voice, error) {
	var v Voice
	err := row.Scan(&v.ID, &v.SentAt, &v.LogicalDate, &v.ReferenceDate, &v.DurationSec, &v.Preview, &v.Edited)
	return v, err
}

func (s *Store) GetVoice(ctx context.Context, id int64) (Voice, error) {
	v, err := scanVoice(s.pool.QueryRow(ctx, `SELECT `+voiceColumns+` FROM voice_messages v WHERE v.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Voice{}, ErrNotFound
	}
	if err != nil {
		return Voice{}, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT transcript FROM voice_messages WHERE id = $1`, id).Scan(&v.Transcript); err != nil {
		return Voice{}, err
	}
	return v, nil
}

func (s *Store) listVoices(ctx context.Context, where string, limit int, args ...any) ([]Voice, error) {
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(
		`SELECT %s FROM voice_messages v %s ORDER BY v.sent_at DESC LIMIT $%d`, voiceColumns, where, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Voice
	for rows.Next() {
		v, err := scanVoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RecentVoices — последние голосовые, новые первыми.
func (s *Store) RecentVoices(ctx context.Context, limit int) ([]Voice, error) {
	return s.listVoices(ctx, "", limit)
}

// VoicesForDay — голосовые, отнесённые к дню или отправленные в этот день дневника.
func (s *Store) VoicesForDay(ctx context.Context, day time.Time, limit int) ([]Voice, error) {
	return s.listVoices(ctx, `WHERE v.reference_date = $1 OR v.logical_date = $1`, limit, day)
}

// UpdateTranscript заменяет расшифровку, сохранив прежнюю версию в transcript_edits.
func (s *Store) UpdateTranscript(ctx context.Context, voiceID int64, newText string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var old string
	err = tx.QueryRow(ctx, `SELECT transcript FROM voice_messages WHERE id = $1 FOR UPDATE`, voiceID).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if old == newText {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO transcript_edits (voice_id, old_text, new_text) VALUES ($1, $2, $3)`,
		voiceID, old, newText); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE voice_messages SET transcript = $2 WHERE id = $1`, voiceID, newText); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Setting возвращает значение настройки; ok == false, если она не задана.
func (s *Store) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM settings WHERE key = $1`, key)
	return err
}
