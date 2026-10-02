package summary

import (
	"strings"
	"testing"

	"diary/internal/entry"
)

func TestBuildExtractPromptDefault(t *testing.T) {
	prompt, version := BuildExtractPrompt(entry.DefaultSchema())

	for _, want := range []string{
		"hobby — строка. Хобби:", "pet_project — строка. Пэт-проект:",
		"sleep_hours — число (в ч). Часы сна:", "water_ml — число (в мл).",
		"sweets — true/false/null. Сладкое:", "done — список строк. Успел:",
		"date, date_source",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("нет %q в промпте", want)
		}
	}
	// мысли для психолога — только по прямому указанию автора
	line := ""
	for _, l := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(l, "for_psychologist") {
			line = l
		}
	}
	for _, want := range []string{"ТОЛЬКО", "прямо назвал", "Не додумывай", "null"} {
		if !strings.Contains(line, want) {
			t.Errorf("в описании for_psychologist нет %q: %s", want, line)
		}
	}
	if !strings.Contains(prompt, "Не относи информацию к категории по своему усмотрению") {
		t.Error("нет общего правила про прямое указание автора")
	}
	if !strings.HasPrefix(version, promptBase+"-") || len(version) != len(promptBase)+1+8 {
		t.Errorf("версия: %q", version)
	}
}

func TestBuildExtractPromptFollowsSchema(t *testing.T) {
	cats := entry.DefaultCategories()
	for i := range cats {
		if cats[i].Key == "tea" {
			cats[i].Active = false
		}
	}
	cats = append(cats, entry.Category{Key: "running", Title: "Бег", Kind: entry.Number, Unit: "км",
		Hint: "сколько\nпробежал   (строки\nсклеиваются)", Position: 1000, Active: true})
	prompt, version := BuildExtractPrompt(entry.NewSchema(cats))

	if strings.Contains(prompt, "tea — ") {
		t.Error("скрытая категория попала в промпт")
	}
	if !strings.Contains(prompt, "running — число (в км). Бег: сколько пробежал (строки склеиваются)") {
		t.Errorf("пользовательская категория: нет строки в промпте")
	}
	// версия зависит от набора категорий
	_, defVersion := BuildExtractPrompt(entry.DefaultSchema())
	if version == defVersion {
		t.Error("версия промпта должна меняться вместе с категориями")
	}
	// одинаковый набор — одинаковая версия
	if _, again := BuildExtractPrompt(entry.NewSchema(cats)); again != version {
		t.Error("версия нестабильна")
	}
}

func TestOneLineLimits(t *testing.T) {
	long := strings.Repeat("а", 1000)
	if got := oneLine(long, 400); len([]rune(got)) != 400 {
		t.Errorf("длина: %d", len([]rune(got)))
	}
	if got := oneLine("  a \n\t b  ", 10); got != "a b" {
		t.Errorf("%q", got)
	}
}
