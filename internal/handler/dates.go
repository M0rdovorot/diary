package handler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/entry"
	"diary/internal/store"
)

// Формат callback_data: "d:<segmentID>:<ГГГГ-ММ-ДД | x>"; x — «другая дата».
const dateCallbackPrefix = "d:"

func (h *Handler) allowed(u *models.User) bool { return u != nil && u.ID == h.AllowedUserID }

// askDate спрашивает, к какому дню относится сегмент с неизвестной датой.
// ref — день, к которому отнесено голосовое: «Сегодня» и «Вчера» в кнопках считаются от него.
// Если вопросов несколько (total > 1), в тексте указывается номер («запись 2 из 3»);
// preview — выдержка из записи, по которой её можно узнать.
func (h *Handler) askDate(ctx context.Context, b *bot.Bot, chatID, segID int64, ref time.Time, n, total int, preview string) {
	today, yesterday := ref, ref.AddDate(0, 0, -1)
	data := func(d time.Time) string {
		return fmt.Sprintf("%s%d:%s", dateCallbackPrefix, segID, d.Format("2006-01-02"))
	}

	text := "К какому дню относится эта запись?"
	if total > 1 {
		text = fmt.Sprintf("К какому дню относится запись %d из %d?", n, total)
	}
	if preview != "" {
		text += "\n\n«" + preview + "»"
	}
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Сегодня (" + today.Format("02.01") + ")", CallbackData: data(today)},
			{Text: "Вчера (" + yesterday.Format("02.01") + ")", CallbackData: data(yesterday)},
		}, {
			{Text: "Другая дата", CallbackData: fmt.Sprintf("%s%d:x", dateCallbackPrefix, segID)},
		}}},
	})
	if err != nil {
		h.Log.Error("ask date", "err", err)
	}
}

func (h *Handler) onDateCallback(ctx context.Context, b *bot.Bot, u *models.Update) {
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

	parts := strings.Split(cq.Data, ":") // d, id, значение
	if len(parts) != 3 {
		answer("")
		return
	}
	segID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		answer("")
		return
	}

	if parts[2] == "x" {
		h.setInput(chatID, pendingInput{kind: inputDate, id: segID})
		answer("")
		h.edit(ctx, b, chatID, msgID, "Напишите дату, к которой относится запись: ДД.ММ или ДД.ММ.ГГГГ (можно «вчера», «позавчера»).")
		return
	}

	date, err := time.Parse("2006-01-02", parts[2])
	if err != nil {
		answer("")
		return
	}
	answer("")
	h.resolve(ctx, b, chatID, msgID, segID, date)
}

// resolve закрепляет дату за сегментом и сообщает результат (правкой вопроса или новым сообщением).
func (h *Handler) resolve(ctx context.Context, b *bot.Bot, chatID int64, msgID int, segID int64, date time.Time) {
	var text string
	switch err := h.Store.ResolveSegment(ctx, segID, date); {
	case err == nil:
		h.clearInput(chatID)
		text = "Дата записи: " + date.Format("02.01.2006") + " ✓\nПосмотреть день: /day " + date.Format("02.01.2006")
	case errors.Is(err, store.ErrNotPending):
		h.clearInput(chatID)
		text = "Эта запись уже не ждёт уточнения даты (дата уточнена или запись пересобрана)."
	default:
		h.Log.Error("resolve segment", "segment", segID, "err", err)
		text = "Не удалось сохранить дату, попробуйте ещё раз."
	}
	if msgID != 0 {
		h.edit(ctx, b, chatID, msgID, text)
	} else {
		h.send(ctx, b, chatID, text)
	}
}

// edit заменяет текст сообщения; клавиатура при этом убирается.
func (h *Handler) edit(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string) {
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text}); err != nil {
		h.Log.Error("edit message", "err", err)
	}
}

func (h *Handler) setInput(chatID int64, in pendingInput) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.input == nil {
		h.input = map[int64]pendingInput{}
	}
	h.input[chatID] = in
}

func (h *Handler) clearInput(chatID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.input, chatID)
}

func (h *Handler) pendingInput(chatID int64) (pendingInput, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	in, ok := h.input[chatID]
	return in, ok
}

// isAwaitedInput: обычный текст от владельца, пока мы ждём от него дату или правку.
func (h *Handler) isAwaitedInput(u *models.Update) bool {
	m := u.Message
	if m == nil || m.Text == "" || strings.HasPrefix(m.Text, "/") || !h.allowed(m.From) {
		return false
	}
	_, ok := h.pendingInput(m.Chat.ID)
	return ok
}

func (h *Handler) onAwaitedInput(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	in, ok := h.pendingInput(m.Chat.ID)
	if !ok {
		return
	}
	switch in.kind {
	case inputDate:
		date, err := entry.ParseUserDate(m.Text, h.Clock.Now())
		if err != nil {
			h.send(ctx, b, m.Chat.ID, err.Error()+". Напишите дату как ДД.ММ или ДД.ММ.ГГГГ.")
			return
		}
		h.resolve(ctx, b, m.Chat.ID, 0, in.id, date)
	case inputReplace:
		h.applyReplacements(ctx, b, m.Chat.ID, in.id, m.Text)
	case inputWhole:
		h.applyWhole(ctx, b, m.Chat.ID, in.id, m.Text)
	}
}

// onPending повторно задаёт вопросы по всем записям без даты (например, после перезапуска бота).
func (h *Handler) onPending(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if !h.allowed(m.From) {
		return
	}
	pend, err := h.Store.PendingSegments(ctx)
	if err != nil {
		h.Log.Error("pending segments", "err", err)
		h.send(ctx, b, m.Chat.ID, "Не удалось получить список записей без даты.")
		return
	}
	if len(pend) == 0 {
		h.send(ctx, b, m.Chat.ID, "Записей без даты нет.")
		return
	}
	for i, p := range pend {
		h.askDate(ctx, b, m.Chat.ID, p.ID, p.RefDate, i+1, len(pend), preview(p.Segment, 140))
	}
}

// preview — короткая выдержка из сегмента, чтобы узнать запись.
func preview(seg entry.Segment, limit int) string {
	text, _ := seg.Values["diary"].(string)
	if text == "" {
		for _, f := range entry.Fields {
			if s, ok := seg.Values[f.Key].(string); ok {
				text = s
				break
			}
		}
	}
	if r := []rune(text); len(r) > limit {
		text = string(r[:limit]) + "…"
	}
	return text
}

// onDay показывает собранную карточку дня: /day, /day 01.09, /day 01.09.2026, /day вчера.
func (h *Handler) onDay(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if !h.allowed(m.From) {
		return
	}
	now := h.Clock.Now()
	date := now
	if fields := strings.Fields(m.Text); len(fields) > 1 {
		d, err := entry.ParseUserDate(strings.Join(fields[1:], " "), now)
		if err != nil {
			h.send(ctx, b, m.Chat.ID, err.Error()+". Пример: /day 01.09 или /day вчера")
			return
		}
		date = d
	}

	obs, err := h.Store.DayObservations(ctx, date)
	if err != nil {
		h.Log.Error("day observations", "err", err)
		h.send(ctx, b, m.Chat.ID, "Не удалось получить записи за день.")
		return
	}
	if len(obs) == 0 {
		h.send(ctx, b, m.Chat.ID, "За "+date.Format("02.01.2006")+" записей нет.")
	} else {
		h.send(ctx, b, m.Chat.ID, entry.RenderCard(entry.BuildCard(date, h.Clock.Loc, obs)))
	}

	if pend, err := h.Store.PendingSegments(ctx); err == nil && len(pend) > 0 {
		h.send(ctx, b, m.Chat.ID, fmt.Sprintf("Есть записей без даты: %d — /pending", len(pend)))
	}
}

// onDate выбирает «рабочий день» для новых голосовых: /date, /date 01.09, /date сброс.
// Пока день выбран, все новые голосовые считаются записанными в него, а «сегодня»/«вчера»
// в них считаются относительно него.
func (h *Handler) onDate(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if !h.allowed(m.From) {
		return
	}
	now := h.Clock.Now()
	cur := h.referenceDate(ctx, time.Time{})
	arg := ""
	if fields := strings.Fields(m.Text); len(fields) > 1 {
		arg = strings.ToLower(strings.Join(fields[1:], " "))
	}

	switch arg {
	case "":
		if cur.IsZero() {
			h.send(ctx, b, m.Chat.ID, "Рабочий день не выбран: «сегодня» и «вчера» считаются от даты отправки голосового (сейчас в дневнике "+
				now.Format("02.01.2006")+").\nВыбрать день: /date 01.09")
		} else {
			h.send(ctx, b, m.Chat.ID, "Рабочий день: "+cur.Format("02.01.2006")+". Все новые голосовые считаются записанными в этот день, «сегодня» и «вчера» — относительно него.\nСбросить: /date сброс")
		}
		return
	case "сброс", "сбросить", "reset", "off", "нет":
		h.resetReferenceDate(ctx, b, m.Chat.ID, now)
		return
	}

	d, err := entry.ParseUserDate(arg, now)
	if err != nil {
		h.send(ctx, b, m.Chat.ID, err.Error()+". Пример: /date 01.09 или /date сброс")
		return
	}
	if d.Equal(now) {
		// выбрать сегодняшний день — то же, что сброс, но без «залипания» на завтра
		h.resetReferenceDate(ctx, b, m.Chat.ID, now)
		return
	}
	if err := h.Store.SetSetting(ctx, referenceDateKey, d.Format("2006-01-02")); err != nil {
		h.Log.Error("set reference date", "err", err)
		h.send(ctx, b, m.Chat.ID, "Не удалось сохранить настройку.")
		return
	}
	h.send(ctx, b, m.Chat.ID, "Рабочий день: "+d.Format("02.01.2006")+". Новые голосовые считаются записанными в этот день; «сегодня» и «вчера» в них — относительно него.\nСбросить: /date сброс")
}

func (h *Handler) resetReferenceDate(ctx context.Context, b *bot.Bot, chatID int64, now time.Time) {
	if err := h.Store.DeleteSetting(ctx, referenceDateKey); err != nil {
		h.Log.Error("reset reference date", "err", err)
		h.send(ctx, b, chatID, "Не удалось сбросить настройку.")
		return
	}
	h.send(ctx, b, chatID, "Рабочий день сброшен: «сегодня» и «вчера» снова считаются от даты отправки голосового (сейчас в дневнике "+now.Format("02.01.2006")+").")
}
