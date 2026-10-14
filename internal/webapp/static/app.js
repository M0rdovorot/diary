'use strict';

// Mini App дневника: календарь → карточка дня; добавление, правка, подтверждение и удаление записей.
// Данные вставляются только через textContent (никакого innerHTML с данными).

const tg = window.Telegram && window.Telegram.WebApp;
const initData = tg ? tg.initData : '';

const MONTHS = ['Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь', 'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь'];
const MONTHS_GEN = ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня', 'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'];
const NOT_MENTIONED = 'не упоминалось';
const MAX_TEXT = 10000;
const POLL_MS = 1500;

const $ = (id) => document.getElementById(id);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// el('p', 'cls', child, 'text', …) — элемент с классом и детьми (строки становятся текстом).
function el(tag, cls, ...children) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  for (const c of children) {
    if (c == null || c === false) continue;
    e.append(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return e;
}

function button(text, cls, onClick) {
  const b = el('button', 'btn' + (cls ? ' ' + cls : ''), text);
  b.type = 'button';
  b.addEventListener('click', (e) => { e.stopPropagation(); onClick(b, e); });
  return b;
}

// ---------- даты ----------

// «2026-09-05» → «05.09»; long — «5 сентября».
function fmtDate(iso, long) {
  const [y, m, d] = iso.split('-').map(Number);
  if (long) return d + ' ' + MONTHS_GEN[m - 1];
  return String(d).padStart(2, '0') + '.' + String(m).padStart(2, '0') + (y !== Number(state.today.slice(0, 4)) ? '.' + y : '');
}

function addDays(iso, n) {
  const [y, m, d] = iso.split('-').map(Number);
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

// ---------- сервер ----------

class APIError extends Error {
  constructor(message, status) { super(message); this.status = status; }
}

async function api(path, opts = {}) {
  const init = { method: opts.method || 'GET', headers: { Authorization: 'tma ' + initData }, cache: 'no-store' };
  if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  let res;
  try {
    res = await fetch(path, init);
  } catch (e) {
    throw new APIError('Нет связи с сервером.', 0);
  }
  let body = null;
  try { body = await res.json(); } catch (e) { /* не JSON */ }
  if (!res.ok) throw new APIError((body && body.error) || 'Ошибка ' + res.status, res.status);
  return body;
}

// waitEntry ждёт окончания фонового разбора записи.
async function waitEntry(id) {
  const deadline = Date.now() + 6 * 60 * 1000;
  for (;;) {
    const st = await api('/api/entries/' + id);
    if (st.state !== 'running') return st;
    if (Date.now() > deadline) throw new APIError('Разбор идёт слишком долго. Загляните позже — запись сохранена.');
    await sleep(POLL_MS);
  }
}

// ---------- диалоги ----------

function alertBox(text) {
  if (tg && tg.showAlert) return new Promise((r) => tg.showAlert(text, r));
  alert(text);
  return Promise.resolve();
}

// confirmBox — «да/нет». Telegram ограничивает текст всплывающего окна 256 символами.
function confirmBox(text) {
  if (text.length > 250) text = text.slice(0, 249) + '…';
  if (tg && tg.showConfirm) return new Promise((r) => tg.showConfirm(text, (ok) => r(!!ok)));
  return Promise.resolve(confirm(text));
}

let toastTimer = null;
function toast(text) {
  const t = $('toast');
  t.textContent = text;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 2500);
}

function haptic(kind) {
  try { tg.HapticFeedback.notificationOccurred(kind); } catch (e) { /* нет поддержки */ }
}

function showStatus(text) {
  const s = $('status');
  s.textContent = text || '';
  s.hidden = !text;
}

// ---------- навигация ----------

const state = { month: null, today: null, minDate: null, canRecord: false, cache: new Map(), day: null, stack: ['calendar'] };
const SCREENS = ['calendar', 'day', 'compose', 'categories', 'catedit', 'catdays'];

function showScreen(name, push = true) {
  if (push && state.stack[state.stack.length - 1] !== name) state.stack.push(name);
  for (const s of SCREENS) $(s).hidden = s !== name;
  if (tg && tg.BackButton) {
    if (name === 'calendar') tg.BackButton.hide(); else tg.BackButton.show();
  }
  window.scrollTo(0, 0);
}

async function goBack() {
  if (state.stack.length <= 1) return;
  if (!$('compose').hidden && composeDirty() && !(await confirmBox('Закрыть форму? Несохранённый текст пропадёт.'))) return;
  if (!$('compose').hidden) resetRecorder();
  state.stack.pop();
  const prev = state.stack[state.stack.length - 1];
  setClosingConfirmation(false);
  if (prev === 'categories') { showScreen('categories', false); await openCategories(false); }
  else if (prev === 'day' && state.day) await openDay(state.day, false);
  else if (prev === 'calendar') { showScreen('calendar', false); await openMonth(state.month, true); }
  else showScreen(prev, false);
}

function backToCalendar() {
  state.stack = ['calendar'];
  showScreen('calendar', false);
  return openMonth(state.month, true);
}

function setClosingConfirmation(on) {
  try { if (on) tg.enableClosingConfirmation(); else tg.disableClosingConfirmation(); } catch (e) { /* нет поддержки */ }
}

// ---------- календарь ----------

function monthKey(y, m) { return y + '-' + String(m + 1).padStart(2, '0'); }
function parseMonth(key) { const [y, m] = key.split('-').map(Number); return [y, m - 1]; }
function shiftMonth(key, delta) {
  const [y, m] = parseMonth(key);
  const d = new Date(Date.UTC(y, m + delta, 1));
  return monthKey(d.getUTCFullYear(), d.getUTCMonth());
}

async function openMonth(key, fresh) {
  if (fresh) state.cache.clear();
  showStatus(state.month ? '' : 'Загрузка…');
  let data = state.cache.get(key);
  try {
    if (!data) {
      data = await api('/api/month' + (key ? '?m=' + key : ''));
      state.cache.set(data.month, data);
    }
  } catch (e) {
    showStatus(e.message);
    return;
  }
  showStatus('');
  state.month = data.month;
  state.today = data.today;
  state.minDate = data.minDate;
  state.canRecord = !!data.canRecord;
  renderMonth(data);
}

function renderMonth(data) {
  const [y, m] = parseMonth(data.month);
  $('month-title').textContent = MONTHS[m] + ' ' + y;
  $('next').disabled = data.month >= data.today.slice(0, 7);
  $('prev').disabled = data.month <= data.minDate.slice(0, 7);

  const days = new Map(data.days.map((d) => [d.date, d]));
  const grid = $('grid');
  grid.replaceChildren();
  const offset = (new Date(Date.UTC(y, m, 1)).getUTCDay() + 6) % 7; // понедельник — первый
  for (let i = 0; i < offset; i++) grid.append(el('span'));
  const total = new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
  for (let d = 1; d <= total; d++) {
    const date = data.month + '-' + String(d).padStart(2, '0');
    const info = days.get(date);
    const cell = el('button', 'cell', String(d));
    if (date === data.today) cell.classList.add('today');
    if (info) {
      cell.classList.add('has');
      let label = d + ': записей ' + info.entries;
      if (info.unconfirmed > 0) {
        cell.append(el('span', 'badge', '⚠'));
        label += ', неподтверждённых ' + info.unconfirmed;
      }
      cell.setAttribute('aria-label', label);
      cell.addEventListener('click', () => openDay(date));
    } else if (date <= data.today && date >= data.minDate) {
      cell.classList.add('empty');
      cell.setAttribute('aria-label', d + ': записей нет');
      cell.addEventListener('click', async () => {
        if (await confirmBox('За ' + fmtDate(date, true) + ' записей нет. Добавить запись на этот день?')) openCompose(date);
      });
    } else {
      cell.disabled = true;
    }
    grid.append(cell);
  }
  renderTodo(data);
}

// renderTodo — то, что ждёт внимания: части записей без даты, неразобранные записи, неподтверждённые.
function renderTodo(data) {
  const box = $('todo');
  box.replaceChildren();
  if (data.pending.length) {
    box.append(el('h2', null, 'Без даты (' + data.pending.length + ')'),
      el('p', 'hint', 'Модель не поняла, к какому дню относится часть записи. Выберите день — она попадёт в его карточку.'));
    for (const p of data.pending) box.append(pendingCard(p, () => openMonth(state.month, true)));
  }
  if (data.unanalyzed.length) {
    box.append(el('h2', null, 'Не разобраны (' + data.unanalyzed.length + ')'),
      el('p', 'hint', 'Текст сохранён, но модель его не разобрала (сбой или перезапуск бота).'));
    for (const u of data.unanalyzed) {
      const card = el('div', 'todo-item', el('div', 'todo-title', u.label), el('div', 'todo-preview', u.preview));
      const actions = el('div', 'actions',
        button('↻ Разобрать', 'primary', (b) => reanalyze(u.id, b, card, () => openMonth(state.month, true))),
        button('🗑 Удалить', 'danger', (b) => deleteVoice({ id: u.id }, b, () => openMonth(state.month, true))));
      card.append(actions);
      box.append(card);
    }
  }
  if (data.unconfirmed > 0) {
    box.append(el('p', 'note', '⚠ Неподтверждённых расшифровок: ' + data.unconfirmed + '. Дни с ними отмечены ⚠ — откройте день и подтвердите или исправьте запись.'));
  }
}

// pendingCard — часть записи без даты с кнопками выбора дня.
function pendingCard(p, onDone) {
  const card = el('div', 'todo-item', el('div', 'todo-title', p.entry), el('div', 'todo-preview', p.preview || '(без текста)'));
  const actions = el('div', 'actions');
  const choose = async (date, b) => {
    b.disabled = true;
    try {
      await api('/api/segments/' + p.id + '/date', { method: 'POST', body: { date } });
      haptic('success');
      card.replaceChildren(el('div', 'todo-title', p.entry), el('div', 'done-line', '✓ Добавлено в карточку ' + fmtDate(date, true)));
      state.cache.clear();
      if (onDone) onDone(date);
    } catch (e) {
      b.disabled = false;
      alertBox(e.message);
    }
  };
  const prev = addDays(p.refDate, -1);
  actions.append(button(fmtDate(p.refDate) + ' — день записи', 'primary', (b) => choose(p.refDate, b)));
  if (prev >= state.minDate) actions.append(button(fmtDate(prev) + ' — накануне', null, (b) => choose(prev, b)));
  const other = el('span', 'other');
  const input = el('input');
  input.type = 'date';
  input.min = state.minDate;
  input.max = state.today;
  input.value = p.refDate;
  other.append(input, button('OK', null, (b) => { if (input.value) choose(input.value, b); }));
  other.hidden = true;
  actions.append(button('Другой день…', null, (b) => { b.hidden = true; other.hidden = false; }), other);
  card.append(actions);
  return card;
}

// ---------- день ----------

async function openDay(date, push = true) {
  let data;
  try {
    data = await api('/api/day?d=' + date);
  } catch (e) {
    if (e.status === 404) {
      // записей за день больше нет (например, последнюю удалили или перенесли)
      if (state.day === date) { state.day = null; toast('За ' + fmtDate(date, true) + ' записей нет'); return backToCalendar(); }
    }
    alertBox(e.message);
    return;
  }
  if (state.day !== date) $('day-rebuild-box').hidden = true; // итог пересборки другого дня здесь не нужен
  state.day = date;
  $('day-title').textContent = data.title;
  renderVoices(data.voices);
  renderCard(data.card);
  showScreen('day', push);
  for (const v of data.voices) if (v.busy) watchBusy(v.id);
}

function reloadDay() { return openDay(state.day, false); }

// watchBusy ждёт окончания разбора записи, начатого раньше, и обновляет день.
async function watchBusy(id) {
  try { await waitEntry(id); } catch (e) { return; }
  if (!$('day').hidden) reloadDay();
}

function renderVoices(voices) {
  $('voices-title').textContent = 'Записи (' + voices.length + ')';
  const list = $('voices');
  list.replaceChildren();
  for (const v of voices) list.append(voiceItem(v));
}

function voiceItem(v) {
  const li = el('li', 'voice');
  li.dataset.voice = v.id;
  const head = el('button', 'voice-head',
    el('span', 'label', v.label),
    !v.confirmed && el('span', 'warn', '⚠ не подтверждена'),
    v.edited && el('span', 'tag', '✎ правилась'),
    v.busy && el('span', 'tag', '⏳ разбирается'),
    el('span', 'chev', '›'));
  head.type = 'button';
  head.setAttribute('aria-expanded', 'false');
  const text = el('div', 'text', v.text);
  const body = el('div', 'voice-body', text);
  const actions = el('div', 'actions');
  if (!v.busy) {
    actions.append(button('✎ Изменить текст', null, () => editVoice(v, li)));
    if (!v.confirmed) actions.append(button('✓ Подтвердить', 'primary', (b) => confirmVoice(v, b)));
    actions.append(
      button('↻ Пересобрать', null, async (b) => {
        if (await confirmBox('Пересобрать запись по текущему тексту? Дни, которые вы уточняли вручную для её частей, нужно будет уточнить снова.')) {
          reanalyze(v.id, b, li, reloadDay);
        }
      }),
      button('🗑 Удалить', 'danger', (b) => deleteVoice(v, b)));
  }
  body.append(actions);
  li.append(head, body);
  // нажатие на заголовок или текст раскрывает и сворачивает запись
  const toggle = () => setOpen(li, !li.classList.contains('open'));
  head.addEventListener('click', toggle);
  text.addEventListener('click', toggle);
  return li;
}

function setOpen(li, open) {
  li.classList.toggle('open', open);
  li.querySelector('.voice-head').setAttribute('aria-expanded', String(open));
}

// focusVoice раскрывает запись, из которой взято значение карточки.
function focusVoice(id) {
  const li = document.querySelector('.voice[data-voice="' + id + '"]');
  if (!li) return;
  setOpen(li, true);
  li.scrollIntoView({ behavior: 'smooth', block: 'center' });
  li.classList.remove('flash');
  void li.offsetWidth; // перезапуск анимации
  li.classList.add('flash');
}

function editVoice(v, li) {
  setOpen(li, true);
  const body = li.querySelector('.voice-body');
  const area = el('textarea', 'edit-area');
  area.value = v.text;
  area.rows = Math.min(16, Math.max(5, Math.ceil(v.text.length / 45)));
  const count = el('span', 'hint');
  const updateCount = () => { count.textContent = area.value.length + ' / ' + MAX_TEXT; };
  area.addEventListener('input', updateCount);
  updateCount();
  const save = button('Сохранить', 'primary', async (b) => {
    const text = area.value.trim();
    if (!text) return alertBox('Текст пустой. Чтобы убрать запись, используйте «Удалить».');
    if (text.length > MAX_TEXT) return alertBox('Слишком длинный текст: больше ' + MAX_TEXT + ' символов.');
    b.disabled = true;
    try {
      const res = await api('/api/entries/' + v.id + '/text', { method: 'PUT', body: { text } });
      state.cache.clear();
      if (!res.changed) { toast('Текст не изменился — запись подтверждена'); return reloadDay(); }
      body.replaceChildren(el('div', 'progress', 'Сохранено. Пересобираю запись…'));
      const st = await waitEntry(v.id);
      if (st.state === 'failed') await alertBox('Текст сохранён, но пересобрать запись не удалось — прежний разбор остался. Попробуйте «Пересобрать» позже.');
      else haptic('success');
      await afterChange(st);
    } catch (e) {
      b.disabled = false;
      alertBox(e.message);
    }
  });
  const cancel = button('Отмена', null, () => reloadDay());
  body.replaceChildren(area, el('div', 'compose-foot', count, el('span', 'actions', cancel, save)),
    el('p', 'hint', 'Прежний текст сохранится в истории. После сохранения запись пересоберётся, правка считается подтверждением.'));
  area.focus();
}

// afterChange — после пересборки: части без даты спросить здесь же, затем обновить день.
async function afterChange(st) {
  if (st.pending && st.pending.length) {
    toast('Есть части без даты — выберите день в календаре');
    return backToCalendar();
  }
  return reloadDay();
}

async function confirmVoice(v, b) {
  b.disabled = true;
  try {
    await api('/api/entries/' + v.id + '/confirm', { method: 'POST' });
    haptic('success');
    toast('Подтверждено');
    state.cache.clear();
    reloadDay();
  } catch (e) {
    b.disabled = false;
    alertBox(e.message);
  }
}

// reanalyze — повторный разбор по текущему тексту (после сбоя модели или изменения категорий).
async function reanalyze(id, b, container, done) {
  b.disabled = true;
  const progress = el('div', 'progress', 'Разбираю запись…');
  container.append(progress);
  try {
    await api('/api/entries/' + id + '/reanalyze', { method: 'POST' });
    const st = await waitEntry(id);
    state.cache.clear();
    if (st.state === 'failed') {
      progress.remove();
      b.disabled = false;
      return alertBox(st.error || 'Не удалось разобрать запись.');
    }
    haptic('success');
    if (st.pending && st.pending.length) toast('Есть части без даты — выберите день в календаре');
    return done();
  } catch (e) {
    progress.remove();
    b.disabled = false;
    alertBox(e.message);
  }
}

async function deleteVoice(v, b, done) {
  let imp;
  try {
    imp = await api('/api/entries/' + v.id + '/impact');
  } catch (e) {
    return alertBox(e.message);
  }
  let msg = 'Удалить ' + imp.name + ' навсегда?';
  if (imp.observations) msg += ' Пропадут её текст' + (imp.edits ? ', история правок' : '') + ' и всё извлечённое из неё (значений: ' + imp.observations + ') в карточках ' + imp.days.join(', ') + '.';
  msg += ' Отменить нельзя.';
  if (!(await confirmBox(msg))) return;
  b.disabled = true;
  try {
    await api('/api/entries/' + v.id, { method: 'DELETE' });
    haptic('success');
    toast('Запись удалена');
    state.cache.clear();
    (done || reloadDay)();
  } catch (e) {
    b.disabled = false;
    alertBox(e.message);
  }
}

// ---------- карточка ----------

function renderCard(card) {
  const root = $('card');
  root.replaceChildren();
  if (card.unconfirmed) {
    root.append(el('div', 'banner', '⚠ Есть неподтверждённые расшифровки — откройте запись в списке и подтвердите или исправьте её.'));
  }
  root.append(el('p', 'hint', 'Нажмите на значение, чтобы открыть запись, из которой оно взято, и исправить её.'));
  for (const s of card.sections) {
    const sec = el('section', 'sec', sectionTitle(s));
    if (!s.mentioned) {
      sec.append(el('p', 'none', NOT_MENTIONED));
    } else if (s.group) {
      for (const it of s.items) sec.append(groupItem(it));
    } else {
      sec.append(...itemBody(s.items[0]));
    }
    root.append(sec);
  }
}

// linkSources — делать ли значения ссылками на записи-источники (в списке категории по дням записей рядом нет).
let linkSources = true;

// sectionTitle — заголовок раздела карточки; у одиночной категории ведёт к её списку по дням.
function sectionTitle(s) {
  const h = el('h3', null, s.title);
  if (!s.group && s.items.length === 1) catLink(h, s.items[0].key, s.items[0].title);
  return h;
}

function catLink(node, key, title) {
  node.classList.add('cat-link');
  node.title = 'Все значения категории по дням';
  node.append(el('span', 'chev-link', ' ›'));
  node.addEventListener('click', () => openCategoryDays(key, title));
  return node;
}

// linked делает элемент карточки ссылкой на запись-источник.
function linked(node, voice) {
  if (!voice || !linkSources) return node;
  node.classList.add('src');
  node.dataset.voice = voice;
  node.addEventListener('click', () => focusVoice(voice));
  return node;
}

function mark(v) { return v.mark ? el('span', 'mark', v.mark) : null; }

function value(tag, cls, v) { return linked(el(tag, cls, v.text, mark(v)), v.voice); }

// itemBody — содержимое одиночной категории.
function itemBody(it) {
  const values = it.values || [];
  switch (it.kind) {
    case 'diary': {
      const blocks = it.blocks || [];
      if (!blocks.length) return [el('p', 'none', NOT_MENTIONED)];
      return blocks.map((b) => linked(el('div', 'block',
        b.header && el('div', 'block-head', '▸ ' + b.header),
        el('p', 'pre', b.texts.join('\n'))), b.voice));
    }
    case 'text':
      if (!values.length) return [el('p', 'none', NOT_MENTIONED)];
      return values.map((v) => value('p', 'pre', v));
    case 'list':
      if (!values.length) return [el('p', 'none', NOT_MENTIONED)];
      return [el('ul', null, ...values.map((v) => value('li', null, v)))];
    default:
      if (!values.length) return [el('p', 'none', NOT_MENTIONED)];
      return [value('p', null, values[0]), earlier(it)];
  }
}

function earlier(it) {
  if (!it.earlier || !it.earlier.length) return null;
  return el('div', 'earlier', 'ранее: ' + it.earlier.map((e) => e.text + (e.mark ? ' (' + e.mark + ')' : '')).join(', '));
}

// groupItem — категория внутри группы: «Название: значение».
function groupItem(it) {
  const values = it.values || [];
  const mentioned = values.length || (it.blocks && it.blocks.length);
  const k = () => catLink(el('span', 'k', it.title), it.key, it.title);
  if (!mentioned) return el('div', 'row', k(), ': ', el('span', 'none', NOT_MENTIONED));
  if (it.kind === 'number' || it.kind === 'bool' || (it.kind === 'text' && values.length === 1)) {
    return linked(el('div', 'row', k(), ': ', values[0].text, mark(values[0]), earlier(it)), values[0].voice);
  }
  // список, дневник, несколько порций текста — блоком под названием
  return el('div', 'row', k(), ': ', ...itemBody(it));
}

// ---------- новая запись ----------

function openCompose(date) {
  const d = date || state.today;
  const input = $('compose-date');
  input.min = state.minDate;
  input.max = state.today;
  input.value = d;
  $('compose-text').value = '';
  resetRecorder();
  $('compose-date').disabled = false;
  $('compose-form').hidden = false;
  $('compose-save').disabled = false;
  $('compose-result').hidden = true;
  $('compose-result').replaceChildren();
  updateComposeCount();
  showScreen('compose');
  $('compose-text').focus();
}

function composeDirty() { return !$('compose-form').hidden && ($('compose-text').value.trim() !== '' || recorderBusy()); }

function updateComposeCount() {
  const n = $('compose-text').value.length;
  const c = $('compose-count');
  c.textContent = n + ' / ' + MAX_TEXT;
  c.classList.toggle('warn', n > MAX_TEXT);
  setClosingConfirmation(composeDirty());
}

async function submitCompose(e) {
  e.preventDefault();
  const date = $('compose-date').value;
  const text = $('compose-text').value.trim();
  if (!date) return alertBox('Выберите день записи.');
  if (date > state.today) return alertBox('Нельзя выбрать день в будущем.');
  if (!text) return alertBox('Запись пустая.');
  if (text.length > MAX_TEXT) return alertBox('Слишком длинная запись: больше ' + MAX_TEXT + ' символов. Разбейте её на несколько.');

  const save = $('compose-save');
  save.disabled = true;
  let created;
  try {
    // запись голосом уже сохранена на сервере: отправляем проверенный текст, а не создаём новую
    const voice = recordedVoice();
    created = voice
      ? await api('/api/entries/' + voice + '/save', { method: 'POST', body: { text } })
      : await api('/api/entries', { method: 'POST', body: { date, text } });
  } catch (err) {
    save.disabled = false;
    return alertBox(err.message);
  }
  setClosingConfirmation(false);
  forgetRecording();
  state.cache.clear();
  $('compose-form').hidden = true;
  const result = $('compose-result');
  result.hidden = false;
  result.replaceChildren(el('div', 'progress', 'Запись сохранена. Разбираю её по дням и категориям — обычно это несколько секунд…'));
  await showComposeResult(created.id, date);
}

async function showComposeResult(id, date) {
  const result = $('compose-result');
  let st;
  try {
    st = await waitEntry(id);
  } catch (e) {
    result.replaceChildren(el('p', 'note', e.message), button('К календарю', null, backToCalendar));
    return;
  }
  state.cache.clear();
  result.replaceChildren();
  if (st.state === 'failed') {
    haptic('error');
    result.append(el('p', 'note', 'Запись сохранена, но разобрать её не удалось. Можно повторить сейчас или позже — она будет в календаре в списке «Не разобраны».'),
      el('div', 'actions',
        button('↻ Повторить разбор', 'primary', (b) => reanalyze(id, b, result, () => showComposeResult(id, date))),
        button('К календарю', null, backToCalendar)));
    return;
  }
  haptic('success');
  result.append(el('h2', null, '✓ Запись добавлена'));
  if (st.days.length) {
    const days = el('div', 'actions');
    for (const d of st.days) days.append(button('Открыть ' + fmtDate(d, true), d === date ? 'primary' : null, () => openDayFresh(d)));
    result.append(el('p', 'hint', 'Попало в карточки дней:'), days);
  }
  if (st.pending.length) {
    result.append(el('h2', null, 'Без даты (' + st.pending.length + ')'),
      el('p', 'hint', 'Модель не поняла, к какому дню относится эта часть записи. Выберите день:'));
    for (const p of st.pending) result.append(pendingCard(p, () => showComposeResult(id, date)));
  }
  result.append(el('div', 'actions', button('Ещё запись', null, () => { state.stack.pop(); openCompose(date); }), button('К календарю', null, backToCalendar)));
}

function openDayFresh(date) {
  state.stack = ['calendar'];
  openMonth(date.slice(0, 7), true);
  return openDay(date);
}

// ---------- запуск ----------

function init() {
  if (tg) {
    document.documentElement.dataset.tg = '1';
    tg.ready();
    tg.expand();
    if (tg.BackButton) tg.BackButton.onClick(goBack);
  }
  $('prev').addEventListener('click', () => openMonth(shiftMonth(state.month, -1)));
  $('next').addEventListener('click', () => openMonth(shiftMonth(state.month, 1)));
  $('new-entry').addEventListener('click', () => openCompose(state.today));
  $('back').addEventListener('click', goBack);
  $('compose-back').addEventListener('click', goBack);
  $('day-add').addEventListener('click', () => openCompose(state.day));
  $('compose-text').addEventListener('input', updateComposeCount);
  $('compose-form').addEventListener('submit', submitCompose);
  $('open-cats').addEventListener('click', () => openCategories(true));
  for (const b of document.querySelectorAll('[data-back]')) b.addEventListener('click', goBack);
  $('day-rebuild').addEventListener('click', () => runRebuild('day', state.day, $('day-rebuild-box'), $('day-rebuild'),
    () => { if (!$('day').hidden) return reloadDay(); }));
  initCategories();
  initRecorder();

  if (!initData) {
    $('calendar').hidden = true;
    showStatus('Откройте календарь через бота в Telegram: кнопка «Календарь» или команда /calendar.');
    return;
  }
  openMonth('');
}

document.addEventListener('DOMContentLoaded', init);
