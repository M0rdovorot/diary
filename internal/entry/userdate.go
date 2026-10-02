package entry

import (
	"errors"
	"strings"
	"time"
)

// ParseUserDate разбирает дату, введённую пользователем: «05.09», «05.09.2026», «05.09.26»,
// «сегодня», «вчера», «позавчера». ref — сегодняшний день дневника. Дата в будущем и старше
// пяти лет — ошибка. Если год не указан и дата выходит в будущее, берётся прошлый год.
func ParseUserDate(s string, ref time.Time) (time.Time, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "сегодня":
		return ref, nil
	case "вчера":
		return ref.AddDate(0, 0, -1), nil
	case "позавчера":
		return ref.AddDate(0, 0, -2), nil
	}

	var d time.Time
	var err error
	if d, err = time.Parse("02.01.2006", s); err != nil {
		if d, err = time.Parse("02.01.06", s); err != nil {
			d, err = time.Parse("02.01", s)
			if err != nil {
				return time.Time{}, errors.New("не понял дату")
			}
			d = time.Date(ref.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
			if d.After(ref) {
				d = d.AddDate(-1, 0, 0)
			}
		}
	}
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	if d.After(ref) {
		return time.Time{}, errors.New("эта дата в будущем")
	}
	if d.Before(ref.AddDate(-5, 0, 0)) {
		return time.Time{}, errors.New("эта дата слишком давняя")
	}
	return d, nil
}
