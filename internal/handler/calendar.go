package handler

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// onCalendar присылает кнопку, открывающую Mini App с календарём дней.
func (h *Handler) onCalendar(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if !h.allowed(m.From) {
		return
	}
	if h.WebAppURL == "" {
		h.send(ctx, b, m.Chat.ID, "Календарь пока не настроен: задайте WEBAPP_URL. Карточку дня можно посмотреть командой /day.")
		return
	}
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: m.Chat.ID,
		Text:   "Календарь: выберите день, чтобы открыть его карточку и расшифровки записей.",
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "📅 Открыть календарь", WebApp: &models.WebAppInfo{URL: h.WebAppURL}},
		}}},
	}); err != nil {
		h.Log.Error("send calendar", "err", err)
	}
}
