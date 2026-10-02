package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweep(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write("old.ogg", 10*24*time.Hour)
	write("fresh.ogg", time.Hour)
	write("old.txt", 10*24*time.Hour)                                      // не .ogg — не трогаем
	if err := os.Mkdir(filepath.Join(dir, "sub.ogg"), 0o755); err != nil { // каталог — не трогаем
		t.Fatal(err)
	}

	n, err := Sweep(dir, 7*24*time.Hour, now)
	if err != nil || n != 1 {
		t.Fatalf("removed=%d err=%v", n, err)
	}
	for name, wantExist := range map[string]bool{"old.ogg": false, "fresh.ogg": true, "old.txt": true, "sub.ogg": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != wantExist {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, wantExist)
		}
	}

	// несуществующая папка — не ошибка
	if n, err := Sweep(filepath.Join(dir, "нет"), time.Hour, now); n != 0 || err != nil {
		t.Errorf("missing dir: %d %v", n, err)
	}
}
