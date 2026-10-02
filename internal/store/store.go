package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"diary/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate применяет ещё не применённые миграции по порядку имён файлов.
// Каждый файл выполняется одним простым запросом, то есть в неявной транзакции:
// либо весь файл и запись о версии, либо ничего.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		sql := string(body) + fmt.Sprintf("\n;INSERT INTO schema_migrations (version) VALUES ('%s');", name)
		if _, err := conn.Conn().PgConn().Exec(ctx, sql).ReadAll(); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return s.seedCategories(ctx)
}

type VoiceMessage struct {
	ChatID      int64
	MessageID   int64
	SentAt      time.Time
	LogicalDate time.Time // день дневника на момент отправки
	// ReferenceDate — день, к которому голосовое отнесено («рабочий день»); без выбора равен LogicalDate.
	ReferenceDate time.Time
	DurationSec   int
	Transcript    string
	Source        string // voice (по умолчанию) | text
	Confirmed     bool   // расшифровку подтвердил пользователь; для текстовых записей — true
}

// SaveVoice сохраняет голосовое с расшифровкой. Повторная доставка того же сообщения
// Telegram обновляет расшифровку, а не создаёт дубль.
func (s *Store) SaveVoice(ctx context.Context, v VoiceMessage) (int64, error) {
	if v.Source == "" {
		v.Source = "voice"
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO voice_messages (telegram_chat_id, telegram_message_id, sent_at, logical_date, reference_date, duration_sec, transcript, source, confirmed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (telegram_chat_id, telegram_message_id) DO UPDATE SET transcript = EXCLUDED.transcript
		RETURNING id`,
		v.ChatID, v.MessageID, v.SentAt, v.LogicalDate, v.ReferenceDate, v.DurationSec, v.Transcript, v.Source, v.Confirmed).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save voice: %w", err)
	}
	return id, nil
}
