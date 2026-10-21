package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Формат callback_data выбора рабочего дня: "w:m:ГГГГ-ММ" — показать месяц, "w:s:ГГГГ-ММ-ДД" — выбрать
// день, "w:r" — сбросить рабочий день, "w:n" — кнопка без действия (заголовок, пустая клетка).
const pickCallbackPrefix = "w:"

// pickYears — насколько далеко в прошлое можно выбрать день (как в entry.ParseUserDate).
const pickYears = 5

// Стили кнопок Bot API: сегодняшний день — зелёный, выбранный рабочий — акцентный.
const (
	styleToday    = "success"
	styleSelected = "primary"
)

var ruMonths = [...]string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
	"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// pickKeyboard — календарь месяца для выбора рабочего дня. today — сегодняшний день дневника,
// selected — выбранный рабочий день (нулевой, если не выбран). Дни после today и старше pickYears
// лет нажать нельзя.
func pickKeyboard(month, today, selected time.Time) *models.InlineKeyboardMarkup {
	month = monthStart(month)
	minDay := today.AddDate(-pickYears, 0, 0)
	noop := func(text string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: text, CallbackData: pickCallbackPrefix + "n"}
	}
	nav := func(text string, m time.Time, ok bool) models.InlineKeyboardButton {
		if !ok {
			return noop(" ")
		}
		return models.InlineKeyboardButton{Text: text, CallbackData: pickCallbackPrefix + "m:" + m.Format("2006-01")}
	}
	prev, next := month.AddDate(0, -1, 0), month.AddDate(0, 1, 0)
	rows := [][]models.InlineKeyboardButton{
		{
			nav("‹", prev, !prev.Before(monthStart(minDay))),
			noop(fmt.Sprintf("%s %d", ruMonths[month.Month()-1], month.Year())),
			nav("›", next, !next.After(monthStart(today))),
		},
		{noop("Пн"), noop("Вт"), noop("Ср"), noop("Чт"), noop("Пт"), noop("Сб"), noop("Вс")},
	}

	row := make([]models.InlineKeyboardButton, 0, 7)
	for i := 0; i < (int(month.Weekday())+6)%7; i++ { // пустые клетки до первого числа (неделя с понедельника)
		row = append(row, noop(" "))
	}
	for d := month; d.Month() == month.Month(); d = d.AddDate(0, 0, 1) {
		var btn models.InlineKeyboardButton
		switch {
		case d.After(today) || d.Before(minDay):
			btn = noop("·")
		case d.Equal(selected):
			btn = models.InlineKeyboardButton{Text: fmt.Sprintf("✓%d", d.Day()), Style: styleSelected,
				CallbackData: pickCallbackPrefix + "s:" + d.Format("2006-01-02")}
		default:
			btn = models.InlineKeyboardButton{Text: fmt.Sprint(d.Day()),
				CallbackData: pickCallbackPrefix + "s:" + d.Format("2006-01-02")}
			if d.Equal(today) {
				btn.Style = styleToday
			}
		}
		row = append(row, btn)
		if len(row) == 7 {
			rows = append(rows, row)
			row = make([]models.InlineKeyboardButton, 0, 7)
		}
	}
	if len(row) > 0 {
		for len(row) < 7 {
			row = append(row, noop(" "))
		}
		rows = append(rows, row)
	}

	switch {
	case !selected.IsZero():
		rows = append(rows, []models.InlineKeyboardButton{{Text: "↺ Сбросить: считать от сегодняшнего дня", CallbackData: pickCallbackPrefix + "r"}})
	case !month.Equal(monthStart(today)):
		rows = append(rows, []models.InlineKeyboardButton{{Text: "К сегодняшнему дню", Style: styleToday,
			CallbackData: pickCallbackPrefix + "m:" + today.Format("2006-01")}})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// askWorkingDay показывает состояние рабочего дня и календарь для выбора.
func (h *Handler) askWorkingDay(ctx context.Context, b *bot.Bot, chatID int64, today, cur time.Time) {
	var text string
	month := today
	if cur.IsZero() {
		text = "Рабочий день не выбран: «сегодня» и «вчера» считаются от даты отправки голосового (сейчас в дневнике " +
			today.Format("02.01.2006") + ").\nВыберите день, к которому относятся новые голосовые (зелёная кнопка — сегодня)."
	} else {
		month = cur
		text = "Рабочий день: " + cur.Format("02.01.2006") + ". Все новые голосовые считаются записанными в этот день, «сегодня» и «вчера» — относительно него.\nВыберите другой день или сбросьте."
	}
	text += "\nМожно и текстом: /date 01.09"
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: pickKeyboard(month, today, cur)}); err != nil {
		h.Log.Error("send date picker", "err", err)
	}
}

func (h *Handler) onPickCallback(ctx context.Context, b *bot.Bot, u *models.Update) {
	cq := u.CallbackQuery
	if !h.allowed(&cq.From) {
		return
	}
	answer := func(text string) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text})
	}
	if cq.Message.Message == nil {
		answer("")
		return
	}
	chatID, msgID := cq.Message.Message.Chat.ID, cq.Message.Message.ID
	today := h.Clock.Now()

	parts := strings.SplitN(strings.TrimPrefix(cq.Data, pickCallbackPrefix), ":", 2)
	switch parts[0] {
	case "m": // листать месяцы
		answer("")
		month, err := time.Parse("2006-01", partArg(parts))
		if err != nil || month.After(monthStart(today)) || month.Before(monthStart(today.AddDate(-pickYears, 0, 0))) {
			return
		}
		cur := h.referenceDate(ctx, time.Time{})
		if _, err := b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID: chatID, MessageID: msgID, ReplyMarkup: pickKeyboard(month, today, cur),
		}); err != nil {
			h.Log.Error("edit date picker", "err", err)
		}
	case "s": // выбрать день
		d, err := time.Parse("2006-01-02", partArg(parts))
		if err != nil || d.After(today) || d.Before(today.AddDate(-pickYears, 0, 0)) {
			answer("Этот день выбрать нельзя")
			return
		}
		answer("")
		h.edit(ctx, b, chatID, msgID, h.setWorkingDay(ctx, d, today))
	case "r": // сбросить
		answer("")
		h.edit(ctx, b, chatID, msgID, h.clearWorkingDay(ctx, today))
	default:
		answer("")
	}
}

func partArg(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
