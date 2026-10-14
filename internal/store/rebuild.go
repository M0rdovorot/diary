package store

import (
	"context"
	"time"
)

// RebuildPlan — что затронет пересборка записей (повторный разбор текущей схемой категорий).
type RebuildPlan struct {
	VoiceIDs  []int64 // записи в порядке отправки
	Days      int     // сколько дней с наблюдениями затронется
	UserDates int     // вручную уточнённых дат частей записей: при пересборке их придётся уточнить снова
	HiddenObs int     // значений скрытых категорий: при пересборке они пропадут (модель их не извлекает)
}

// RebuildPlanFor строит план пересборки: day != nil — записи, из которых собрана карточка этого
// дня; nil — все записи. Пересборка записи затрагивает и другие дни, о которых она рассказывает.
func (s *Store) RebuildPlanFor(ctx context.Context, day *time.Time) (RebuildPlan, error) {
	var plan RebuildPlan
	var q string
	var args []any
	if day == nil {
		q = `SELECT id FROM voice_messages ORDER BY sent_at, id`
	} else {
		q = `SELECT v.id FROM voice_messages v
		     WHERE EXISTS (SELECT 1 FROM observations o WHERE o.voice_id = v.id AND o.entry_date = $1)
		     ORDER BY v.sent_at, v.id`
		args = append(args, *day)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return plan, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return plan, err
		}
		plan.VoiceIDs = append(plan.VoiceIDs, id)
	}
	if err := rows.Err(); err != nil {
		return plan, err
	}
	rows.Close()
	if len(plan.VoiceIDs) == 0 {
		return plan, nil
	}
	err = s.pool.QueryRow(ctx, `
		SELECT (SELECT count(DISTINCT entry_date) FROM observations WHERE voice_id = ANY($1)),
		       (SELECT count(*) FROM segments WHERE voice_id = ANY($1) AND date_source = 'user'),
		       (SELECT count(*) FROM observations o JOIN categories c ON c.key = o.field
		        WHERE o.voice_id = ANY($1) AND NOT c.active)`, plan.VoiceIDs).Scan(&plan.Days, &plan.UserDates, &plan.HiddenObs)
	return plan, err
}
