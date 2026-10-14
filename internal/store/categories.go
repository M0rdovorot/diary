package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"diary/internal/entry"
)

var (
	ErrCategoryExists = errors.New("категория с таким ключом уже есть")
	ErrProtected      = errors.New("эту категорию нельзя скрыть")
)

const categoryColumns = `key, title, kind, hint, unit, group_name, position, active`

func scanCategory(row pgx.Row) (entry.Category, error) {
	var c entry.Category
	var kind string
	if err := row.Scan(&c.Key, &c.Title, &kind, &c.Hint, &c.Unit, &c.Group, &c.Position, &c.Active); err != nil {
		return c, err
	}
	c.Kind, _ = entry.ParseKind(kind)
	return c, nil
}

// Categories возвращает все категории (и скрытые) в порядке отображения.
func (s *Store) Categories(ctx context.Context) ([]entry.Category, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories ORDER BY position, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entry.Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Schema — текущий набор категорий.
func (s *Store) Schema(ctx context.Context) (entry.Schema, error) {
	cats, err := s.Categories(ctx)
	if err != nil {
		return entry.Schema{}, err
	}
	return entry.NewSchema(cats), nil
}

// seedCategories добавляет категории по умолчанию, которых ещё нет — в конец списка (так новые
// категории из обновлений не вклиниваются в порядок, который настроил пользователь). Существующие
// (в том числе изменённые или скрытые пользователем) не трогает.
func (s *Store) seedCategories(ctx context.Context) error {
	for _, c := range entry.DefaultCategories() {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO categories (key, title, kind, hint, unit, group_name, position, active)
			VALUES ($1, $2, $3, $4, $5, $6, (SELECT COALESCE(MAX(position), 0) + 10 FROM categories), true)
			ON CONFLICT (key) DO NOTHING`,
			c.Key, c.Title, entry.KindName(c.Kind), c.Hint, c.Unit, c.Group); err != nil {
			return fmt.Errorf("seed category %s: %w", c.Key, err)
		}
	}
	return nil
}

// AddCategory добавляет категорию в конец списка. Key должен быть уже подобран (entry.SlugKey).
func (s *Store) AddCategory(ctx context.Context, c entry.Category) (entry.Category, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO categories (key, title, kind, hint, unit, group_name, position, active)
		VALUES ($1, $2, $3, $4, $5, $6, (SELECT COALESCE(MAX(position), 0) + 10 FROM categories), true)
		RETURNING position`,
		c.Key, c.Title, entry.KindName(c.Kind), c.Hint, c.Unit, c.Group).Scan(&c.Position)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return entry.Category{}, ErrCategoryExists
	}
	if err != nil {
		return entry.Category{}, err
	}
	c.Active = true
	return c, nil
}

// CategoryUpdate — какие свойства категории менять (nil — не менять). Тип не меняется:
// иначе уже сохранённые значения стали бы некорректными.
type CategoryUpdate struct {
	Title  *string
	Hint   *string
	Unit   *string
	Group  *string
	Active *bool
}

func (s *Store) UpdateCategory(ctx context.Context, key string, u CategoryUpdate) error {
	if key == entry.DiaryKey && u.Active != nil && !*u.Active {
		return ErrProtected
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE categories SET
			title      = COALESCE($2, title),
			hint       = COALESCE($3, hint),
			unit       = COALESCE($4, unit),
			group_name = COALESCE($5, group_name),
			active     = COALESCE($6, active)
		WHERE key = $1`, key, u.Title, u.Hint, u.Unit, u.Group, u.Active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveCategory сдвигает активную категорию на одно место вверх (delta < 0) или вниз (delta > 0):
// меняет позицию с соседней активной. На краю списка ничего не делает.
func (s *Store) MoveCategory(ctx context.Context, key string, delta int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `SELECT key, position FROM categories WHERE active ORDER BY position, key FOR UPDATE`)
	if err != nil {
		return err
	}
	type kp struct {
		key string
		pos int
	}
	var list []kp
	for rows.Next() {
		var x kp
		if err := rows.Scan(&x.key, &x.pos); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	idx := -1
	for i, x := range list {
		if x.key == key {
			idx = i
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	other := idx + 1
	if delta < 0 {
		other = idx - 1
	}
	if other < 0 || other >= len(list) {
		return nil
	}
	a, b := list[idx], list[other]
	if _, err := tx.Exec(ctx, `UPDATE categories SET position = $2 WHERE key = $1`, a.key, b.pos); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE categories SET position = $2 WHERE key = $1`, b.key, a.pos); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CategoryUse — сколько данных накоплено по категории.
type CategoryUse struct {
	Observations int
	Days         int
}

// CategoryUsage — по ключу категории: число наблюдений и дней, в которых она упоминалась.
func (s *Store) CategoryUsage(ctx context.Context) (map[string]CategoryUse, error) {
	rows, err := s.pool.Query(ctx, `SELECT field, count(*), count(DISTINCT entry_date) FROM observations GROUP BY field`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CategoryUse{}
	for rows.Next() {
		var f string
		var u CategoryUse
		if err := rows.Scan(&f, &u.Observations, &u.Days); err != nil {
			return nil, err
		}
		out[f] = u
	}
	return out, rows.Err()
}

var ErrNotHidden = errors.New("удалить навсегда можно только скрытую категорию")

// DeleteCategory безвозвратно удаляет скрытую категорию вместе со всеми её наблюдениями.
// Активную удалить нельзя — сначала её скрывают (так случайное нажатие не стирает данные).
// Возвращает, сколько наблюдений стёрто.
func (s *Store) DeleteCategory(ctx context.Context, key string) (int, error) {
	if key == entry.DiaryKey {
		return 0, ErrProtected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var active bool
	if err := tx.QueryRow(ctx, `SELECT active FROM categories WHERE key = $1 FOR UPDATE`, key).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	if active {
		return 0, ErrNotHidden
	}
	tag, err := tx.Exec(ctx, `DELETE FROM observations WHERE field = $1`, key)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM categories WHERE key = $1`, key); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

// DatedObservation — наблюдение с днём, к которому оно относится.
type DatedObservation struct {
	Date time.Time
	entry.Observation
}

// CategoryObservations — все наблюдения одной категории по возрастанию дня, внутри дня — в порядке
// отправки записей. Работает и для скрытых категорий.
func (s *Store) CategoryObservations(ctx context.Context, key string) ([]DatedObservation, error) {
	schema, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT o.entry_date, o.field, o.value, o.relation, v.id, v.sent_at, v.reference_date, v.confirmed
		FROM observations o
		JOIN voice_messages v ON v.id = o.voice_id
		WHERE o.field = $1
		ORDER BY o.entry_date, v.sent_at, o.id`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DatedObservation
	for rows.Next() {
		var (
			d               time.Time
			field, relation string
			raw             []byte
			src             entry.Source
		)
		if err := rows.Scan(&d, &field, &raw, &relation, &src.VoiceID, &src.SentAt, &src.Logical, &src.Confirmed); err != nil {
			return nil, err
		}
		src.Relation = relation
		if v, ok := entry.DecodeValue(schema, field, raw); ok {
			out = append(out, DatedObservation{Date: d, Observation: entry.Observation{Field: field, Value: v, Src: src}})
		}
	}
	return out, rows.Err()
}
