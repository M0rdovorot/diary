package store

import (
	"context"
	"errors"
	"fmt"

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

// seedCategories добавляет категории по умолчанию, которых ещё нет. Существующие (в том числе
// изменённые или скрытые пользователем) не трогает.
func (s *Store) seedCategories(ctx context.Context) error {
	for _, c := range entry.DefaultCategories() {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO categories (key, title, kind, hint, unit, group_name, position, active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, true)
			ON CONFLICT (key) DO NOTHING`,
			c.Key, c.Title, entry.KindName(c.Kind), c.Hint, c.Unit, c.Group, c.Position); err != nil {
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
