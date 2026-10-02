'use strict';

// Mini App дневника: календарь месяца → карточка дня. Только чтение.
// Данные вставляются только через textContent (никакого innerHTML с данными).

const tg = window.Telegram && window.Telegram.WebApp;
const initData = tg ? tg.initData : '';

const MONTHS = ['Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь', 'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь'];
const NOT_MENTIONED = 'не упоминалось';

const $ = (id) => document.getElementById(id);

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

class APIError extends Error {}

async function api(path) {
  let res;
  try {
    res = await fetch(path, { headers: { Authorization: 'tma ' + initData }, cache: 'no-store' });
  } catch (e) {
    throw new APIError('Нет связи с сервером.');
  }
  let body = null;
  try { body = await res.json(); } catch (e) { /* не JSON */ }
  if (!res.ok) throw new APIError((body && body.error) || 'Ошибка ' + res.status);
  return body;
}

function showStatus(text) {
  const s = $('status');
  s.textContent = text || '';
  s.hidden = !text;
}

// ---------- календарь ----------

const state = { month: null, today: null, cache: new Map() };

function monthKey(y, m) { return y + '-' + String(m + 1).padStart(2, '0'); }
function parseMonth(key) { const [y, m] = key.split('-').map(Number); return [y, m - 1]; }
function shiftMonth(key, delta) {
  const [y, m] = parseMonth(key);
  const d = new Date(Date.UTC(y, m + delta, 1));
  return monthKey(d.getUTCFullYear(), d.getUTCMonth());
}

async function loadMonth(key) {
  if (state.cache.has(key)) return state.cache.get(key);
  const data = await api('/api/month' + (key ? '?m=' + key : ''));
  state.cache.set(data.month, data);
  return data;
}

async function openMonth(key) {
  showStatus(state.month ? '' : 'Загрузка…');
  let data;
  try {
    data = await loadMonth(key);
  } catch (e) {
    showStatus(e.message);
    return;
  }
  showStatus('');
  state.month = data.month;
  state.today = data.today;
  renderMonth(data);
}

function renderMonth(data) {
  const [y, m] = parseMonth(data.month);
  $('month-title').textContent = MONTHS[m] + ' ' + y;
  $('next').disabled = data.month >= data.today.slice(0, 7);

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
    } else {
      cell.disabled = true;
    }
    grid.append(cell);
  }

  const notes = [];
  if (data.pending > 0) notes.push('записей без даты: ' + data.pending);
  if (data.unconfirmed > 0) notes.push('неподтверждённых расшифровок: ' + data.unconfirmed);
  const att = $('attention');
  att.hidden = notes.length === 0;
  att.textContent = notes.length ? 'Ждут внимания — ' + notes.join(', ') + '. Разобрать: /pending в чате с ботом.' : '';
}

// ---------- день ----------

async function openDay(date) {
  let data;
  try {
    data = await api('/api/day?d=' + date);
  } catch (e) {
    if (tg && tg.showAlert) tg.showAlert(e.message); else alert(e.message);
    return;
  }
  $('day-title').textContent = data.title;
  renderVoices(data.voices);
  renderCard(data.card);
  showScreen('day');
  window.scrollTo(0, 0);
}

function renderVoices(voices) {
  $('voices-title').textContent = 'Записи (' + voices.length + ')';
  const list = $('voices');
  list.replaceChildren();
  for (const v of voices) {
    const label = el('span', 'label', v.label,
      !v.confirmed && el('span', 'warn', '⚠ не подтверждена'),
      v.edited && el('span', null, '✎'),
      el('span', 'chev', '›'));
    const item = el('button', 'voice', label, el('div', 'text', v.text));
    item.setAttribute('aria-expanded', 'false');
    item.addEventListener('click', () => {
      const open = item.classList.toggle('open');
      item.setAttribute('aria-expanded', String(open));
    });
    list.append(el('li', null, item));
  }
}

function renderCard(card) {
  const root = $('card');
  root.replaceChildren();
  if (card.unconfirmed) {
    root.append(el('div', 'banner', '⚠ Есть неподтверждённые расшифровки — проверьте их через /pending в чате с ботом.'));
  }
  for (const s of card.sections) {
    const sec = el('section', 'sec', el('h3', null, s.title));
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

function mark(v) { return v.mark ? el('span', 'mark', v.mark) : null; }

function valueNodes(v) { return [v.text, mark(v)]; }

// itemBody — содержимое одиночной категории.
function itemBody(it) {
  const values = it.values || [];
  switch (it.kind) {
    case 'diary': {
      const blocks = it.blocks || [];
      if (!blocks.length) return [el('p', 'none', NOT_MENTIONED)];
      const out = [];
      for (const b of blocks) {
        if (b.header) out.push(el('div', 'block-head', '▸ ' + b.header));
        out.push(el('p', 'pre', b.texts.join('\n')));
      }
      return out;
    }
    case 'text':
      if (!values.length) return [el('p', 'none', NOT_MENTIONED)];
      return values.map((v) => el('p', 'pre', ...valueNodes(v)));
    case 'list':
      if (!values.length) return [el('p', 'none', NOT_MENTIONED)];
      return [el('ul', null, ...values.map((v) => el('li', null, ...valueNodes(v))))];
    default:
      if (!values.length) return [el('p', 'none', NOT_MENTIONED)];
      return [el('p', null, ...valueNodes(values[0])), earlier(it)];
  }
}

function earlier(it) {
  if (!it.earlier || !it.earlier.length) return null;
  return el('div', 'earlier', 'ранее: ' + it.earlier.map((e) => e.text + (e.mark ? ' (' + e.mark + ')' : '')).join(', '));
}

// groupItem — категория внутри группы: «Название: значение».
function groupItem(it) {
  const values = it.values || [];
  const row = el('div', 'row', el('span', 'k', it.title + ': '));
  const mentioned = values.length || (it.blocks && it.blocks.length);
  if (!mentioned) {
    row.append(el('span', 'none', NOT_MENTIONED));
    return row;
  }
  if (it.kind === 'number' || it.kind === 'bool' || (it.kind === 'text' && values.length === 1)) {
    row.append(...valueNodes(values[0]));
    const e = earlier(it);
    if (e) row.append(e);
    return row;
  }
  row.append(...itemBody(it)); // список, дневник, несколько порций текста — блоком под названием
  return row;
}

// ---------- навигация ----------

function showScreen(name) {
  $('calendar').hidden = name !== 'calendar';
  $('day').hidden = name !== 'day';
  if (tg && tg.BackButton) {
    if (name === 'day') tg.BackButton.show(); else tg.BackButton.hide();
  }
}

function backToCalendar() { showScreen('calendar'); }

function init() {
  if (tg) {
    document.documentElement.dataset.tg = '1';
    tg.ready();
    tg.expand();
    if (tg.BackButton) tg.BackButton.onClick(backToCalendar);
  }
  $('prev').addEventListener('click', () => openMonth(shiftMonth(state.month, -1)));
  $('next').addEventListener('click', () => openMonth(shiftMonth(state.month, 1)));
  $('back').addEventListener('click', backToCalendar);

  if (!initData) {
    $('calendar').hidden = true;
    showStatus('Откройте календарь через бота в Telegram: кнопка «Календарь» или команда /calendar.');
    return;
  }
  openMonth('');
}

init();
