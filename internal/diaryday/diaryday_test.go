package diaryday

import (
	"testing"
	"time"
)

func TestLogicalDate(t *testing.T) {
	c, err := NewClock("Europe/Moscow", 5)
	if err != nil {
		t.Fatal(err)
	}
	msk := c.Loc
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 2, 1, 0, 0, 0, msk), "2026-09-01"},        // ночью — предыдущий день
		{time.Date(2026, 9, 2, 4, 59, 0, 0, msk), "2026-09-01"},       // прямо перед границей
		{time.Date(2026, 9, 2, 5, 0, 0, 0, msk), "2026-09-02"},        // граница — уже новый день
		{time.Date(2026, 9, 2, 23, 30, 0, 0, msk), "2026-09-02"},      // вечер
		{time.Date(2026, 1, 1, 2, 0, 0, 0, msk), "2025-12-31"},        // через границу года
		{time.Date(2026, 9, 1, 22, 30, 0, 0, time.UTC), "2026-09-01"}, // 01:30 МСК 02.09 — вход в UTC
	}
	for _, tc := range cases {
		if got := c.LogicalDate(tc.at).Format("2006-01-02"); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.at, got, tc.want)
		}
	}
}
