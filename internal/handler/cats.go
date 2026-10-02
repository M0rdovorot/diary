package handler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"diary/internal/entry"
	"diary/internal/store"
)

// Управление категориями записи: /categories. Формат callback_data: "c:<действие>:<аргументы>".
const catCallbackPrefix = "c:"

const (
	maxTitleRunes = 40
	maxHintRunes  = 300
	maxUnitRunes  = 12
	maxGroupRunes = 30
)

// catDraft — новая категория, собираемая мастером добавления.
type catDraft struct {
	Title string
	Kind  entry.Kind
	Hint  string
	Unit  string
	Group string
}

func (h *Handler) onCategories(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	if !h.allowed(m.From) {
		return
	}
	h.clearInput(m.Chat.ID) // вход в настройки отменяет незавершённый ввод
	h.showCategories(ctx, b, m.Chat.ID, 0)
}

// show отправляет сообщение с кнопками или, если msgID != 0, правит существующее.
func (h *Handler) show(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, rows [][]models.InlineKeyboardButton) {
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	var err error
	if msgID != 0 {
		_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text, ReplyMarkup: markup})
	} else {
		_, err = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: markup})
	}
	if err != nil {
		h.Log.Warn("show message", "err", err)
	}
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// showCategories — список активных категорий кнопками.
func (h *Handler) showCategories(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	schema := h.schema(ctx)
	active := schema.Active()
	hidden := len(schema.All) - len(active)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Категории записи (%d). Нажмите на категорию, чтобы изменить её. "+
		"Что попадает в категорию, определяет подсказка для модели. Изменения действуют на новые записи; "+
		"для уже сохранённых используйте /edit → «Пересобрать запись».\n\n", len(active))
	for i, c := range active {
		fmt.Fprintf(&sb, "%d. %s — %s", i+1, c.Title, entry.KindLabel(c.Kind))
		if c.Group != "" {
			sb.WriteString(" · " + c.Group)
		}
		sb.WriteByte('\n')
	}

	var rows [][]models.InlineKeyboardButton
	for i := 0; i < len(active); i += 2 {
		row := []models.InlineKeyboardButton{btn(truncRunes(active[i].Title, 28), "c:m:"+active[i].Key)}
		if i+1 < len(active) {
			row = append(row, btn(truncRunes(active[i+1].Title, 28), "c:m:"+active[i+1].Key))
		}
		rows = append(rows, row)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("➕ Добавить категорию", "c:n")})
	if hidden > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(fmt.Sprintf("Скрытые (%d)", hidden), "c:v")})
	}
	h.show(ctx, b, chatID, msgID, sb.String(), rows)
}

// showCategoryMenu — меню одной категории.
func (h *Handler) showCategoryMenu(ctx context.Context, b *bot.Bot, chatID int64, msgID int, key string) {
	schema := h.schema(ctx)
	c, ok := schema.Lookup(key)
	if !ok {
		h.show(ctx, b, chatID, msgID, "Категория не найдена.", [][]models.InlineKeyboardButton{{btn("← К списку", "c:l")}})
		return
	}

	group, hint, unit := "—", "—", ""
	if c.Group != "" {
		group = c.Group
	}
	if c.Hint != "" {
		hint = c.Hint
	}
	if c.Kind == entry.Number && c.Unit != "" {
		unit = "\nЕдиница: " + c.Unit
	}
	status := ""
	if !c.Active {
		status = " (скрыта)"
	}
	text := fmt.Sprintf("Категория «%s»%s\nТип: %s (после создания не меняется)%s\nГруппа: %s\nПодсказка для модели: %s",
		c.Title, status, entry.KindLabel(c.Kind), unit, group, hint)

	var rows [][]models.InlineKeyboardButton
	if !c.Active {
		rows = [][]models.InlineKeyboardButton{
			{btn("Вернуть", "c:res:"+key)},
			{btn("← К скрытым", "c:v")},
		}
		h.show(ctx, b, chatID, msgID, text, rows)
		return
	}
	rows = [][]models.InlineKeyboardButton{
		{btn("Переименовать", "c:r:"+key), btn("Подсказка", "c:h:"+key)},
	}
	second := []models.InlineKeyboardButton{btn("Группа", "c:g:"+key)}
	if c.Kind == entry.Number {
		second = append(second, btn("Единица", "c:t:"+key))
	}
	rows = append(rows, second,
		[]models.InlineKeyboardButton{btn("↑ Выше", "c:u:"+key), btn("↓ Ниже", "c:d:"+key)},
	)
	if key != entry.DiaryKey {
		rows = append(rows, []models.InlineKeyboardButton{btn("Скрыть", "c:x:"+key)})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("← К списку", "c:l")})
	h.show(ctx, b, chatID, msgID, text, rows)
}

// showGroupChoice предлагает выбрать группу. wizard — выбор для новой категории (c:w:…),
// иначе для существующей key (c:s:<key>:…).
func (h *Handler) showGroupChoice(ctx context.Context, b *bot.Bot, chatID int64, msgID int, key string, wizard bool) {
	groups := h.schema(ctx).Groups()
	data := func(arg string) string {
		if wizard {
			return "c:w:" + arg
		}
		return "c:s:" + key + ":" + arg
	}
	var rows [][]models.InlineKeyboardButton
	for i, g := range groups {
		rows = append(rows, []models.InlineKeyboardButton{btn(truncRunes(g, 30), data(strconv.Itoa(i)))})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn("Без группы", data("-"))},
		[]models.InlineKeyboardButton{btn("Новая группа…", data("+"))},
	)
	back := "c:l"
	if !wizard {
		back = "c:m:" + key
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("Отмена", back)})
	h.show(ctx, b, chatID, msgID, "Группа объединяет категории в один раздел карточки (например, «Привычки»). Выберите группу:", rows)
}

func (h *Handler) showHidden(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	var rows [][]models.InlineKeyboardButton
	for _, c := range h.schema(ctx).All {
		if !c.Active {
			rows = append(rows, []models.InlineKeyboardButton{btn(truncRunes(c.Title, 30), "c:m:"+c.Key)})
		}
	}
	if len(rows) == 0 {
		h.showCategories(ctx, b, chatID, msgID)
		return
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("← К списку", "c:l")})
	h.show(ctx, b, chatID, msgID, "Скрытые категории. Данные в них сохранены; выберите категорию, чтобы вернуть её:", rows)
}

func (h *Handler) onCategoryCallback(ctx context.Context, b *bot.Bot, u *models.Update) {
	cq := u.CallbackQuery
	if !h.allowed(&cq.From) {
		return
	}
	alert := func(text string) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text, ShowAlert: text != ""})
	}
	if cq.Message.Message == nil {
		alert("")
		return
	}
	chatID, msgID := cq.Message.Message.Chat.ID, cq.Message.Message.ID

	parts := strings.Split(cq.Data, ":") // c, действие, аргументы…
	if len(parts) < 2 {
		alert("")
		return
	}
	action := parts[1]
	arg := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	key := arg(2)

	switch action {
	case "l": // к списку; отменяет незавершённый ввод
		alert("")
		h.clearInput(chatID)
		h.showCategories(ctx, b, chatID, msgID)

	case "m":
		alert("")
		h.clearInput(chatID)
		h.showCategoryMenu(ctx, b, chatID, msgID, key)

	case "v":
		alert("")
		h.showHidden(ctx, b, chatID, msgID)

	case "res": // вернуть скрытую категорию
		alert("")
		on := true
		if err := h.Store.UpdateCategory(ctx, key, store.CategoryUpdate{Active: &on}); err != nil {
			h.Log.Error("restore category", "err", err)
		}
		h.showCategories(ctx, b, chatID, msgID)

	case "u", "d":
		alert("")
		delta := 1
		if action == "u" {
			delta = -1
		}
		if err := h.Store.MoveCategory(ctx, key, delta); err != nil {
			h.Log.Error("move category", "err", err)
		}
		h.showCategories(ctx, b, chatID, msgID)

	case "x": // спросить подтверждение скрытия
		if key == entry.DiaryKey {
			alert("Эту категорию нельзя скрыть: в ней хранится рассказ от первого лица.")
			return
		}
		alert("")
		c, _ := h.schema(ctx).Lookup(key)
		h.show(ctx, b, chatID, msgID,
			fmt.Sprintf("Скрыть категорию «%s»? Она пропадёт из карточек и перестанет выделяться в новых записях, "+
				"но уже сохранённые данные останутся, и категорию можно вернуть (/categories → «Скрытые»).", c.Title),
			[][]models.InlineKeyboardButton{{btn("Скрыть", "c:xx:"+key)}, {btn("Отмена", "c:m:"+key)}})

	case "xx":
		alert("")
		off := false
		switch err := h.Store.UpdateCategory(ctx, key, store.CategoryUpdate{Active: &off}); {
		case errors.Is(err, store.ErrProtected):
			alert("Эту категорию нельзя скрыть.")
			return
		case err != nil:
			h.Log.Error("hide category", "err", err)
		}
		h.showCategories(ctx, b, chatID, msgID)

	case "r": // переименовать
		alert("")
		h.setInput(chatID, pendingInput{kind: inputCatRename, key: key})
		h.show(ctx, b, chatID, msgID, "Новое название категории (до 40 символов):",
			[][]models.InlineKeyboardButton{{btn("Отмена", "c:m:"+key)}})

	case "h": // подсказка
		alert("")
		c, _ := h.schema(ctx).Lookup(key)
		cur := c.Hint
		if cur == "" {
			cur = "—"
		}
		h.setInput(chatID, pendingInput{kind: inputCatHintEdit, key: key})
		h.show(ctx, b, chatID, msgID,
			"Подсказка для модели: что записывать в эту категорию (одна-две фразы, до 300 символов). "+
				"Пришлите «-», чтобы очистить.\n\nСейчас: "+cur,
			[][]models.InlineKeyboardButton{{btn("Отмена", "c:m:"+key)}})

	case "t": // единица измерения
		alert("")
		h.setInput(chatID, pendingInput{kind: inputCatUnitEdit, key: key})
		h.show(ctx, b, chatID, msgID, "Единица измерения (например «ч», «мл», «км»). Пришлите «-», чтобы убрать:",
			[][]models.InlineKeyboardButton{{btn("Отмена", "c:m:"+key)}})

	case "g": // выбрать группу существующей категории
		alert("")
		h.clearInput(chatID)
		h.showGroupChoice(ctx, b, chatID, msgID, key, false)

	case "s": // группа выбрана
		alert("")
		choice := arg(3)
		if choice == "+" {
			h.setInput(chatID, pendingInput{kind: inputCatGroupNew, key: key})
			h.show(ctx, b, chatID, msgID, "Название новой группы (до 30 символов):",
				[][]models.InlineKeyboardButton{{btn("Отмена", "c:m:"+key)}})
			return
		}
		group, ok := h.pickGroup(ctx, choice)
		if !ok {
			h.showGroupChoice(ctx, b, chatID, msgID, key, false)
			return
		}
		if err := h.Store.UpdateCategory(ctx, key, store.CategoryUpdate{Group: &group}); err != nil {
			h.Log.Error("set group", "err", err)
		}
		h.showCategoryMenu(ctx, b, chatID, msgID, key)

	case "n": // мастер добавления
		alert("")
		h.setInput(chatID, pendingInput{kind: inputCatTitle, draft: &catDraft{}})
		h.show(ctx, b, chatID, msgID, "Новая категория. Как её назвать (до 40 символов)? Например: «Хобби».",
			[][]models.InlineKeyboardButton{{btn("Отмена", "c:l")}})

	case "k": // тип выбран
		in, ok := h.pendingInput(chatID)
		if !ok || in.kind != inputCatKind || in.draft == nil {
			alert("Добавление категории прервалось. Начните заново: /categories")
			return
		}
		kind, valid := entry.ParseKind(key)
		if !valid {
			alert("")
			return
		}
		alert("")
		in.draft.Kind = kind
		h.setInput(chatID, pendingInput{kind: inputCatHint, draft: in.draft})
		h.show(ctx, b, chatID, msgID,
			"Что модель должна записывать в эту категорию? Одна-две фразы (например: «чем занимался и впечатления»). "+
				"Это подсказка для модели. Пришлите «-», чтобы пропустить.",
			[][]models.InlineKeyboardButton{{btn("Отмена", "c:l")}})

	case "w": // группа для новой категории выбрана
		in, ok := h.pendingInput(chatID)
		if !ok || in.kind != inputCatGroup || in.draft == nil {
			alert("Добавление категории прервалось. Начните заново: /categories")
			return
		}
		alert("")
		if key == "+" {
			h.setInput(chatID, pendingInput{kind: inputCatGroupNew, draft: in.draft})
			h.show(ctx, b, chatID, msgID, "Название новой группы (до 30 символов):",
				[][]models.InlineKeyboardButton{{btn("Отмена", "c:l")}})
			return
		}
		group, ok := h.pickGroup(ctx, key)
		if !ok {
			h.showGroupChoice(ctx, b, chatID, msgID, "", true)
			return
		}
		in.draft.Group = group
		h.finishCategory(ctx, b, chatID, msgID, in.draft)

	default:
		alert("")
	}
}

// pickGroup переводит выбор кнопки в название группы: «-» — без группы, число — номер в текущем списке.
func (h *Handler) pickGroup(ctx context.Context, choice string) (string, bool) {
	if choice == "-" {
		return "", true
	}
	i, err := strconv.Atoi(choice)
	groups := h.schema(ctx).Groups()
	if err != nil || i < 0 || i >= len(groups) {
		return "", false
	}
	return groups[i], true
}

// onCategoryInput обрабатывает текстовые шаги настройки категорий.
func (h *Handler) onCategoryInput(ctx context.Context, b *bot.Bot, chatID int64, in pendingInput, text string) {
	text = strings.TrimSpace(text)
	clearable := text == "-"
	schema := h.schema(ctx)

	// ждём кнопку, а пришёл текст
	if in.kind == inputCatKind || in.kind == inputCatGroup {
		h.send(ctx, b, chatID, "Выберите вариант кнопкой выше или отмените: /categories")
		return
	}

	switch in.kind {
	case inputCatTitle, inputCatRename:
		title, err := validateTitle(schema, text, in.key)
		if err != "" {
			h.send(ctx, b, chatID, err)
			return
		}
		if in.kind == inputCatRename {
			if e := h.Store.UpdateCategory(ctx, in.key, store.CategoryUpdate{Title: &title}); e != nil {
				h.Log.Error("rename category", "err", e)
				h.send(ctx, b, chatID, "Не удалось сохранить название.")
				return
			}
			h.clearInput(chatID)
			h.showCategoryMenu(ctx, b, chatID, 0, in.key)
			return
		}
		in.draft.Title = title
		h.setInput(chatID, pendingInput{kind: inputCatKind, draft: in.draft})
		h.show(ctx, b, chatID, 0,
			"Тип значения:\n— текст: описание или рассказ (настроение, работа);\n— список: перечень пунктов (занятия, мысли);\n"+
				"— число: измеряемое значение (часы сна, километры);\n— да/нет: было или не было (сладкое, алкоголь).",
			[][]models.InlineKeyboardButton{
				{btn("Текст", "c:k:text"), btn("Список", "c:k:list")},
				{btn("Число", "c:k:number"), btn("Да/нет", "c:k:bool")},
				{btn("Отмена", "c:l")},
			})

	case inputCatHint, inputCatHintEdit:
		hint := text
		if clearable {
			hint = ""
		}
		if len([]rune(hint)) > maxHintRunes {
			h.send(ctx, b, chatID, fmt.Sprintf("Слишком длинно (%d символов, максимум %d). Сократите, пожалуйста.", len([]rune(hint)), maxHintRunes))
			return
		}
		if in.kind == inputCatHintEdit {
			if e := h.Store.UpdateCategory(ctx, in.key, store.CategoryUpdate{Hint: &hint}); e != nil {
				h.Log.Error("set hint", "err", e)
				h.send(ctx, b, chatID, "Не удалось сохранить подсказку.")
				return
			}
			h.clearInput(chatID)
			h.showCategoryMenu(ctx, b, chatID, 0, in.key)
			return
		}
		in.draft.Hint = hint
		if in.draft.Kind == entry.Number {
			h.setInput(chatID, pendingInput{kind: inputCatUnit, draft: in.draft})
			h.show(ctx, b, chatID, 0, "Единица измерения (например «ч», «мл», «км»). Пришлите «-», чтобы без единицы.",
				[][]models.InlineKeyboardButton{{btn("Отмена", "c:l")}})
			return
		}
		h.askGroup(ctx, b, chatID, in.draft)

	case inputCatUnit, inputCatUnitEdit:
		unit := text
		if clearable {
			unit = ""
		}
		if len([]rune(unit)) > maxUnitRunes {
			h.send(ctx, b, chatID, fmt.Sprintf("Слишком длинно (максимум %d символов).", maxUnitRunes))
			return
		}
		if in.kind == inputCatUnitEdit {
			if e := h.Store.UpdateCategory(ctx, in.key, store.CategoryUpdate{Unit: &unit}); e != nil {
				h.Log.Error("set unit", "err", e)
				h.send(ctx, b, chatID, "Не удалось сохранить единицу.")
				return
			}
			h.clearInput(chatID)
			h.showCategoryMenu(ctx, b, chatID, 0, in.key)
			return
		}
		in.draft.Unit = unit
		h.askGroup(ctx, b, chatID, in.draft)

	case inputCatGroupNew:
		if text == "" || len([]rune(text)) > maxGroupRunes {
			h.send(ctx, b, chatID, fmt.Sprintf("Название группы — от 1 до %d символов.", maxGroupRunes))
			return
		}
		if in.draft != nil {
			in.draft.Group = text
			h.finishCategory(ctx, b, chatID, 0, in.draft)
			return
		}
		if e := h.Store.UpdateCategory(ctx, in.key, store.CategoryUpdate{Group: &text}); e != nil {
			h.Log.Error("set group", "err", e)
			h.send(ctx, b, chatID, "Не удалось сохранить группу.")
			return
		}
		h.clearInput(chatID)
		h.showCategoryMenu(ctx, b, chatID, 0, in.key)
	}
}

func (h *Handler) askGroup(ctx context.Context, b *bot.Bot, chatID int64, d *catDraft) {
	h.setInput(chatID, pendingInput{kind: inputCatGroup, draft: d})
	h.showGroupChoice(ctx, b, chatID, 0, "", true)
}

// validateTitle проверяет название категории. Возвращает очищенное название или текст ошибки.
// exceptKey — ключ редактируемой категории (её собственное название дублем не считается).
func validateTitle(schema entry.Schema, title, exceptKey string) (string, string) {
	title = strings.Join(strings.Fields(title), " ")
	switch n := len([]rune(title)); {
	case n == 0:
		return "", "Название не может быть пустым."
	case n > maxTitleRunes:
		return "", fmt.Sprintf("Слишком длинно (%d символов, максимум %d). Сократите, пожалуйста.", n, maxTitleRunes)
	}
	if c, ok := schema.FindByTitle(title); ok && c.Key != exceptKey {
		return "", "Категория с таким названием уже есть. Придумайте другое название."
	}
	return title, ""
}

// finishCategory создаёт категорию из черновика мастера.
func (h *Handler) finishCategory(ctx context.Context, b *bot.Bot, chatID int64, msgID int, d *catDraft) {
	schema := h.schema(ctx)
	key := entry.SlugKey(d.Title, func(k string) bool { _, taken := schema.Lookup(k); return taken })
	c, err := h.Store.AddCategory(ctx, entry.Category{
		Key: key, Title: d.Title, Kind: d.Kind, Hint: d.Hint, Unit: d.Unit, Group: d.Group,
	})
	if err != nil {
		h.Log.Error("add category", "err", err)
		h.clearInput(chatID)
		h.send(ctx, b, chatID, "Не удалось добавить категорию, попробуйте ещё раз: /categories")
		return
	}
	h.clearInput(chatID)
	h.show(ctx, b, chatID, msgID,
		fmt.Sprintf("Категория «%s» (%s) добавлена в конец списка. Новые записи будут её учитывать; "+
			"уже сохранённые — после /edit → «Пересобрать запись». Порядок, подсказку и группу можно поменять в /categories.",
			c.Title, entry.KindLabel(c.Kind)),
		[][]models.InlineKeyboardButton{{btn("К списку категорий", "c:l")}})
}
