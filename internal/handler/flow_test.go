package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"

	"diary/internal/diaryday"
	"diary/internal/entry"
	"diary/internal/store"
	"diary/internal/summary"
)

// fakeTelegram запоминает вызовы Bot API и отвечает минимально валидными ответами.
type fakeTelegram struct {
	mu    sync.Mutex
	calls []call
	next  int
}

type call struct {
	method string
	body   map[string]any
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// go-telegram/bot отправляет параметры как multipart/form-data
	body := map[string]any{}
	if err := r.ParseMultipartForm(1 << 20); err == nil {
		for k, v := range r.MultipartForm.Value {
			body[k] = v[0]
			if k == "reply_markup" {
				var rm any
				if json.Unmarshal([]byte(v[0]), &rm) == nil {
					body[k] = rm
				}
			}
		}
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]

	f.mu.Lock()
	f.calls = append(f.calls, call{method, body})
	f.next++
	id := f.next
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if method == "answerCallbackQuery" {
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		return
	}
	fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":0,"chat":{"id":1,"type":"private"}}}`, id)
}

// texts — тексты отправленных/изменённых сообщений.
func (f *fakeTelegram) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.method == "sendMessage" || c.method == "editMessageText" {
			out = append(out, c.body["text"].(string))
		}
	}
	return out
}

func (f *fakeTelegram) last() string {
	t := f.texts()
	if len(t) == 0 {
		return ""
	}
	return t[len(t)-1]
}

// beforeLast — предпоследнее сообщение (например, карточка дня перед подсказкой про /pending).
func (f *fakeTelegram) beforeLast() string {
	t := f.texts()
	if len(t) < 2 {
		return ""
	}
	return t[len(t)-2]
}

type button struct{ text, data string }

// lastButtons — кнопки последней клавиатуры, построчно.
func (f *fakeTelegram) lastButtons() [][]button {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		rm, ok := f.calls[i].body["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		var rows [][]button
		for _, row := range rm["inline_keyboard"].([]any) {
			var r []button
			for _, b := range row.([]any) {
				m := b.(map[string]any)
				r = append(r, button{m["text"].(string), m["callback_data"].(string)})
			}
			rows = append(rows, r)
		}
		return rows
	}
	return nil
}

// env — стенд: настоящий Postgres, поддельные Telegram и YandexGPT.
type env struct {
	t    *testing.T
	ctx  context.Context
	h    *Handler
	b    *bot.Bot
	tg   *fakeTelegram
	db   *store.Store
	zone *time.Location

	mu      sync.Mutex
	gpt     func(user string) string // ответ «модели» по пользовательскому сообщению
	users   []string                 // что получила «модель» (пользовательские сообщения)
	systems []string                 // системные промпты
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL не задан")
	}
	ctx := context.Background()
	e := &env{t: t, ctx: ctx}

	// чистая схема в тестовой БД
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	e.db, err = store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.db.Close)
	if err := e.db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	gptSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Text string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, user := req.Messages[0].Text, req.Messages[len(req.Messages)-1].Text
		e.mu.Lock()
		e.users = append(e.users, user)
		e.systems = append(e.systems, system)
		fn := e.gpt
		e.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"alternatives": []any{
			map[string]any{"message": map[string]any{"role": "assistant", "text": fn(user)}, "status": "ALTERNATIVE_STATUS_FINAL"},
		}}})
	}))
	t.Cleanup(gptSrv.Close)

	e.tg = &fakeTelegram{}
	tgSrv := httptest.NewServer(e.tg)
	t.Cleanup(tgSrv.Close)
	e.b, err = bot.New("TOKEN", bot.WithServerURL(tgSrv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}

	clock, _ := diaryday.NewClock("Europe/Moscow", 5)
	e.zone = clock.Loc
	sum := summary.NewYandexGPT("key", "folder", "yandexgpt/latest")
	sum.URL = gptSrv.URL
	e.h = &Handler{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AllowedUserID: 1,
		Store:         e.db,
		Clock:         clock,
		Summarizer:    sum,
	}
	e.h.Register(e.b)
	return e
}

func (e *env) setGPT(fn func(user string) string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gpt = fn
}

func (e *env) lastSystem() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.systems[len(e.systems)-1]
}

func (e *env) lastUser() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.users[len(e.users)-1]
}

// voice эмулирует обработку голосового, отправленного в момент sent (после распознавания):
// после разбора бот просит подтвердить расшифровку.
func (e *env) voice(msgID int, sent time.Time, text string) {
	logical := e.h.Clock.LogicalDate(sent)
	ref := e.h.referenceDate(e.ctx, logical)
	e.h.processTranscript(e.ctx, e.b, 1, msgID, sent, ref, 60, text, "voice")
}

// text эмулирует текстовую запись (без подтверждения): тот же путь, что у голосового.
func (e *env) text(msgID int, sent time.Time, text string) {
	logical := e.h.Clock.LogicalDate(sent)
	ref := e.h.referenceDate(e.ctx, logical)
	e.h.processTranscript(e.ctx, e.b, 1, msgID, sent, ref, 0, text, "text")
}

// sayAt — сообщение пользователя, отправленное в момент sent (для текстовых записей важна дата).
func (e *env) sayAt(text string, sent time.Time) {
	e.t.Helper()
	e.dispatch(&models.Update{Message: &models.Message{ID: int(sent.Unix() % 100000), Date: int(sent.Unix()),
		Text: text, From: &models.User{ID: 1}, Chat: models.Chat{ID: 1}}})
}

func (e *env) say(text string) {
	e.t.Helper()
	e.dispatch(&models.Update{Message: &models.Message{Text: text, From: &models.User{ID: 1}, Chat: models.Chat{ID: 1}}})
}

// dispatch маршрутизирует сообщение так же, как Register: команды, затем ответ на вопрос бота,
// затем текстовая запись.
func (e *env) dispatch(upd *models.Update) {
	e.t.Helper()
	text := upd.Message.Text
	switch {
	case strings.HasPrefix(text, "/day"):
		e.h.onDay(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/date"):
		e.h.onDate(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/edit"):
		e.h.onEdit(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/pending"):
		e.h.onPending(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/categories"):
		e.h.onCategories(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/help"):
		e.h.onHelp(e.ctx, e.b, upd)
	case e.h.isAwaitedInput(upd):
		e.h.onAwaitedInput(e.ctx, e.b, upd)
	case e.h.isTextEntry(upd):
		e.h.onText(e.ctx, e.b, upd)
	default:
		e.t.Fatalf("сообщение %q никто не обработал", text)
	}
}

// press нажимает кнопку: маршрутизация по префиксу callback_data, как в Register.
func (e *env) press(data string) {
	upd := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb", From: models.User{ID: 1}, Data: data,
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 7, Chat: models.Chat{ID: 1}}},
	}}
	switch {
	case strings.HasPrefix(data, dateCallbackPrefix):
		e.h.onDateCallback(e.ctx, e.b, upd)
	case strings.HasPrefix(data, catCallbackPrefix):
		e.h.onCategoryCallback(e.ctx, e.b, upd)
	default:
		e.h.onEditCallback(e.ctx, e.b, upd)
	}
}

func need(t *testing.T, haystack string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(haystack, w) {
			t.Fatalf("нет %q в:\n%s", w, haystack)
		}
	}
}

func TestVoiceFlowWithDateClarification(t *testing.T) {
	e := newEnv(t)
	// сегмент «сегодня», ретро-сегмент про 01.09 и сегмент без даты
	e.setGPT(func(string) string {
		return "```json\n" + `{"segments":[
		  {"date":"2026-09-05","date_source":"relative","diary":"Я сегодня работал.","mood":"бодро","sleep_hours":7,"alcohol":false},
		  {"date":"2026-09-01","date_source":"explicit","diary":"Вспомнил про первое сентября.","sleep_hours":6,"katya":"были споры"},
		  {"date":null,"date_source":"unknown","to_think":["про переезд"]}
		]}` + "\n```"
	})

	// голосовое отправлено 05.09.2026 в 10:00 МСК
	e.text(100, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "транскрипт")
	all := strings.Join(e.tg.texts(), "\n=====\n")
	need(t, all, "Дата: 05.09.2026", "Я сегодня работал.",
		"Запись о другом дне — добавлена в карточку этого дня.", "Дата: 01.09.2026",
		"Дата: не определена", "К какому дню относится эта запись?")

	// нажимаем «Вчера» в клавиатуре вопроса
	kb := e.tg.lastButtons()
	if len(kb) != 2 || !strings.Contains(kb[0][1].text, "Вчера (04.09)") {
		t.Fatalf("клавиатура: %+v", kb)
	}
	e.press(kb[0][1].data)
	need(t, e.tg.last(), "Дата записи: 04.09.2026")

	// повторное нажатие не должно дублировать наблюдения
	e.press(kb[0][1].data)
	need(t, e.tg.last(), "уже не ждёт уточнения даты")

	// /day 01.09 показывает ретро-данные с пометкой источника
	e.say("/day 01.09")
	need(t, e.tg.last(), "Дата: 01.09.2026", "▸ Дополнение из записи от 05.09 · 10:00", "Вспомнил про первое сентября.", "— Часы сна: 6 ч (из записи от 05.09)")

	// /day 04.09 — данные, добавленные после уточнения даты
	e.say("/day 04.09")
	need(t, e.tg.last(), "про переезд (из записи от 05.09)")
}

func TestWorkingDay(t *testing.T) {
	e := newEnv(t)
	sent1 := time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone) // отправлено 05.09
	e.setGPT(func(user string) string {
		// «модель» считает «сегодня» от опорной даты из сообщения
		d := "2026-09-05"
		if strings.Contains(user, "2026-09-01") {
			d = "2026-09-01"
		}
		return fmt.Sprintf(`{"segments":[{"date":"%s","date_source":"relative","diary":"Запись за %s."}]}`, d, d)
	})

	e.say("/date")
	need(t, e.tg.last(), "Рабочий день не выбран")
	e.say("/date 01.09")
	need(t, e.tg.last(), "Рабочий день: 01.09.2026")
	e.say("/date")
	need(t, e.tg.last(), "Рабочий день: 01.09.2026")

	// голосовое от 05.09 с выбранным днём 01.09: опорная дата для модели — 01.09
	e.text(1, sent1, "про первое сентября")
	need(t, e.lastUser(), "2026-09-01")
	out := strings.Join(e.tg.texts(), "\n")
	if strings.Contains(out, "Запись о другом дне") {
		t.Fatalf("запись о рабочем дне не должна считаться «другим днём»:\n%s", out)
	}

	// сбрасываем: следующее голосовое считается от даты отправки и для 01.09 становится дополнением
	e.say("/date сброс")
	need(t, e.tg.last(), "Рабочий день сброшен")
	e.setGPT(func(string) string {
		return `{"segments":[{"date":"2026-09-01","date_source":"explicit","diary":"Вспомнил позже."}]}`
	})
	e.text(2, time.Date(2026, 9, 5, 11, 0, 0, 0, e.zone), "ещё про первое")
	need(t, e.lastUser(), "2026-09-05")
	need(t, e.tg.last(), "Запись о другом дне", "Вспомнил позже.")

	// карточка 01.09: основная запись (отправлена 05.09 10:00, но отнесена к 01.09) + дополнение
	e.say("/day 01.09")
	need(t, e.tg.last(),
		"▸ Основная запись · 05.09 10:00\nЗапись за 2026-09-01.",
		"▸ Дополнение из записи от 05.09 · 11:00\nВспомнил позже.")

	// «сегодня» как рабочий день = сброс; будущее отклоняется
	e.say("/date 01.09")
	e.say("/date сегодня")
	need(t, e.tg.last(), "Рабочий день сброшен")
	e.say("/date 31.12.2099")
	need(t, e.tg.last(), "в будущем")
	if _, ok, _ := e.db.Setting(e.ctx, referenceDateKey); ok {
		t.Fatal("настройка должна быть сброшена")
	}
}

func TestEditTranscriptFlow(t *testing.T) {
	e := newEnv(t)
	e.setGPT(func(user string) string {
		diary := "Я встретил кате."
		if strings.Contains(user, "Катю") {
			diary = "Я встретил Катю."
		}
		return fmt.Sprintf(`{"segments":[{"date":"2026-09-05","date_source":"relative","diary":"%s","mood":"хорошее"}]}`, diary)
	})
	e.text(1, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "я встретил кате")
	need(t, e.tg.last(), "Я встретил кате.")

	// /edit -> выбрать голосовое
	e.say("/edit")
	kb := e.tg.lastButtons()
	if len(kb) != 1 || !strings.Contains(kb[0][0].text, "05.09 10:00") || !strings.Contains(kb[0][0].text, "я встретил кате") {
		t.Fatalf("список: %+v", kb)
	}
	e.press(kb[0][0].data)
	texts := strings.Join(e.tg.texts(), "\n")
	need(t, texts, "Текстовая запись от 05.09.2026 10:00", "я встретил кате", "Что сделать с этой расшифровкой?")

	// «Найти и заменить»: сначала несуществующее — остаёмся в режиме правки
	var replaceBtn string
	for _, row := range e.tg.lastButtons() {
		if row[0].text == "Найти и заменить" {
			replaceBtn = row[0].data
		}
	}
	e.press(replaceBtn)
	need(t, e.tg.last(), "как было => как надо")
	e.say("зелёный => синий\nплохая строка без разделителя")
	need(t, e.tg.last(), "Ничего не заменено", "«зелёный»: не найдено", "нет «=>»")

	// корректная замена (регистр не важен)
	e.say("КАТЕ => Катю")
	need(t, strings.Join(e.tg.texts(), "\n"), "«КАТЕ» → «Катю»: 1", "Расшифровка обновлена", "Я встретил Катю.")
	if strings.Contains(e.tg.last(), "Я встретил кате") {
		t.Fatalf("старый текст остался: %s", e.tg.last())
	}

	vs, _ := e.db.RecentVoices(e.ctx, 1)
	voiceID := vs[0].ID
	v, _ := e.db.GetVoice(e.ctx, voiceID)
	if v.Transcript != "я встретил Катю" || !v.Edited {
		t.Fatalf("расшифровка: %+v", v)
	}
	// карточка дня пересобрана: старый дневник заменён, не дублируется
	e.say("/day 05.09")
	day := e.tg.last()
	need(t, day, "Я встретил Катю.")
	if strings.Contains(day, "Я встретил кате") || strings.Contains(day, "▸") {
		t.Fatalf("карточка после правки:\n%s", day)
	}
	// режим правки завершён: обычный текст больше не ждём
	if e.h.isAwaitedInput(&models.Update{Message: &models.Message{Text: "привет", From: &models.User{ID: 1}, Chat: models.Chat{ID: 1}}}) {
		t.Fatal("после правки бот не должен ждать текст")
	}

	// «Заменить целиком»
	e.say("/edit")
	e.press(e.tg.lastButtons()[0][0].data)
	var wholeBtn string
	for _, row := range e.tg.lastButtons() {
		if row[0].text == "Заменить целиком" {
			wholeBtn = row[0].data
		}
	}
	e.press(wholeBtn)
	need(t, e.tg.last(), "целиком")
	e.say("Совсем другой текст про Катю")
	v, _ = e.db.GetVoice(e.ctx, voiceID)
	if v.Transcript != "Совсем другой текст про Катю" {
		t.Fatalf("после замены целиком: %q", v.Transcript)
	}

	// /edit с датой, где голосовых нет
	e.say("/edit 01.01.2026")
	need(t, e.tg.last(), "Голосовых не найдено")
}

func TestExtractFailureOffersRedo(t *testing.T) {
	e := newEnv(t)
	e.setGPT(func(string) string { return "это не JSON" }) // модель сломалась
	e.text(1, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "запись, которую не удалось разобрать")

	all := strings.Join(e.tg.texts(), "\n")
	need(t, all, "Не удалось разобрать запись, вот расшифровка:", "запись, которую не удалось разобрать", "Повторить разбор записи?")
	btns := e.tg.lastButtons()
	if len(btns) != 1 || btns[0][0].text != "Повторить разбор" {
		t.Fatalf("кнопки: %+v", btns)
	}

	// модель починилась — повторяем разбор
	e.setGPT(func(string) string {
		return `{"segments":[{"date":"2026-09-05","date_source":"relative","diary":"Теперь разобралось."}]}`
	})
	e.press(btns[0][0].data)
	need(t, strings.Join(e.tg.texts(), "\n"), "Пересобираю запись", "Теперь разобралось.")
	e.say("/day 05.09")
	need(t, e.tg.last(), "Теперь разобралось.")
}

func TestUnauthorizedUserIgnored(t *testing.T) {
	e := newEnv(t)
	before := len(e.tg.texts())
	intruder := &models.Update{Message: &models.Message{Text: "/day", From: &models.User{ID: 666}, Chat: models.Chat{ID: 1}}}
	e.h.onDay(e.ctx, e.b, intruder)
	e.h.onDate(e.ctx, e.b, intruder)
	e.h.onEdit(e.ctx, e.b, intruder)
	e.h.onPending(e.ctx, e.b, intruder)
	e.h.onHelp(e.ctx, e.b, intruder)
	e.h.onCategories(e.ctx, e.b, intruder)
	if e.h.isTextEntry(intruder) || e.h.isTextEntry(&models.Update{Message: &models.Message{Text: "обычный текст", From: &models.User{ID: 666}}}) {
		t.Fatal("текст от постороннего не должен становиться записью")
	}
	e.h.onCategoryCallback(e.ctx, e.b, &models.Update{CallbackQuery: &models.CallbackQuery{ID: "x", From: models.User{ID: 666}, Data: "c:n"}})
	e.h.onEditCallback(e.ctx, e.b, &models.Update{CallbackQuery: &models.CallbackQuery{ID: "x", From: models.User{ID: 666}, Data: "ef:1"}})
	e.h.onDateCallback(e.ctx, e.b, &models.Update{CallbackQuery: &models.CallbackQuery{ID: "x", From: models.User{ID: 666}, Data: "d:1:x"}})
	if len(e.tg.texts()) != before {
		t.Fatalf("бот ответил постороннему: %v", e.tg.texts()[before:])
	}
}

func TestHelpListsAllCommands(t *testing.T) {
	e := newEnv(t)
	e.say("/help")
	help := e.tg.last()
	// каждая зарегистрированная команда должна быть описана в справке
	need(t, help, "/day", "/date", "/edit", "/pending", "/categories", "/help", "05:00", "не упоминалось", "=>", "сброс", "текст", "✓ Верно", "психолога", "Удалить запись")
	if n := len([]rune(help)); n > 4096 {
		t.Fatalf("справка не влезает в одно сообщение Telegram: %d символов", n)
	}
}

func TestSeveralPendingSegmentsAreDistinguishable(t *testing.T) {
	e := newEnv(t)
	e.setGPT(func(string) string {
		return `{"segments":[
		  {"date":null,"date_source":"unknown","diary":"Первая запись про работу."},
		  {"date":"2026-09-05","date_source":"relative","diary":"Сегодняшняя запись."},
		  {"date":null,"date_source":"unknown","diary":"Вторая запись про сон."}
		]}`
	})
	e.text(1, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "транскрипт")
	all := strings.Join(e.tg.texts(), "\n=====\n")
	need(t, all, "К какому дню относится запись 1 из 2?", "«Первая запись про работу.»",
		"К какому дню относится запись 2 из 2?", "«Вторая запись про сон.»")

	// оба вопроса независимы: отвечаем на последний, затем через /pending остаётся первый
	e.press(e.tg.lastButtons()[0][1].data) // «Вчера» для второго
	e.say("/pending")
	texts := e.tg.texts()
	last := texts[len(texts)-1]
	need(t, last, "К какому дню относится эта запись?", "«Первая запись про работу.»")
	if strings.Contains(last, "сон") {
		t.Fatalf("закрытый сегмент снова в списке: %s", last)
	}
}

func (e *env) buttonWith(prefix string) string {
	e.t.Helper()
	for _, row := range e.tg.lastButtons() {
		for _, b := range row {
			if strings.HasPrefix(b.data, prefix) {
				return b.data
			}
		}
	}
	e.t.Fatalf("нет кнопки с данными %q среди %+v", prefix, e.tg.lastButtons())
	return ""
}

func (e *env) buttonText(label string) string {
	e.t.Helper()
	for _, row := range e.tg.lastButtons() {
		for _, b := range row {
			if b.text == label {
				return b.data
			}
		}
	}
	e.t.Fatalf("нет кнопки %q среди %+v", label, e.tg.lastButtons())
	return ""
}

func TestTextEntryFlow(t *testing.T) {
	e := newEnv(t)
	sent := time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone)
	e.setGPT(func(string) string {
		return `{"segments":[{"date":"2026-09-05","date_source":"relative","diary":"Записал текстом.","mood":"бодро"}]}`
	})

	// обычное сообщение без «/» — это запись
	e.sayAt("Сегодня я записал текстом свои мысли", sent)
	all := strings.Join(e.tg.texts(), "\n=====\n")
	need(t, all, "Запись получена, разбираю", "Записал текстом.", "бодро")
	if strings.Contains(all, "Расшифровка:") || strings.Contains(all, "Всё верно?") {
		t.Fatalf("для текстовой записи подтверждение не нужно:\n%s", all)
	}
	recent, _ := e.db.RecentVoices(e.ctx, 5)
	if len(recent) != 1 || recent[0].Source != "text" || !recent[0].Confirmed || recent[0].DurationSec != 0 {
		t.Fatalf("запись: %+v", recent)
	}
	v, _ := e.db.GetVoice(e.ctx, recent[0].ID)
	if v.Transcript != "Сегодня я записал текстом свои мысли" {
		t.Fatalf("текст записи: %q", v.Transcript)
	}
	e.say("/day 05.09")
	need(t, e.tg.last(), "Записал текстом.")
	if strings.Contains(e.tg.last(), "⚠") {
		t.Fatal("текстовая запись не требует подтверждения")
	}

	// команды записью не становятся
	if e.h.isTextEntry(&models.Update{Message: &models.Message{Text: "/day", From: &models.User{ID: 1}}}) {
		t.Fatal("команда принята за запись")
	}

	// ответ на вопрос бота важнее новой записи: текст «03.09» уходит в дату, а не в запись
	e.setGPT(func(string) string {
		return `{"segments":[{"date":null,"date_source":"unknown","diary":"Про неизвестный день."}]}`
	})
	e.sayAt("Что-то без даты", sent.Add(time.Hour))
	e.press(e.buttonText("Другая дата"))
	need(t, e.tg.last(), "Напишите дату")
	before, _ := e.db.RecentVoices(e.ctx, 10)
	e.sayAt("03.09", sent.Add(2*time.Hour))
	after, _ := e.db.RecentVoices(e.ctx, 10)
	if len(after) != len(before) {
		t.Fatalf("ответ на вопрос о дате стал новой записью: %d -> %d", len(before), len(after))
	}
	need(t, e.tg.last(), "Дата записи: 03.09.2026")
}

func TestPendingInputExpires(t *testing.T) {
	e := newEnv(t)
	text := func(s string) *models.Update {
		return &models.Update{Message: &models.Message{Text: s, From: &models.User{ID: 1}, Chat: models.Chat{ID: 1}}}
	}
	e.h.setInput(1, pendingInput{kind: inputDate, id: 1})
	if !e.h.isAwaitedInput(text("05.09")) {
		t.Fatal("бот должен ждать ответ")
	}
	// ждём слишком давно — дальше обычный текст снова считается записью
	e.h.mu.Lock()
	in := e.h.input[1]
	in.at = time.Now().Add(-2 * inputTTL)
	e.h.input[1] = in
	e.h.mu.Unlock()
	if e.h.isAwaitedInput(text("05.09")) {
		t.Fatal("устаревшее ожидание должно сбрасываться")
	}
	if !e.h.isTextEntry(text("05.09")) {
		t.Fatal("после истечения ожидания текст — новая запись")
	}
}

func TestVoiceConfirmationFlow(t *testing.T) {
	e := newEnv(t)
	sent := time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone)
	e.setGPT(func(string) string {
		return `{"segments":[{"date":"2026-09-05","date_source":"relative","diary":"Голосовая запись."}]}`
	})

	// запись обрабатывается как обычно, затем бот просит подтвердить расшифровку
	e.voice(1, sent, "расшифровка голосового")
	all := strings.Join(e.tg.texts(), "\n=====\n")
	need(t, all, "Голосовая запись.", "Расшифровка:\nрасшифровка голосового\n\nВсё верно?")
	confirmData := e.buttonText("✓ Верно")
	menuData := e.buttonText("✎ Править")
	if !strings.HasPrefix(confirmData, "ek:") || !strings.HasPrefix(menuData, "ex:") {
		t.Fatalf("кнопки: %q %q", confirmData, menuData)
	}

	// пока не подтверждено — пометка в карточке дня и подсказка про /pending
	e.say("/day 05.09")
	texts := strings.Join(e.tg.texts(), "\n")
	need(t, texts, "⚠ Есть неподтверждённые расшифровки", "Есть неподтверждённых расшифровок: 1 — /pending")

	// /pending: список неподтверждённых, выбрать запись
	e.say("/pending")
	need(t, e.tg.last(), "Расшифровки, ожидающие подтверждения (1)")
	kb := e.tg.lastButtons()
	if len(kb) != 1 || !strings.HasPrefix(kb[0][0].text, "⚠ 05.09 10:00") {
		t.Fatalf("список: %+v", kb)
	}
	e.press(kb[0][0].data)
	texts = strings.Join(e.tg.texts(), "\n")
	need(t, texts, "не подтверждена", "расшифровка голосового", "Что сделать с этой расшифровкой?")
	e.press(e.buttonText("✓ Подтвердить"))
	need(t, e.tg.last(), "✓ Подтверждено")

	e.say("/day 05.09")
	// последнее сообщение — карточка дня; предупреждения и подсказки про /pending быть не должно
	if strings.Contains(e.tg.last(), "⚠") || strings.Contains(e.tg.last(), "/pending") {
		t.Fatalf("после подтверждения пометка осталась:\n%s", e.tg.last())
	}
	e.say("/pending")
	need(t, e.tg.last(), "Всё уточнено и подтверждено")

	// подтверждение кнопкой прямо под расшифровкой
	e.voice(2, sent.Add(time.Hour), "вторая расшифровка")
	e.press(e.buttonText("✓ Верно"))
	if n, _ := e.db.CountUnconfirmed(e.ctx); n != 0 {
		t.Fatalf("после «Верно» неподтверждённых: %d", n)
	}

	// правка тоже подтверждает: «Править» → найти и заменить
	e.voice(3, sent.Add(2*time.Hour), "третья расшифровка с ошибкой")
	if n, _ := e.db.CountUnconfirmed(e.ctx); n != 1 {
		t.Fatalf("неподтверждённых: %d", n)
	}
	e.press(e.buttonText("✎ Править"))
	need(t, strings.Join(e.tg.texts(), "\n"), "✎ Правка…", "Что сделать с этой расшифровкой?")
	e.press(e.buttonText("Найти и заменить"))
	e.say("ошибкой => исправлением")
	need(t, strings.Join(e.tg.texts(), "\n"), "считается подтверждённой")
	if n, _ := e.db.CountUnconfirmed(e.ctx); n != 0 {
		t.Fatalf("после правки неподтверждённых: %d", n)
	}
}

func TestCategoriesFlow(t *testing.T) {
	e := newEnv(t)
	sent := time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone)

	e.say("/categories")
	need(t, e.tg.last(), "Категории записи (22)", "1. Дневниковая запись — текст", "Хобби — текст",
		"Пэт-проект — текст", "Часы сна — число · Сон", "Сладкое — да/нет · Привычки")
	e.buttonText("➕ Добавить категорию")

	// мастер добавления: название → тип → подсказка → единица → группа
	e.press("c:n")
	need(t, e.tg.last(), "Как её назвать")
	e.say("хобби") // дубль названия (без учёта регистра)
	need(t, e.tg.last(), "уже есть")
	e.say("Бег")
	need(t, e.tg.last(), "Тип значения")
	e.say("просто текст вместо кнопки")
	need(t, e.tg.last(), "Выберите вариант кнопкой")
	e.press("c:k:number")
	need(t, e.tg.last(), "Что модель должна записывать")
	e.say("сколько километров пробежал")
	need(t, e.tg.last(), "Единица измерения")
	e.say("км")
	need(t, e.tg.last(), "Выберите группу")
	e.press("c:w:-")
	need(t, e.tg.last(), "Категория «Бег» (число) добавлена")

	schema, _ := e.db.Schema(e.ctx)
	c, ok := schema.Lookup("beg")
	if !ok || c.Kind != entry.Number || c.Unit != "км" || c.Hint != "сколько километров пробежал" || c.Group != "" || !c.Active {
		t.Fatalf("созданная категория: %+v %v", c, ok)
	}

	// новая категория сразу попадает в промпт и в карточку
	e.setGPT(func(string) string {
		return `{"segments":[{"date":"2026-09-05","date_source":"relative","diary":"Бегал.","beg":5.5}]}`
	})
	e.sayAt("Сегодня пробежал пять с половиной километров", sent)
	need(t, e.lastSystem(), "beg — число (в км). Бег: сколько километров пробежал")
	need(t, strings.Join(e.tg.texts(), "\n"), "Бег\n5.5 км")
	e.say("/day 05.09")
	need(t, e.tg.last(), "Бег\n5.5 км")

	// переименование, подсказка, единица
	e.press("c:m:beg")
	need(t, e.tg.last(), "Категория «Бег»", "Тип: число", "Единица: км")
	e.press("c:r:beg")
	e.say("Бег трусцой")
	need(t, e.tg.last(), "Категория «Бег трусцой»")
	e.press("c:h:beg")
	e.say("-")
	schema, _ = e.db.Schema(e.ctx)
	if c, _ := schema.Lookup("beg"); c.Title != "Бег трусцой" || c.Hint != "" {
		t.Fatalf("после переименования и очистки подсказки: %+v", c)
	}
	e.press("c:t:beg")
	e.say("метров")
	schema, _ = e.db.Schema(e.ctx)
	if c, _ := schema.Lookup("beg"); c.Unit != "метров" {
		t.Fatalf("единица: %+v", c)
	}

	// группа: новая, затем существующая
	e.press("c:g:beg")
	e.press("c:s:beg:+")
	need(t, e.tg.last(), "Название новой группы")
	e.say("Спорт")
	schema, _ = e.db.Schema(e.ctx)
	if c, _ := schema.Lookup("beg"); c.Group != "Спорт" {
		t.Fatalf("новая группа: %+v", c)
	}
	e.press("c:g:beg")
	groups := schema.Groups() // Сон, Основные занятия, Привычки, Спорт
	idx := -1
	for i, g := range groups {
		if g == "Привычки" {
			idx = i
		}
	}
	e.press(fmt.Sprintf("c:s:beg:%d", idx))
	schema, _ = e.db.Schema(e.ctx)
	if c, _ := schema.Lookup("beg"); c.Group != "Привычки" {
		t.Fatalf("существующая группа: %+v", c)
	}

	// порядок: «выше» меняет местами с соседней
	active := schema.Active()
	last, prev := active[len(active)-1], active[len(active)-2]
	if last.Key != "beg" {
		t.Fatalf("новая категория должна быть последней: %s", last.Key)
	}
	e.press("c:u:beg")
	schema, _ = e.db.Schema(e.ctx)
	active = schema.Active()
	if active[len(active)-1].Key != prev.Key || active[len(active)-2].Key != "beg" {
		t.Fatalf("порядок после сдвига: …%s, %s", active[len(active)-2].Key, active[len(active)-1].Key)
	}

	// скрытие: diary защищена, остальные — с подтверждением; данные сохраняются, вернуть можно
	e.press("c:x:diary")
	schema, _ = e.db.Schema(e.ctx)
	if c, _ := schema.Lookup("diary"); !c.Active {
		t.Fatal("diary скрыть нельзя")
	}
	e.press("c:x:beg")
	need(t, e.tg.last(), "Скрыть категорию «Бег трусцой»?")
	e.press("c:xx:beg")
	e.buttonText("Скрытые (1)") // после скрытия в списке появилась кнопка со скрытыми
	schema, _ = e.db.Schema(e.ctx)
	if c, _ := schema.Lookup("beg"); c.Active {
		t.Fatal("категория должна быть скрыта")
	}
	e.say("/day 05.09")
	if strings.Contains(e.tg.last(), "Бег трусцой") {
		t.Fatalf("скрытая категория в карточке:\n%s", e.tg.last())
	}
	e.press("c:v")
	need(t, e.tg.last(), "Скрытые категории")
	e.press("c:m:beg")
	need(t, e.tg.last(), "(скрыта)")
	e.press("c:res:beg")
	e.say("/day 05.09")
	need(t, e.tg.last(), "Бег трусцой") // данные никуда не делись

	// кнопка мастера без начатого мастера ничего не ломает
	e.press("c:k:text")
	e.press("c:w:-")
	schema, _ = e.db.Schema(e.ctx)
	if len(schema.All) != len(entry.DefaultCategories())+1 {
		t.Fatalf("лишние категории: %d", len(schema.All))
	}
}

func TestDeleteEntryFlow(t *testing.T) {
	e := newEnv(t)
	sent := time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone)
	e.setGPT(func(user string) string {
		diary := "Первая запись дня."
		if strings.Contains(user, "вторая") {
			diary = "Вторая запись дня."
		}
		return fmt.Sprintf(`{"segments":[{"date":"2026-09-05","date_source":"relative","diary":"%s","mood":"ок"}]}`, diary)
	})
	e.text(1, sent, "первая")
	e.text(2, sent.Add(time.Hour), "вторая")
	e.voice(3, sent.Add(2*time.Hour), "третья голосовая")

	e.say("/day 05.09")
	need(t, e.tg.beforeLast(), "Первая запись дня.", "Вторая запись дня.") // дальше — подсказка про неподтверждённую голосовую

	// выбираем вторую запись → меню содержит удаление
	e.say("/edit")
	var secondBtn string
	for _, row := range e.tg.lastButtons() {
		if strings.Contains(row[0].text, "вторая") {
			secondBtn = row[0].data
		}
	}
	if secondBtn == "" {
		t.Fatalf("нет кнопки второй записи: %+v", e.tg.lastButtons())
	}
	e.press(secondBtn)
	deleteData := e.buttonText("🗑 Удалить запись")

	// сначала подтверждение с описанием последствий; отмена ничего не удаляет
	e.press(deleteData)
	need(t, e.tg.last(), "Удалить запись от 05.09.2026 11:00 навсегда?", "«вторая»", "Будет удалено: текст",
		"всё извлечённое из неё (2 значений за дни: 05.09)", "Остальные записи этих дней останутся", "нельзя")
	confirmData := e.buttonText("Удалить безвозвратно")
	e.press(e.buttonText("Отмена"))
	need(t, e.tg.last(), "Отменено.")
	recent, _ := e.db.RecentVoices(e.ctx, 10)
	if len(recent) != 3 {
		t.Fatalf("после отмены записей: %d", len(recent))
	}

	// ждём правку именно этой записи — после удаления ожидание сбрасывается
	secondID, _ := strconv.ParseInt(strings.TrimPrefix(deleteData, "ed:"), 10, 64)
	e.h.setInput(1, pendingInput{kind: inputReplace, id: secondID})

	e.press(confirmData)
	need(t, e.tg.last(), "Запись удалена ✓", "Удалено значений: 2", "Затронутые дни: 05.09.2026")
	if _, ok := e.h.pendingInput(1); ok {
		t.Fatal("ожидание правки удалённой записи должно сбрасываться")
	}
	if _, err := e.db.GetVoice(e.ctx, secondID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("запись должна быть удалена: %v", err)
	}

	// карточка дня пересобрана без удалённой записи, остальные на месте
	e.say("/day 05.09")
	day := e.tg.beforeLast()
	need(t, day, "Первая запись дня.")
	if strings.Contains(day, "Вторая запись дня.") {
		t.Fatalf("удалённая запись осталась в карточке:\n%s", day)
	}

	// повторное удаление и устаревшие кнопки не ломаются
	e.press(confirmData)
	need(t, e.tg.last(), "Запись уже удалена.")
	e.press(deleteData)
	need(t, e.tg.last(), "Запись уже удалена.")

	// неподтверждённая голосовая: удаляем её — из /pending она исчезает, её кнопка «Верно» больше не работает
	pend, _ := e.db.UnconfirmedVoices(e.ctx, 10)
	if len(pend) != 1 {
		t.Fatalf("неподтверждённых: %d", len(pend))
	}
	e.press(fmt.Sprintf("ed:%d", pend[0].ID))
	e.press(fmt.Sprintf("ey:%d", pend[0].ID))
	need(t, e.tg.last(), "Запись удалена ✓")
	e.say("/pending")
	need(t, e.tg.last(), "Всё уточнено и подтверждено")
	e.press(fmt.Sprintf("ek:%d", pend[0].ID))
	need(t, e.tg.last(), "Запись не найдена.")

	// осталась только первая запись
	left, _ := e.db.RecentVoices(e.ctx, 10)
	if len(left) != 1 || left[0].Preview != "первая" {
		t.Fatalf("осталось: %+v", left)
	}
}
