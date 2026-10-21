package handler

import (
	"strings"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func rowTexts(row []buttonView) string {
	var out []string
	for _, b := range row {
		out = append(out, b.Text)
	}
	return strings.Join(out, "|")
}

type buttonView = struct {
	Text, Style, Data string
}

func view(t *testing.T, month, today, selected time.Time) [][]buttonView {
	t.Helper()
	var rows [][]buttonView
	for _, r := range pickKeyboard(month, today, selected).InlineKeyboard {
		var row []buttonView
		for _, b := range r {
			row = append(row, buttonView{b.Text, b.Style, b.CallbackData})
		}
		rows = append(rows, row)
	}
	return rows
}

func TestPickKeyboardLayout(t *testing.T) {
	today := day(2026, 10, 21) // среда
	rows := view(t, today, today, time.Time{})

	if got := rowTexts(rows[0]); got != "‹|Октябрь 2026| " {
		t.Errorf("заголовок: %q (в текущем месяце листать вперёд нельзя)", got)
	}
	if got := rowTexts(rows[1]); got != "Пн|Вт|Ср|Чт|Пт|Сб|Вс" {
		t.Errorf("дни недели: %q", got)
	}
	// 1 октября 2026 — четверг: три пустые клетки, затем 1, 2, 3, 4
	if got := rowTexts(rows[2]); got != " | | |1|2|3|4" {
		t.Errorf("первая неделя: %q", got)
	}
	// после сегодняшнего дня выбирать нельзя; 31 октября — суббота, воскресенье пустое
	last := rows[len(rows)-1]
	if got := rowTexts(last); got != "·|·|·|·|·|·| " {
		t.Errorf("последняя неделя: %q", got)
	}
	for _, r := range rows[2:] {
		for _, b := range r {
			switch {
			case b.Text == "21":
				if b.Style != styleToday || b.Data != "w:s:2026-10-21" {
					t.Errorf("сегодня: %+v", b)
				}
			case b.Text == "·":
				if b.Data != "w:n" {
					t.Errorf("будущий день нажимается: %+v", b)
				}
			case b.Text == "20":
				if b.Style != "" || b.Data != "w:s:2026-10-20" {
					t.Errorf("обычный день: %+v", b)
				}
			}
		}
	}
	for _, r := range rows[1:] {
		if len(r) != 7 {
			t.Errorf("строка календаря должна быть из 7 клеток: %v", rowTexts(r))
		}
	}
}

func TestPickKeyboardSelectedAndFooter(t *testing.T) {
	today := day(2026, 10, 21)
	sel := day(2026, 9, 5)

	// другой месяц, рабочий день выбран: ✓ на выбранном, листать можно в обе стороны, есть «Сбросить»
	rows := view(t, sel, today, sel)
	if got := rowTexts(rows[0]); got != "‹|Сентябрь 2026|›" {
		t.Errorf("заголовок: %q", got)
	}
	found := false
	for _, r := range rows[2:] {
		for _, b := range r {
			if b.Text == "✓5" {
				found = b.Style == styleSelected && b.Data == "w:s:2026-09-05"
			}
		}
	}
	if !found {
		t.Errorf("выбранный день не выделен: %v", rows)
	}
	if f := rows[len(rows)-1]; len(f) != 1 || f[0].Data != "w:r" {
		t.Errorf("нет кнопки сброса: %v", f)
	}

	// рабочий день не выбран, смотрим прошлый месяц: кнопка возврата к сегодняшнему
	rows = view(t, sel, today, time.Time{})
	if f := rows[len(rows)-1]; len(f) != 1 || f[0].Data != "w:m:2026-10" || f[0].Style != styleToday {
		t.Errorf("нет кнопки «К сегодняшнему дню»: %v", f)
	}

	// предел в прошлое: за пять лет назад листать нельзя
	old := view(t, today.AddDate(-pickYears, 0, 0), today, time.Time{})
	if old[0][0].Data != "w:n" {
		t.Errorf("можно листать глубже пяти лет: %v", old[0])
	}
}

func TestPickCalendarFlow(t *testing.T) {
	e := newEnv(t)
	today := e.h.Clock.Now()
	prev := monthStart(today).AddDate(0, -1, 0)

	e.say("/date")
	need(t, e.tg.last(), "Рабочий день не выбран", "зелёная кнопка")
	header := e.tg.lastButtons()[0]
	if header[1].text != ruMonths[today.Month()-1]+" "+today.Format("2006") {
		t.Fatalf("заголовок %q", header[1].text)
	}
	if header[2].data != "w:n" {
		t.Fatalf("вперёд из текущего месяца листать нельзя: %+v", header)
	}
	var todayBtn button
	for _, row := range e.tg.lastButtons() {
		for _, b := range row {
			if b.style == styleToday {
				todayBtn = b
			}
		}
	}
	if todayBtn.data != "w:s:"+today.Format("2006-01-02") {
		t.Fatalf("сегодняшний день не выделен: %+v", todayBtn)
	}

	// листаем назад и выбираем 15-е прошлого месяца
	e.press(header[0].data)
	if got := e.tg.lastButtons()[0][1].text; got != ruMonths[prev.Month()-1]+" "+prev.Format("2006") {
		t.Fatalf("после «‹» месяц %q", got)
	}
	pick := "w:s:" + prev.AddDate(0, 0, 14).Format("2006-01-02")
	e.press(e.buttonWith(pick))
	want := prev.AddDate(0, 0, 14)
	need(t, e.tg.last(), "Рабочий день: "+want.Format("02.01.2006"))
	if v, ok, _ := e.db.Setting(e.ctx, referenceDateKey); !ok || v != want.Format("2006-01-02") {
		t.Fatalf("рабочий день не сохранён: %q %v", v, ok)
	}

	// /date открывается на месяце выбранного дня, день отмечен ✓
	e.say("/date")
	need(t, e.tg.last(), "Рабочий день: "+want.Format("02.01.2006"))
	e.buttonText("✓15")

	// будущий день и мусор не принимаются
	e.press("w:s:2099-01-01")
	e.press("w:s:вчера")
	if v, _, _ := e.db.Setting(e.ctx, referenceDateKey); v != want.Format("2006-01-02") {
		t.Fatalf("настройка изменилась: %q", v)
	}

	// сегодняшний день = сброс; кнопка «Сбросить» тоже
	e.press(pickCallbackPrefix + "s:" + today.Format("2006-01-02"))
	need(t, e.tg.last(), "Рабочий день сброшен")
	e.say("/date")
	e.press("w:s:" + prev.AddDate(0, 0, 2).Format("2006-01-02")) // нажатие в старом сообщении тоже работает
	e.press("w:r")
	need(t, e.tg.last(), "Рабочий день сброшен")
	if _, ok, _ := e.db.Setting(e.ctx, referenceDateKey); ok {
		t.Fatal("настройка должна быть сброшена")
	}
}
