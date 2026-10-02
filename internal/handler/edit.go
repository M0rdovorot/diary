package handler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/entry"
	"diary/internal/store"
)

// Формат callback_data правки: все начинаются с "e".
const (
	editCallbackPrefix = "e"
	editSelectPrefix   = "es:" // es:<voiceID> — выбрать голосовое
	editReplacePrefix  = "ef:" // ef:<voiceID> — режим «найти и заменить»
	editWholePrefix    = "ew:" // ew:<voiceID> — режим «заменить целиком»
	editRedoPrefix     = "er:" // er:<voiceID> — пересобрать запись по текущей расшифровке
	editConfirmPrefix  = "ek:" // ek:<voiceID> — подтвердить расшифровку
	editMenuPrefix     = "ex:" // ex:<voiceID> — открыть меню правки (текст уже на экране)
	editDeletePrefix   = "ed:" // ed:<voiceID> — спросить подтверждение удаления записи
	editDeleteOKPrefix = "ey:" // ey:<voiceID> — удалить запись безвозвратно
	editCancelData     = "ec"
)

const editListSize = 8

// onEdit показывает голосовые для выбора: /edit (последние) или /edit 01.09 (за день).
func (h *Handler) onEdit(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if !h.allowed(m.From) {
		return
	}

	var (
		voices []store.Voice
		err    error
		title  = "Последние голосовые. Выберите, какую расшифровку править:"
	)
	if fields := strings.Fields(m.Text); len(fields) > 1 {
		day, perr := entry.ParseUserDate(strings.Join(fields[1:], " "), h.Clock.Now())
		if perr != nil {
			h.send(ctx, b, m.Chat.ID, perr.Error()+". Пример: /edit или /edit 01.09")
			return
		}
		voices, err = h.Store.VoicesForDay(ctx, day, editListSize)
		title = "Голосовые за " + day.Format("02.01.2006") + ". Выберите, какую расшифровку править:"
	} else {
		voices, err = h.Store.RecentVoices(ctx, editListSize)
	}
	if err != nil {
		h.Log.Error("list voices", "err", err)
		h.send(ctx, b, m.Chat.ID, "Не удалось получить список голосовых.")
		return
	}
	if len(voices) == 0 {
		h.send(ctx, b, m.Chat.ID, "Голосовых не найдено.")
		return
	}

	h.sendVoiceList(ctx, b, m.Chat.ID, title, voices)
}

// sendVoiceList присылает список записей кнопками; нажатие открывает расшифровку и меню правки.
func (h *Handler) sendVoiceList(ctx context.Context, b *bot.Bot, chatID int64, title string, voices []store.Voice) {
	rows := make([][]models.InlineKeyboardButton, 0, len(voices))
	for _, v := range voices {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         h.voiceLabel(v),
			CallbackData: fmt.Sprintf("%s%d", editSelectPrefix, v.ID),
		}})
	}
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        title,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: rows},
	}); err != nil {
		h.Log.Error("send voice list", "err", err)
	}
}

// voiceLabel — подпись кнопки: «⚠ 05.09 10:00 · 1:23 · начало текста…».
// ⚠ — расшифровка не подтверждена, ✎ — правилась вручную; у текстовых записей вместо длительности «текст».
func (h *Handler) voiceLabel(v store.Voice) string {
	dur := fmt.Sprintf("%d:%02d", v.DurationSec/60, v.DurationSec%60)
	if v.Source == "text" {
		dur = "текст"
	}
	label := fmt.Sprintf("%s · %s · %s", v.SentAt.In(h.Clock.Loc).Format("02.01 15:04"), dur, shorten(v.Preview, 28))
	switch {
	case !v.Confirmed:
		label = "⚠ " + label
	case v.Edited:
		label = "✎ " + label
	}
	return label
}

// shorten сжимает текст в одну строку не длиннее limit рун.
func shorten(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

func (h *Handler) onEditCallback(ctx context.Context, b *bot.Bot, u *models.Update) {
	cq := u.CallbackQuery
	if !h.allowed(&cq.From) {
		return
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
	if cq.Message.Message == nil {
		return
	}
	chatID, msgID := cq.Message.Message.Chat.ID, cq.Message.Message.ID

	data := cq.Data
	if data == editCancelData {
		h.clearInput(chatID)
		h.edit(ctx, b, chatID, msgID, "Отменено.")
		return
	}

	idStr := data[strings.Index(data, ":")+1:]
	voiceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || !strings.Contains(data, ":") {
		return
	}

	switch {
	case strings.HasPrefix(data, editDeletePrefix):
		h.askDelete(ctx, b, chatID, msgID, voiceID)
	case strings.HasPrefix(data, editDeleteOKPrefix):
		h.deleteEntry(ctx, b, chatID, msgID, voiceID)
	case strings.HasPrefix(data, editConfirmPrefix):
		h.confirm(ctx, b, chatID, msgID, cq.Message.Message.Text, voiceID)
	case strings.HasPrefix(data, editMenuPrefix):
		// сообщение с расшифровкой оставляем как есть, но убираем его кнопки
		h.edit(ctx, b, chatID, msgID, cq.Message.Message.Text+"\n\n✎ Правка…")
		h.showEditMenu(ctx, b, chatID, voiceID)
	case strings.HasPrefix(data, editSelectPrefix):
		h.showVoice(ctx, b, chatID, voiceID)
	case strings.HasPrefix(data, editReplacePrefix):
		h.setInput(chatID, pendingInput{kind: inputReplace, id: voiceID})
		h.edit(ctx, b, chatID, msgID, "Пришлите замены одним сообщением, по одной в строке:\n\nкак было => как надо\n\n"+
			"Например:\nкатя => Катя\nсходил в зал ов => сходил в зал\n\n"+
			"Регистр при поиске не важен, заменяются все совпадения. Отменить: /edit")
	case strings.HasPrefix(data, editWholePrefix):
		h.setInput(chatID, pendingInput{kind: inputWhole, id: voiceID})
		h.edit(ctx, b, chatID, msgID, "Пришлите новый текст расшифровки целиком одним сообщением (Telegram принимает до 4096 символов; "+
			"для длинных записей удобнее «найти и заменить»). Прежняя версия сохранится в истории. Отменить: /edit")
	case strings.HasPrefix(data, editRedoPrefix):
		v, err := h.Store.GetVoice(ctx, voiceID)
		if err != nil {
			h.edit(ctx, b, chatID, msgID, "Голосовое не найдено.")
			return
		}
		h.edit(ctx, b, chatID, msgID, "Пересобираю запись…")
		h.analyze(ctx, b, chatID, v.ID, v.ReferenceDate, v.LogicalDate, v.Transcript, true)
	}
}

// askDelete просит подтвердить удаление записи и показывает, что именно удалится.
func (h *Handler) askDelete(ctx context.Context, b *bot.Bot, chatID int64, msgID int, voiceID int64) {
	v, err := h.Store.GetVoice(ctx, voiceID)
	if errors.Is(err, store.ErrNotFound) {
		h.edit(ctx, b, chatID, msgID, "Запись уже удалена.")
		return
	}
	if err != nil {
		h.Log.Error("get voice", "err", err)
		h.edit(ctx, b, chatID, msgID, "Не удалось получить запись.")
		return
	}
	imp, err := h.Store.VoiceImpact(ctx, voiceID)
	if err != nil {
		h.Log.Error("voice impact", "err", err)
		h.edit(ctx, b, chatID, msgID, "Не удалось получить запись.")
		return
	}

	what := "расшифровка"
	if v.Source == "text" {
		what = "текст"
	}
	text := fmt.Sprintf("Удалить запись от %s навсегда?\n«%s»\n\nБудет удалено: %s", v.SentAt.In(h.Clock.Loc).Format("02.01.2006 15:04"), shorten(v.Preview, 80), what)
	if imp.Edits > 0 {
		text += ", история её правок"
	}
	text += describeImpact(imp, ", ")
	text += ".\nОстальные записи этих дней останутся, карточки дней пересоберутся без неё. " +
		"Отменить удаление будет нельзя (копии в бэкапах не затрагиваются)."
	h.show(ctx, b, chatID, msgID, text, [][]models.InlineKeyboardButton{
		{btn("Удалить безвозвратно", editDeleteOKPrefix+strconv.FormatInt(voiceID, 10))},
		{btn("Отмена", editCancelData)},
	})
}

// describeImpact — «, всё извлечённое из неё (N полей за дни: 01.09, 05.09)»; пусто, если извлечённого нет.
func describeImpact(imp store.VoiceImpact, prefix string) string {
	if imp.Observations == 0 {
		return ""
	}
	days := make([]string, len(imp.Days))
	for i, d := range imp.Days {
		days[i] = d.Format("02.01")
	}
	return fmt.Sprintf("%sвсё извлечённое из неё (%d значений за дни: %s)", prefix, imp.Observations, strings.Join(days, ", "))
}

// deleteEntry удаляет запись безвозвратно.
func (h *Handler) deleteEntry(ctx context.Context, b *bot.Bot, chatID int64, msgID int, voiceID int64) {
	imp, err := h.Store.DeleteVoice(ctx, voiceID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.edit(ctx, b, chatID, msgID, "Запись уже удалена.")
		return
	case err != nil:
		h.Log.Error("delete voice", "voice", voiceID, "err", err)
		h.edit(ctx, b, chatID, msgID, "Не удалось удалить запись, попробуйте ещё раз.")
		return
	}
	h.Log.Info("entry deleted", "voice_id", voiceID, "observations", imp.Observations)

	// если ждали правку именно этой записи — больше не ждём
	if in, ok := h.pendingInput(chatID); ok && (in.kind == inputReplace || in.kind == inputWhole) && in.id == voiceID {
		h.clearInput(chatID)
	}

	text := "Запись удалена ✓"
	if imp.Observations > 0 {
		days := make([]string, len(imp.Days))
		for i, d := range imp.Days {
			days[i] = d.Format("02.01.2006")
		}
		text += fmt.Sprintf("\nУдалено значений: %d. Затронутые дни: %s — посмотреть: /day", imp.Observations, strings.Join(days, ", "))
	}
	h.edit(ctx, b, chatID, msgID, text)
}

// confirm помечает расшифровку подтверждённой и убирает кнопки у сообщения с вопросом.
func (h *Handler) confirm(ctx context.Context, b *bot.Bot, chatID int64, msgID int, msgText string, voiceID int64) {
	switch err := h.Store.ConfirmVoice(ctx, voiceID); {
	case err == nil:
		h.edit(ctx, b, chatID, msgID, msgText+"\n\n✓ Подтверждено")
	case errors.Is(err, store.ErrNotFound):
		h.edit(ctx, b, chatID, msgID, "Запись не найдена.")
	default:
		h.Log.Error("confirm voice", "voice", voiceID, "err", err)
		h.send(ctx, b, chatID, "Не удалось сохранить подтверждение, попробуйте ещё раз.")
	}
}

// showVoice присылает текст выбранной записи и меню действий.
func (h *Handler) showVoice(ctx context.Context, b *bot.Bot, chatID, voiceID int64) {
	v, err := h.Store.GetVoice(ctx, voiceID)
	if errors.Is(err, store.ErrNotFound) {
		h.send(ctx, b, chatID, "Запись не найдена.")
		return
	}
	if err != nil {
		h.Log.Error("get voice", "err", err)
		h.send(ctx, b, chatID, "Не удалось получить запись.")
		return
	}

	kind := fmt.Sprintf("Расшифровка голосового от %s (%d:%02d)", v.SentAt.In(h.Clock.Loc).Format("02.01.2006 15:04"), v.DurationSec/60, v.DurationSec%60)
	if v.Source == "text" {
		kind = "Текстовая запись от " + v.SentAt.In(h.Clock.Loc).Format("02.01.2006 15:04")
	}
	status := ""
	if !v.Confirmed {
		status = ", не подтверждена"
	}
	h.send(ctx, b, chatID, fmt.Sprintf("%s, день записи %s%s:", kind, v.ReferenceDate.Format("02.01.2006"), status))
	h.send(ctx, b, chatID, v.Transcript)
	h.sendEditMenu(ctx, b, chatID, v)
}

// showEditMenu присылает меню действий для записи (текст уже показан).
func (h *Handler) showEditMenu(ctx context.Context, b *bot.Bot, chatID, voiceID int64) {
	v, err := h.Store.GetVoice(ctx, voiceID)
	if err != nil {
		h.Log.Error("get voice", "err", err)
		h.send(ctx, b, chatID, "Не удалось получить запись.")
		return
	}
	h.sendEditMenu(ctx, b, chatID, v)
}

func (h *Handler) sendEditMenu(ctx context.Context, b *bot.Bot, chatID int64, v store.Voice) {
	id := strconv.FormatInt(v.ID, 10)
	var rows [][]models.InlineKeyboardButton
	if !v.Confirmed {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "✓ Подтвердить", CallbackData: editConfirmPrefix + id}})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: "Найти и заменить", CallbackData: editReplacePrefix + id}},
		[]models.InlineKeyboardButton{{Text: "Заменить целиком", CallbackData: editWholePrefix + id}},
		[]models.InlineKeyboardButton{{Text: "Пересобрать запись", CallbackData: editRedoPrefix + id}},
		[]models.InlineKeyboardButton{{Text: "🗑 Удалить запись", CallbackData: editDeletePrefix + id}},
		[]models.InlineKeyboardButton{{Text: "Закрыть", CallbackData: editCancelData}},
	)
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "Что сделать с этой расшифровкой?",
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: rows},
	}); err != nil {
		h.Log.Error("send edit menu", "err", err)
	}
}

type replacement struct{ old, new string }

// parseReplacements разбирает строки «было => стало» (также «->» и «→»).
// Возвращает замены и описания нераспознанных строк.
func parseReplacements(text string) ([]replacement, []string) {
	var out []replacement
	var bad []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx, sepLen := -1, 0
		for _, sep := range []string{"=>", "->", "→"} {
			if i := strings.Index(line, sep); i >= 0 && (idx < 0 || i < idx) {
				idx, sepLen = i, len(sep)
			}
		}
		if idx < 0 {
			bad = append(bad, "нет «=>»: "+shorten(line, 40))
			continue
		}
		old, repl := strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+sepLen:])
		if old == "" {
			bad = append(bad, "пустое «как было»: "+shorten(line, 40))
			continue
		}
		out = append(out, replacement{old, repl})
	}
	return out, bad
}

// applyReplacements применяет замены к расшифровке и пересобирает запись.
func (h *Handler) applyReplacements(ctx context.Context, b *bot.Bot, chatID, voiceID int64, text string) {
	pairs, bad := parseReplacements(text)
	v, err := h.Store.GetVoice(ctx, voiceID)
	if err != nil {
		h.clearInput(chatID)
		h.send(ctx, b, chatID, "Голосовое не найдено.")
		return
	}

	t, report, changed := applyPairs(v.Transcript, pairs)
	for _, s := range bad {
		report = append(report, "— пропущено ("+s+")")
	}
	if !changed {
		// остаёмся в режиме правки: можно прислать исправленный список
		h.send(ctx, b, chatID, "Ничего не заменено:\n"+strings.Join(report, "\n")+"\n\nПришлите замены ещё раз или отмените: /edit")
		return
	}
	h.send(ctx, b, chatID, "Замены:\n"+strings.Join(report, "\n"))
	h.commitTranscript(ctx, b, chatID, v, t)
}

// applyPairs последовательно применяет замены без учёта регистра. report — строка на каждую замену.
func applyPairs(text string, pairs []replacement) (result string, report []string, changed bool) {
	for _, p := range pairs {
		re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(p.old))
		n := len(re.FindAllStringIndex(text, -1))
		if n == 0 {
			report = append(report, fmt.Sprintf("— «%s»: не найдено", p.old))
			continue
		}
		text = re.ReplaceAllLiteralString(text, p.new)
		report = append(report, fmt.Sprintf("— «%s» → «%s»: %d", p.old, p.new, n))
		changed = true
	}
	return text, report, changed
}

// applyWhole заменяет расшифровку присланным текстом.
func (h *Handler) applyWhole(ctx context.Context, b *bot.Bot, chatID, voiceID int64, text string) {
	text = strings.TrimSpace(text)
	v, err := h.Store.GetVoice(ctx, voiceID)
	if err != nil {
		h.clearInput(chatID)
		h.send(ctx, b, chatID, "Голосовое не найдено.")
		return
	}
	h.commitTranscript(ctx, b, chatID, v, text)
}

// commitTranscript сохраняет новую расшифровку (прежняя остаётся в истории) и пересобирает запись.
func (h *Handler) commitTranscript(ctx context.Context, b *bot.Bot, chatID int64, v store.Voice, newText string) {
	if err := h.Store.UpdateTranscript(ctx, v.ID, newText); err != nil {
		h.Log.Error("update transcript", "voice", v.ID, "err", err)
		h.send(ctx, b, chatID, "Не удалось сохранить правку.")
		return
	}
	h.clearInput(chatID)
	h.send(ctx, b, chatID, "Расшифровка обновлена и считается подтверждённой, прежняя версия сохранена в истории. Пересобираю запись…")
	h.analyze(ctx, b, chatID, v.ID, v.ReferenceDate, v.LogicalDate, newText, true)
}
