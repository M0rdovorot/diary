package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitMessage(t *testing.T) {
	if got := splitMessage("коротко", 4000); len(got) != 1 || got[0] != "коротко" {
		t.Fatalf("short text: %v", got)
	}

	long := strings.Repeat("слово ", 2000) // 12000 рун
	parts := splitMessage(long, 4000)
	if len(parts) < 3 {
		t.Fatalf("expected >=3 parts, got %d", len(parts))
	}
	var total int
	for _, p := range parts {
		if n := utf8.RuneCountInString(p); n > 4000 {
			t.Fatalf("part too long: %d", n)
		}
		total += len(strings.Fields(p))
	}
	if total != 2000 {
		t.Fatalf("words lost: %d", total)
	}
}
