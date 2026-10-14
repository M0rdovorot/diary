'use strict';

// Запись голоса прямо в приложении: микрофон → сервер (распознавание) → расшифровка в текстовом поле,
// которую можно сразу поправить и сохранить. Звук на сервере не хранится — только текст.

const MAX_REC_SEC = 60 * 60;
const MIME_CANDIDATES = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus'];

const rec = {
  recorder: null, stream: null, chunks: [], startedAt: 0, timer: null,
  blob: null, seconds: 0, voice: 0, // voice — id записи, уже сохранённой на сервере
  busy: false, // идёт запись звука или распознавание
  gen: 0, // номер попытки: отбрасывает ответы устаревших попыток после сброса
};

function recorderBusy() { return rec.busy; }
function recordedVoice() { return rec.voice; }
function forgetRecording() { rec.voice = 0; }

function recSupported() {
  return !!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia && window.MediaRecorder);
}

// resetRecorder — сбросить всё (при открытии формы и при выходе из неё).
function resetRecorder() {
  rec.gen++;
  stopTracks();
  clearInterval(rec.timer);
  if (rec.recorder && rec.recorder.state !== 'inactive') {
    rec.recorder.ondataavailable = null;
    rec.recorder.onstop = null;
    try { rec.recorder.stop(); } catch (e) { /* уже остановлена */ }
  }
  if ($('rec-audio').src) URL.revokeObjectURL($('rec-audio').src);
  // расшифровка, уже сохранённая на сервере, но так и не отправленная, больше не нужна
  if (rec.voice) api('/api/entries/' + rec.voice, { method: 'DELETE' }).catch(() => {});
  Object.assign(rec, { recorder: null, chunks: [], blob: null, seconds: 0, voice: 0, busy: false });
  renderRecorder('idle');
}

function stopTracks() {
  if (rec.stream) rec.stream.getTracks().forEach((t) => t.stop());
  rec.stream = null;
}

// renderRecorder показывает нужные элементы: idle | recording | working | done | error.
function renderRecorder(mode, text) {
  $('rec').hidden = !(state.canRecord && recSupported());
  const show = (id, on) => { $(id).hidden = !on; };
  show('rec-start', mode === 'idle' || mode === 'error' || mode === 'done');
  $('rec-start').textContent = mode === 'done' ? '🎙 Записать заново' : (mode === 'error' ? '🎙 Записать ещё раз' : '🎙 Записать голосом');
  show('rec-live', mode === 'recording');
  show('rec-stop', mode === 'recording');
  show('rec-cancel', mode === 'recording');
  show('rec-progress', mode === 'working');
  if (mode === 'working') $('rec-progress').textContent = text || '';
  show('rec-retry', mode === 'error' && !!rec.blob);
  show('rec-error', mode === 'error');
  if (mode === 'error') $('rec-error').textContent = text || '';
  show('rec-audio', mode === 'done' || (mode === 'error' && !!rec.blob));
  show('rec-hint', mode === 'done');
  $('compose-date').disabled = mode === 'done' || mode === 'working' || mode === 'recording';
}

function fmtClock(sec) { return Math.floor(sec / 60) + ':' + String(sec % 60).padStart(2, '0'); }

async function startRecording() {
  if (rec.busy) return;
  if ($('compose-text').value.trim() && !rec.voice &&
      !(await confirmBox('В поле уже есть текст. Расшифровка записи заменит его. Продолжить?'))) return;
  const gen = ++rec.gen;
  // прежняя запись, уже сохранённая на сервере, но не отправленная, больше не нужна
  if (rec.voice) {
    api('/api/entries/' + rec.voice, { method: 'DELETE' }).catch(() => {});
    rec.voice = 0;
  }
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
  } catch (e) {
    renderRecorder('error', 'Нет доступа к микрофону. Разрешите его для Telegram в настройках телефона — или отправьте голосовое прямо боту в чат.');
    return;
  }
  if (gen !== rec.gen) { stream.getTracks().forEach((t) => t.stop()); return; }

  const mime = MIME_CANDIDATES.find((m) => MediaRecorder.isTypeSupported(m)) || '';
  let recorder;
  try {
    recorder = new MediaRecorder(stream, Object.assign({ audioBitsPerSecond: 32000 }, mime ? { mimeType: mime } : {}));
  } catch (e) {
    stream.getTracks().forEach((t) => t.stop());
    renderRecorder('error', 'Этот телефон не умеет записывать звук в приложении. Отправьте голосовое прямо боту в чат.');
    return;
  }
  rec.stream = stream;
  rec.recorder = recorder;
  rec.chunks = [];
  rec.busy = true;
  recorder.ondataavailable = (ev) => { if (ev.data && ev.data.size) rec.chunks.push(ev.data); };
  recorder.onstop = () => finishRecording(gen, recorder.mimeType || mime || 'audio/webm');
  recorder.start(1000);
  rec.startedAt = Date.now();
  $('rec-time').textContent = '0:00';
  rec.timer = setInterval(() => {
    const sec = Math.floor((Date.now() - rec.startedAt) / 1000);
    $('rec-time').textContent = fmtClock(sec);
    if (sec >= MAX_REC_SEC) stopRecording();
  }, 500);
  renderRecorder('recording');
  setClosingConfirmation(true);
}

function stopRecording() {
  clearInterval(rec.timer);
  if (rec.recorder && rec.recorder.state !== 'inactive') rec.recorder.stop();
}

function cancelRecording() {
  resetRecorder();
  updateComposeCount();
}

function finishRecording(gen, mime) {
  if (gen !== rec.gen) return;
  stopTracks();
  rec.seconds = Math.max(1, Math.round((Date.now() - rec.startedAt) / 1000));
  rec.blob = new Blob(rec.chunks, { type: mime });
  rec.chunks = [];
  if (rec.blob.size < 1000) {
    rec.busy = false;
    rec.blob = null;
    return renderRecorder('error', 'Запись получилась пустой. Попробуйте ещё раз.');
  }
  const audio = $('rec-audio');
  audio.src = URL.createObjectURL(rec.blob);
  return uploadRecording(gen);
}

// uploadRecording отправляет звук на сервер и ждёт расшифровку.
async function uploadRecording(gen) {
  rec.busy = true;
  renderRecorder('working', 'Отправляю запись…');
  let task;
  try {
    const q = '?date=' + encodeURIComponent($('compose-date').value) + '&duration=' + rec.seconds;
    const res = await fetch('/api/recordings' + q, {
      method: 'POST', cache: 'no-store',
      headers: { Authorization: 'tma ' + initData, 'Content-Type': rec.blob.type || 'application/octet-stream' },
      body: rec.blob,
    });
    const body = await res.json().catch(() => null);
    if (!res.ok) throw new Error((body && body.error) || 'Ошибка ' + res.status);
    task = body.id;
  } catch (e) {
    if (gen !== rec.gen) return;
    rec.busy = false;
    return renderRecorder('error', (e instanceof TypeError ? 'Нет связи с сервером. ' : '') + (e.message || '') + ' Запись сохранена на телефоне — отправьте её ещё раз или запишите заново.');
  }

  renderRecorder('working', 'Распознаю речь… Обычно до минуты, длинные записи — дольше.');
  const deadline = Date.now() + 35 * 60 * 1000;
  for (;;) {
    await sleep(2500);
    if (gen !== rec.gen) return;
    let st;
    try {
      st = await api('/api/recordings/' + task);
    } catch (e) {
      if (e.status === 404 || Date.now() > deadline) {
        rec.busy = false;
        return renderRecorder('error', e.message);
      }
      continue; // кратковременный сбой связи
    }
    if (st.state === 'running') continue;
    rec.busy = false;
    if (st.state === 'failed') return renderRecorder('error', st.error || 'Не удалось распознать речь.');
    rec.voice = st.voice;
    $('compose-text').value = st.text;
    updateComposeCount();
    haptic('success');
    renderRecorder('done');
    $('compose-text').focus();
    return;
  }
}

function initRecorder() {
  $('rec-start').addEventListener('click', startRecording);
  $('rec-stop').addEventListener('click', stopRecording);
  $('rec-cancel').addEventListener('click', cancelRecording);
  $('rec-retry').addEventListener('click', () => { if (!rec.busy && rec.blob) uploadRecording(rec.gen); });
}
