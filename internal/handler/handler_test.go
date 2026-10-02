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

func TestParseReplacements(t *testing.T) {
	pairs, bad := parseReplacements("катя => Катя\n  зал ов -> зал  \nсвоё → своё\n\nбез разделителя\n => пусто\nслово =>\nа -> б => в")
	want := []replacement{{"катя", "Катя"}, {"зал ов", "зал"}, {"своё", "своё"}, {"слово", ""}, {"а", "б => в"}}
	if len(pairs) != len(want) {
		t.Fatalf("pairs: %+v", pairs)
	}
	for i := range want {
		if pairs[i] != want[i] {
			t.Errorf("pair %d: %+v, want %+v", i, pairs[i], want[i])
		}
	}
	if len(bad) != 2 {
		t.Errorf("bad: %v", bad)
	}
}

func TestApplyPairs(t *testing.T) {
	out, report, changed := applyPairs("Катя и катя пошли. Ещё КАТЯ.", []replacement{{"катя", "Катю"}, {"нет такого", "x"}})
	if !changed || out != "Катю и Катю пошли. Ещё Катю." {
		t.Fatalf("out=%q changed=%v", out, changed)
	}
	if len(report) != 2 || !strings.Contains(report[0], ": 3") || !strings.Contains(report[1], "не найдено") {
		t.Errorf("report: %v", report)
	}
	// спецсимволы regexp в «было» экранируются
	if out, _, ok := applyPairs("цена (100) руб.", []replacement{{"(100)", "200"}}); !ok || out != "цена 200 руб." {
		t.Errorf("спецсимволы: %q", out)
	}
	if _, _, ok := applyPairs("текст", []replacement{{"нету", "x"}}); ok {
		t.Error("ничего не найдено — changed должно быть false")
	}
}
