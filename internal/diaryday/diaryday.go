// Package diaryday определяет «день дневника»: календарный день со сдвинутой границей,
// чтобы голосовое в 01:00 относилось к предыдущему дню.
package diaryday

import "time"

type Clock struct {
	Loc        *time.Location
	CutoffHour int // до этого часа (не включая) момент относится к предыдущему дню
}

func NewClock(tz string, cutoffHour int) (Clock, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Clock{}, err
	}
	return Clock{Loc: loc, CutoffHour: cutoffHour}, nil
}

// LogicalDate возвращает день дневника для момента t (время 00:00 UTC с нужными год/месяц/день).
func (c Clock) LogicalDate(t time.Time) time.Time {
	shifted := t.In(c.Loc).Add(-time.Duration(c.CutoffHour) * time.Hour)
	return time.Date(shifted.Year(), shifted.Month(), shifted.Day(), 0, 0, 0, 0, time.UTC)
}

// Now — текущий день дневника.
func (c Clock) Now() time.Time { return c.LogicalDate(time.Now()) }
