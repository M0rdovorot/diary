package webapp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"diary/internal/entry"
	"diary/internal/store"
)

// categoryDTO — категория для интерфейса.
type categoryDTO struct {
	Key          string `json:"key"`
	Title        string `json:"title"`
	Kind         string `json:"kind"` // text | list | number | bool
	KindLabel    string `json:"kindLabel"`
	Hint         string `json:"hint"`
	Unit         string `json:"unit"`
	Group        string `json:"group"`
	Active       bool   `json:"active"`
	Protected    bool   `json:"protected"` // скрыть и удалить нельзя
	Observations int    `json:"observations"`
	Days         int    `json:"days"`
}

type categoriesResponse struct {
	Categories []categoryDTO `json:"categories"`
	Groups     []string      `json:"groups"` // группы активных категорий
	Limits     struct {
		Title int `json:"title"`
		Hint  int `json:"hint"`
		Unit  int `json:"unit"`
		Group int `json:"group"`
	} `json:"limits"`
}

func toDTO(c entry.Category, use store.CategoryUse) categoryDTO {
	return categoryDTO{
		Key: c.Key, Title: c.Title, Kind: entry.KindName(c.Kind), KindLabel: entry.KindLabel(c.Kind),
		Hint: c.Hint, Unit: c.Unit, Group: c.Group, Active: c.Active, Protected: c.Key == entry.DiaryKey,
		Observations: use.Observations, Days: use.Days,
	}
}

// listCategories — все категории (и скрытые) в порядке отображения: GET /api/categories.
func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		s.fail(w, "schema", err)
		return
	}
	usage, err := s.Store.CategoryUsage(ctx)
	if err != nil {
		s.fail(w, "category usage", err)
		return
	}
	resp := categoriesResponse{Categories: []categoryDTO{}, Groups: schema.Groups()}
	resp.Limits.Title, resp.Limits.Hint, resp.Limits.Unit, resp.Limits.Group =
		entry.MaxTitleRunes, entry.MaxHintRunes, entry.MaxUnitRunes, entry.MaxGroupRunes
	for _, c := range schema.All {
		resp.Categories = append(resp.Categories, toDTO(c, usage[c.Key]))
	}
	if resp.Groups == nil {
		resp.Groups = []string{}
	}
	writeJSON(w, resp)
}

type createCategoryRequest struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Hint  string `json:"hint"`
	Unit  string `json:"unit"`
	Group string `json:"group"`
}

// createCategory — новая категория в конец списка: POST /api/categories.
func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	var req createCategoryRequest
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		s.fail(w, "schema", err)
		return
	}
	title, msg := entry.CleanTitle(schema, req.Title, "")
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	kind, ok := entry.ParseKind(req.Kind)
	if !ok {
		writeError(w, http.StatusBadRequest, "Неизвестный тип категории.")
		return
	}
	hint, unit, group, msg := cleanCategoryProps(req.Hint, req.Unit, req.Group)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if kind != entry.Number {
		unit = ""
	}
	key := entry.SlugKey(title, func(k string) bool { _, taken := schema.Lookup(k); return taken })
	c, err := s.Store.AddCategory(ctx, entry.Category{Key: key, Title: title, Kind: kind, Hint: hint, Unit: unit, Group: group})
	if err != nil {
		s.fail(w, "add category", err)
		return
	}
	s.notify(ctx, fmt.Sprintf("🗂 Календарь: добавлена категория «%s» (%s). Применится к новым записям; к прежним — после пересборки.", c.Title, entry.KindLabel(c.Kind)))
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, toDTO(c, store.CategoryUse{}))
}

type updateCategoryRequest struct {
	Title  *string `json:"title"`
	Hint   *string `json:"hint"`
	Unit   *string `json:"unit"`
	Group  *string `json:"group"`
	Active *bool   `json:"active"`
}

// updateCategory меняет название, подсказку, единицу, группу или скрывает/возвращает категорию:
// PATCH /api/categories/{key}. Тип не меняется.
func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	var req updateCategoryRequest
	if !readJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	key := r.PathValue("key")
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		s.fail(w, "schema", err)
		return
	}
	cur, ok := schema.Lookup(key)
	if !ok {
		writeError(w, http.StatusNotFound, "Категория не найдена.")
		return
	}
	if req.Active != nil && *req.Active && !cur.Active {
		if other, dup := schema.FindByTitle(cur.Title); dup && other.Key != key {
			writeError(w, http.StatusConflict, "Есть другая категория с таким названием — сначала переименуйте её или эту.")
			return
		}
	}
	var u store.CategoryUpdate
	if req.Title != nil {
		title, msg := entry.CleanTitle(schema, *req.Title, key)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		u.Title = &title
	}
	hintIn, unitIn, groupIn := deref(req.Hint), deref(req.Unit), deref(req.Group)
	hint, unit, group, msg := cleanCategoryProps(hintIn, unitIn, groupIn)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if req.Hint != nil {
		u.Hint = &hint
	}
	if req.Unit != nil {
		if cur.Kind != entry.Number {
			writeError(w, http.StatusBadRequest, "Единица измерения есть только у числовых категорий.")
			return
		}
		u.Unit = &unit
	}
	if req.Group != nil {
		u.Group = &group
	}
	u.Active = req.Active
	if err := s.Store.UpdateCategory(ctx, key, u); errors.Is(err, store.ErrProtected) {
		writeError(w, http.StatusBadRequest, "Эту категорию нельзя скрыть: на ней строится дневниковая запись.")
		return
	} else if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Категория не найдена.")
		return
	} else if err != nil {
		s.fail(w, "update category", err)
		return
	}
	title := cur.Title
	if u.Title != nil {
		title = *u.Title
	}
	switch {
	case req.Active != nil && !*req.Active:
		s.notify(ctx, "🗂 Календарь: категория «"+title+"» скрыта. Данные сохранены, вернуть можно в любой момент.")
	case req.Active != nil && *req.Active:
		s.notify(ctx, "🗂 Календарь: категория «"+title+"» возвращена. Применится к новым записям; к прежним — после пересборки.")
	default:
		s.notify(ctx, "🗂 Календарь: изменена категория «"+title+"». Применится к новым записям; к прежним — после пересборки.")
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type moveRequest struct {
	Delta int `json:"delta"`
}

// moveCategory сдвигает категорию на одно место: POST /api/categories/{key}/move {delta: -1|1}.
func (s *Server) moveCategory(w http.ResponseWriter, r *http.Request) {
	var req moveRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Delta != -1 && req.Delta != 1 {
		writeError(w, http.StatusBadRequest, "Сдвиг — на одно место вверх или вниз.")
		return
	}
	if err := s.Store.MoveCategory(r.Context(), r.PathValue("key"), req.Delta); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Категория не найдена или скрыта.")
		return
	} else if err != nil {
		s.fail(w, "move category", err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// deleteCategory навсегда удаляет скрытую категорию вместе с её данными: DELETE /api/categories/{key}.
func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.PathValue("key")
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		s.fail(w, "schema", err)
		return
	}
	cur, ok := schema.Lookup(key)
	if !ok {
		writeError(w, http.StatusNotFound, "Категория не найдена.")
		return
	}
	removed, err := s.Store.DeleteCategory(ctx, key)
	switch {
	case errors.Is(err, store.ErrProtected):
		writeError(w, http.StatusBadRequest, "Эту категорию нельзя удалить: на ней строится дневниковая запись.")
		return
	case errors.Is(err, store.ErrNotHidden):
		writeError(w, http.StatusConflict, "Сначала скройте категорию: удалить навсегда можно только скрытую.")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Категория уже удалена.")
		return
	case err != nil:
		s.fail(w, "delete category", err)
		return
	}
	s.Log.Info("webapp category deleted", "key", key, "observations", removed)
	s.notify(ctx, fmt.Sprintf("🗑 Календарь: категория «%s» удалена навсегда (значений стёрто: %d).", cur.Title, removed))
	writeJSON(w, map[string]int{"removed": removed})
}

// ---------- список по дням ----------

type categoryDay struct {
	Date        string         `json:"date"`
	Title       string         `json:"title"`
	Item        entry.ItemView `json:"item"`
	Unconfirmed bool           `json:"unconfirmed"`
}

type categoryDaysResponse struct {
	Category categoryDTO   `json:"category"`
	Days     []categoryDay `json:"days"` // новые дни первыми
}

// categoryDays — все значения категории по дням: GET /api/categories/{key}/days.
// Значения за день слиты по тем же правилам, что в карточке дня; работает и для скрытых категорий.
func (s *Server) categoryDays(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	schema, err := s.Store.Schema(ctx)
	if err != nil {
		s.fail(w, "schema", err)
		return
	}
	cat, ok := schema.Lookup(r.PathValue("key"))
	if !ok {
		writeError(w, http.StatusNotFound, "Категория не найдена.")
		return
	}
	obs, err := s.Store.CategoryObservations(ctx, cat.Key)
	if err != nil {
		s.fail(w, "category observations", err)
		return
	}

	resp := categoryDaysResponse{Days: []categoryDay{}}
	var days []categoryDay
	for i := 0; i < len(obs); {
		j := i
		var group []entry.Observation
		for ; j < len(obs) && obs[j].Date.Equal(obs[i].Date); j++ {
			group = append(group, obs[j].Observation)
		}
		date := obs[i].Date
		card := entry.BuildCard(date, s.Clock.Loc, group)
		cd := categoryDay{Date: date.Format("2006-01-02"), Title: dayTitle(date), Item: card.Item(cat)}
		for _, v := range card.Voices {
			cd.Unconfirmed = cd.Unconfirmed || !v.Confirmed
		}
		days = append(days, cd)
		i = j
	}
	for i := len(days) - 1; i >= 0; i-- {
		resp.Days = append(resp.Days, days[i])
	}
	resp.Category = toDTO(cat, store.CategoryUse{Observations: len(obs), Days: len(days)})
	writeJSON(w, resp)
}

// ---------- помощники ----------

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// cleanCategoryProps проверяет и очищает подсказку, единицу и группу.
func cleanCategoryProps(hint, unit, group string) (string, string, string, string) {
	one := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	hint, unit, group = one(hint), one(unit), one(group)
	switch {
	case len([]rune(hint)) > entry.MaxHintRunes:
		return "", "", "", fmt.Sprintf("Подсказка слишком длинная (%d символов, максимум %d).", len([]rune(hint)), entry.MaxHintRunes)
	case len([]rune(unit)) > entry.MaxUnitRunes:
		return "", "", "", fmt.Sprintf("Единица измерения слишком длинная (максимум %d символов).", entry.MaxUnitRunes)
	case len([]rune(group)) > entry.MaxGroupRunes:
		return "", "", "", fmt.Sprintf("Название группы слишком длинное (максимум %d символов).", entry.MaxGroupRunes)
	}
	return hint, unit, group, ""
}
