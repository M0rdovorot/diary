// Package cleanup удаляет устаревшие локальные аудиофайлы.
package cleanup

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sweep удаляет в dir обычные файлы *.ogg, не менявшиеся дольше maxAge. Подкаталоги не трогает.
// Возвращает число удалённых файлов.
func Sweep(dir string, maxAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".ogg") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// Run подметает папку при старте и затем каждые interval, пока жив ctx.
// Это страховка для файлов, оставшихся после неудачной обработки (успешно обработанные удаляются сразу).
func Run(ctx context.Context, log *slog.Logger, dir string, maxAge, interval time.Duration) {
	sweep := func() {
		n, err := Sweep(dir, maxAge, time.Now())
		switch {
		case err != nil:
			log.Warn("cleanup data dir", "dir", dir, "err", err)
		case n > 0:
			log.Info("cleanup data dir", "dir", dir, "removed", n)
		}
	}
	sweep()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
