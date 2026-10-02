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
	Source        string // voice | text
	Confirmed     bool   // расшифровку подтвердил пользователь
}

const voiceColumns = `v.id, v.sent_at, v.logical_date, v.reference_date, v.duration_sec,
	left(v.transcript, 120), EXISTS (SELECT 1 FROM transcript_edits e WHERE e.voice_id = v.id),
	v.source, v.confirmed`

func scanVoice(row pgx.Row) (Voice, error) {
	var v Voice
	err := row.Scan(&v.ID, &v.SentAt, &v.LogicalDate, &v.ReferenceDate, &v.DurationSec, &v.Preview, &v.Edited, &v.Source, &v.Confirmed)
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

// UnconfirmedVoices — записи с неподтверждённой расшифровкой, новые первыми.
func (s *Store) UnconfirmedVoices(ctx context.Context, limit int) ([]Voice, error) {
	return s.listVoices(ctx, `WHERE NOT v.confirmed`, limit)
}

// CountUnconfirmed — сколько записей ждут подтверждения расшифровки.
func (s *Store) CountUnconfirmed(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM voice_messages WHERE NOT confirmed`).Scan(&n)
	return n, err
}

// ConfirmVoice помечает расшифровку подтверждённой.
func (s *Store) ConfirmVoice(ctx context.Context, voiceID int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE voice_messages SET confirmed = true WHERE id = $1`, voiceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateTranscript заменяет расшифровку, сохранив прежнюю версию в transcript_edits.
// Правка считается подтверждением: пользователь прочитал и исправил текст.
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
		_, err := tx.Exec(ctx, `UPDATE voice_messages SET confirmed = true WHERE id = $1`, voiceID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO transcript_edits (voice_id, old_text, new_text) VALUES ($1, $2, $3)`,
		voiceID, old, newText); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE voice_messages SET transcript = $2, confirmed = true WHERE id = $1`, voiceID, newText); err != nil {
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

// VoiceImpact — что будет удалено вместе с записью (или уже удалено).
type VoiceImpact struct {
	Segments     int
	Observations int
	Edits        int         // сколько правок расшифровки в истории
	Days         []time.Time // дни, в карточках которых были данные из этой записи
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func voiceImpact(ctx context.Context, q queryer, id int64) (VoiceImpact, error) {
	var imp VoiceImpact
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM voice_messages WHERE id = $1)`, id).Scan(&exists); err != nil {
		return imp, err
	}
	if !exists {
		return imp, ErrNotFound
	}
	err := q.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM segments WHERE voice_id = $1),
		       (SELECT count(*) FROM observations WHERE voice_id = $1),
		       (SELECT count(*) FROM transcript_edits WHERE voice_id = $1)`, id).Scan(&imp.Segments, &imp.Observations, &imp.Edits)
	if err != nil {
		return imp, err
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT entry_date FROM observations WHERE voice_id = $1 ORDER BY entry_date`, id)
	if err != nil {
		return imp, err
	}
	defer rows.Close()
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return imp, err
		}
		imp.Days = append(imp.Days, d)
	}
	return imp, rows.Err()
}

// VoiceImpact показывает, что удалится вместе с записью. ErrNotFound — записи нет.
func (s *Store) VoiceImpact(ctx context.Context, id int64) (VoiceImpact, error) {
	return voiceImpact(ctx, s.pool, id)
}

// DeleteVoice безвозвратно удаляет запись вместе с расшифровкой, историей правок, извлечениями,
// сегментами и наблюдениями (каскадом). Остальные записи тех же дней не затрагиваются: карточки
// дней собираются из наблюдений и пересоберутся без неё. Возвращает, что было удалено.
func (s *Store) DeleteVoice(ctx context.Context, id int64) (VoiceImpact, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return VoiceImpact{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	imp, err := voiceImpact(ctx, tx, id)
	if err != nil {
		return imp, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM voice_messages WHERE id = $1`, id); err != nil {
		return imp, fmt.Errorf("delete voice: %w", err)
	}
	return imp, tx.Commit(ctx)
}
