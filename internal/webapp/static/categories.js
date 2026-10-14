'use strict';

// Категории: список и управление, список значений категории по дням, пересборка дней.
// Использует общие помощники из app.js (api, el, button, showScreen, …).

const catState = { data: null, edit: null };

// ---------- список ----------

async function openCategories(push = true) {
  let data;
  try {
    data = await api('/api/categories');
  } catch (e) {
    return alertBox(e.message);
  }
  catState.data = data;
  renderCategories(data);
  showScreen('categories', push);
  resumeRebuild($('cat-rebuild-box'), $('cat-rebuild'));
}

function renderCategories(data) {
  const list = $('cat-list');
  list.replaceChildren();
  const active = data.categories.filter((c) => c.active);
  const hidden = data.categories.filter((c) => !c.active);
  active.forEach((c, i) => list.append(catRow(c, i > 0, i < active.length - 1)));
  if (hidden.length) {
    list.append(el('h2', null, 'Скрытые (' + hidden.length + ')'),
      el('p', 'hint', 'Скрытая категория не попадает в новые записи и карточки, но её данные сохранены. Вернуть или удалить навсегда — через ✎.'));
    for (const c of hidden) list.append(catRow(c, false, false));
  }
}

function catRow(c, canUp, canDown) {
  const meta = [c.kindLabel];
  if (c.unit) meta.push(c.unit);
  if (c.group) meta.push('группа «' + c.group + '»');
  meta.push(c.days ? 'дней: ' + c.days : 'пока пусто');
  const main = el('button', 'cat-main', el('span', 'cat-title', c.title), el('span', 'hint', meta.join(' · ')));
  main.type = 'button';
  main.addEventListener('click', () => openCategoryDays(c.key, c.title));
  const tools = el('div', 'cat-tools');
  if (c.active) {
    const up = button('↑', null, (b) => moveCategory(c.key, -1, b));
    const down = button('↓', null, (b) => moveCategory(c.key, 1, b));
    up.disabled = !canUp;
    down.disabled = !canDown;
    up.setAttribute('aria-label', 'Выше');
    down.setAttribute('aria-label', 'Ниже');
    tools.append(up, down);
  }
  const edit = button('✎', null, () => openCategoryEdit(c));
  edit.setAttribute('aria-label', 'Изменить');
  tools.append(edit);
  return el('div', 'cat-row' + (c.active ? '' : ' hidden-cat'), main, tools);
}

async function moveCategory(key, delta, b) {
  b.disabled = true;
  try {
    await api('/api/categories/' + encodeURIComponent(key) + '/move', { method: 'POST', body: { delta } });
    await openCategories(false);
  } catch (e) {
    b.disabled = false;
    alertBox(e.message);
  }
}

// refreshCategories обновляет список, только если пользователь всё ещё на нём (иначе не уводим с другого экрана).
async function refreshCategories() {
  if (!$('categories').hidden) await openCategories(false);
}

// ---------- создание и правка ----------

function openCategoryEdit(c) {
  catState.edit = c || null;
  const limits = catState.data.limits;
  $('catedit-title').textContent = c ? c.title : 'Новая категория';
  $('ce-title').value = c ? c.title : '';
  $('ce-title').maxLength = limits.title;
  $('ce-kind').value = c ? c.kind : 'text';
  $('ce-kind').disabled = !!c;
  $('ce-kind-note').hidden = !c;
  $('ce-unit').value = c ? c.unit : '';
  $('ce-unit').maxLength = limits.unit;
  $('ce-group').value = c ? c.group : '';
  $('ce-group').maxLength = limits.group;
  $('ce-groups').replaceChildren(...catState.data.groups.map((g) => { const o = document.createElement('option'); o.value = g; return o; }));
  $('ce-hint').value = c ? c.hint : '';
  $('ce-hint').maxLength = limits.hint;
  updateKindFields();
  updateHintCount();
  $('ce-save').disabled = false;

  const actions = $('ce-actions');
  actions.replaceChildren();
  if (c && !c.protected) {
    if (c.active) {
      actions.append(button('Скрыть категорию', null, (b) => hideCategory(c, b)));
    } else {
      actions.append(button('Вернуть категорию', 'primary', (b) => showCategory(c, b)),
        button('Удалить навсегда', 'danger', (b) => deleteCategory(c, b)));
    }
  } else if (c) {
    actions.append(el('p', 'hint', 'Эту категорию нельзя скрыть или удалить: на ней строится дневниковая запись.'));
  }
  showScreen('catedit');
  if (!c) $('ce-title').focus();
}

function updateKindFields() {
  $('ce-unit-wrap').hidden = $('ce-kind').value !== 'number';
}

function updateHintCount() {
  $('ce-count').textContent = $('ce-hint').value.length + ' / ' + catState.data.limits.hint;
}

async function submitCategory(e) {
  e.preventDefault();
  const c = catState.edit;
  const body = {
    title: $('ce-title').value,
    hint: $('ce-hint').value,
    group: $('ce-group').value,
  };
  const kind = $('ce-kind').value;
  if (!c) body.kind = kind;
  if (kind === 'number') body.unit = $('ce-unit').value;
  const save = $('ce-save');
  save.disabled = true;
  try {
    if (c) await api('/api/categories/' + encodeURIComponent(c.key), { method: 'PATCH', body });
    else await api('/api/categories', { method: 'POST', body });
  } catch (err) {
    save.disabled = false;
    return alertBox(err.message);
  }
  haptic('success');
  toast(c ? 'Сохранено' : 'Категория добавлена');
  state.cache.clear();
  await goBack();
}

async function setActive(c, active, b, okText) {
  b.disabled = true;
  try {
    await api('/api/categories/' + encodeURIComponent(c.key), { method: 'PATCH', body: { active } });
    haptic('success');
    toast(okText);
    state.cache.clear();
    await goBack();
  } catch (e) {
    b.disabled = false;
    alertBox(e.message);
  }
}

async function hideCategory(c, b) {
  const text = 'Скрыть «' + c.title + '»? Она пропадёт из новых записей и карточек, данные сохранятся — вернуть можно в любой момент.';
  if (await confirmBox(text)) setActive(c, false, b, 'Категория скрыта');
}

function showCategory(c, b) { return setActive(c, true, b, 'Категория возвращена'); }

async function deleteCategory(c, b) {
  let text = 'Удалить «' + c.title + '» навсегда?';
  if (c.observations) text += ' Сотрётся всё, что накоплено по ней: значений ' + c.observations + ' за ' + c.days + ' дн.';
  text += ' Отменить нельзя.';
  if (!(await confirmBox(text))) return;
  b.disabled = true;
  try {
    await api('/api/categories/' + encodeURIComponent(c.key), { method: 'DELETE' });
    haptic('success');
    toast('Категория удалена');
    state.cache.clear();
    await goBack();
  } catch (e) {
    b.disabled = false;
    alertBox(e.message);
  }
}

// ---------- значения категории по дням ----------

async function openCategoryDays(key, title) {
  let data;
  try {
    data = await api('/api/categories/' + encodeURIComponent(key) + '/days');
  } catch (e) {
    return alertBox(e.message);
  }
  const c = data.category;
  $('catdays-title').textContent = c.title;
  const sub = [c.kindLabel, c.active ? '' : 'скрыта'];
  sub.push(data.days.length ? 'дней со значениями: ' + data.days.length : '');
  $('catdays-sub').textContent = sub.filter(Boolean).join(' · ');

  const list = $('catdays-list');
  list.replaceChildren();
  if (!data.days.length) {
    list.append(el('p', 'none', 'По этой категории пока ничего не записано.'));
  }
  const prev = linkSources;
  linkSources = false;
  try {
    for (const d of data.days) {
      const head = el('button', 'catday-head', el('span', null, d.title), d.unconfirmed && el('span', 'warn', ' ⚠'), el('span', 'chev', '›'));
      head.type = 'button';
      head.title = 'Открыть карточку дня';
      head.addEventListener('click', () => openDay(d.date));
      list.append(el('section', 'catday', head, ...itemBody(d.item)));
    }
  } finally {
    linkSources = prev;
  }
  showScreen('catdays');
}

// ---------- пересборка ----------

let rebuildWatch = null; // box, за которым сейчас следим

// runRebuild спрашивает подтверждение с последствиями, запускает пересборку и показывает ход.
// scope: 'all' | 'day'; date — для 'day'; box — куда писать ход; btn — кнопка запуска; done — после окончания.
async function runRebuild(scope, date, box, btn, done) {
  let plan;
  try {
    plan = await api('/api/rebuild/plan?scope=' + scope + (scope === 'day' ? '&d=' + date : ''));
  } catch (e) {
    return alertBox(e.message);
  }
  if (!plan.voices) return alertBox('Нет записей для пересборки.');

  let text = scope === 'all'
    ? 'Пересобрать все записи (' + plan.voices + ') с текущими категориями? '
    : 'Пересобрать записи этого дня (' + plan.voices + ')? Заденет и другие дни из этих записей. ';
  if (plan.userDates) text += 'Вручную уточнённые дни (' + plan.userDates + ') придётся указать снова. ';
  if (plan.hiddenValues) text += 'Значения скрытых категорий (' + plan.hiddenValues + ') пропадут. ';
  text += 'Если разбор записи не удастся, останется прежний.';
  if (!(await confirmBox(text))) return;

  btn.disabled = true;
  try {
    await api('/api/rebuild', { method: 'POST', body: { scope, date: date || '' } });
  } catch (e) {
    btn.disabled = false;
    return alertBox(e.message);
  }
  await watchRebuild(box, btn, done);
}

// resumeRebuild — если пересборка уже идёт (начата раньше или в другом окне), показать её ход.
async function resumeRebuild(box, btn) {
  try {
    const st = await api('/api/rebuild');
    if (st.state === 'running') {
      btn.disabled = true;
      watchRebuild(box, btn, refreshCategories);
    }
  } catch (e) { /* не критично */ }
}

async function watchRebuild(box, btn, done) {
  if (rebuildWatch === box) return;
  rebuildWatch = box;
  box.hidden = false;
  try {
    for (;;) {
      const st = await api('/api/rebuild');
      if (st.state !== 'running') {
        box.replaceChildren(rebuildSummary(st));
        haptic(st.failed ? 'warning' : 'success');
        state.cache.clear();
        if (done) await done();
        return;
      }
      const n = st.done + st.failed + st.skipped;
      box.replaceChildren(el('div', 'progress', 'Пересобираю записи: ' + n + ' из ' + st.total + '…'),
        el('p', 'hint', 'Можно закрыть приложение — пересборка продолжится на сервере, итог придёт сообщением в чат.'));
      await sleep(2000);
    }
  } catch (e) {
    box.replaceChildren(el('p', 'note', e.message));
  } finally {
    rebuildWatch = null;
    btn.disabled = false;
  }
}

function rebuildSummary(st) {
  let t = '✓ Пересобрано записей: ' + st.done + ' из ' + st.total + '.';
  if (st.failed) t += ' Не удалось: ' + st.failed + ' (у них остался прежний разбор — повторите позже).';
  if (st.skipped) t += ' Пропущено (разбирались в этот момент): ' + st.skipped + '.';
  if (st.pending) t += ' Частей без даты: ' + st.pending + ' — выберите дни в календаре.';
  return el('p', 'note', t);
}

// ---------- запуск ----------

function initCategories() {
  $('cat-new').addEventListener('click', () => openCategoryEdit(null));
  $('cat-rebuild').addEventListener('click', () => runRebuild('all', '', $('cat-rebuild-box'), $('cat-rebuild'), refreshCategories));
  $('catedit-form').addEventListener('submit', submitCategory);
  $('ce-kind').addEventListener('change', updateKindFields);
  $('ce-hint').addEventListener('input', updateHintCount);
}
