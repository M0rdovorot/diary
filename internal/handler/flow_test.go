package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"

	"diary/internal/diaryday"
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

	mu    sync.Mutex
	gpt   func(user string) string // ответ «модели» по пользовательскому сообщению
	users []string                 // что получила «модель»
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
		user := req.Messages[len(req.Messages)-1].Text
		e.mu.Lock()
		e.users = append(e.users, user)
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

func (e *env) lastUser() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.users[len(e.users)-1]
}

// voice эмулирует обработку голосового, отправленного в момент sent (после распознавания).
func (e *env) voice(msgID int, sent time.Time, text string) {
	logical := e.h.Clock.LogicalDate(sent)
	ref := e.h.referenceDate(e.ctx, logical)
	e.h.processTranscript(e.ctx, e.b, 1, msgID, sent, ref, 60, text)
}

func (e *env) say(text string) {
	e.t.Helper()
	upd := &models.Update{Message: &models.Message{Text: text, From: &models.User{ID: 1}, Chat: models.Chat{ID: 1}}}
	switch {
	case strings.HasPrefix(text, "/day"):
		e.h.onDay(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/date"):
		e.h.onDate(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/edit"):
		e.h.onEdit(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/pending"):
		e.h.onPending(e.ctx, e.b, upd)
	case strings.HasPrefix(text, "/help"):
		e.h.onHelp(e.ctx, e.b, upd)
	default:
		if !e.h.isAwaitedInput(upd) {
			e.t.Fatalf("бот не ждёт текст, но получил %q", text)
		}
		e.h.onAwaitedInput(e.ctx, e.b, upd)
	}
}

// press нажимает кнопку: маршрутизация по префиксу callback_data, как в Register.
func (e *env) press(data string) {
	upd := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb", From: models.User{ID: 1}, Data: data,
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{ID: 7, Chat: models.Chat{ID: 1}}},
	}}
	if strings.HasPrefix(data, dateCallbackPrefix) {
		e.h.onDateCallback(e.ctx, e.b, upd)
	} else {
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
	e.voice(100, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "транскрипт")
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
	need(t, e.tg.last(), "Дата: 01.09.2026", "▸ Дополнение из записи от 05.09 · 10:00", "Вспомнил про первое сентября.", "— 6 ч (из записи от 05.09)")

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
	e.voice(1, sent1, "про первое сентября")
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
	e.voice(2, time.Date(2026, 9, 5, 11, 0, 0, 0, e.zone), "ещё про первое")
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
	e.voice(1, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "я встретил кате")
	need(t, e.tg.last(), "Я встретил кате.")

	// /edit -> выбрать голосовое
	e.say("/edit")
	kb := e.tg.lastButtons()
	if len(kb) != 1 || !strings.Contains(kb[0][0].text, "05.09 10:00") || !strings.Contains(kb[0][0].text, "я встретил кате") {
		t.Fatalf("список: %+v", kb)
	}
	e.press(kb[0][0].data)
	texts := strings.Join(e.tg.texts(), "\n")
	need(t, texts, "Расшифровка голосового от 05.09.2026 10:00", "я встретил кате", "Что сделать с этой расшифровкой?")

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
	e.voice(1, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "запись, которую не удалось разобрать")

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
	need(t, help, "/day", "/date", "/edit", "/pending", "/help", "05:00", "не упоминалось", "=>", "сброс")
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
	e.voice(1, time.Date(2026, 9, 5, 10, 0, 0, 0, e.zone), "транскрипт")
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
