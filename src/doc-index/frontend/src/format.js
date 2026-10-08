// Чистые функции интерфейса: маршруты, форматирование, раскраска глав
// и разметка текста главы под подсветку чанков. Покрыты тестами (format.test.js).

// ---------- маршруты (#/chat/3, #/search?q=…, #/read/tom/3?c=…) ----------

export function parseRoute(hash) {
  const raw = (hash || '').replace(/^#/, '') || '/chat';
  const [path, qs = ''] = raw.split('?');
  const params = Object.fromEntries(new URLSearchParams(qs));
  const parts = path.split('/').filter(Boolean);
  const view = parts[0] || 'chat';
  if (view === 'read') {
    return { view, book: parts[1] || '', section: parts[2] === undefined ? null : Number(parts[2]), params };
  }
  if (view === 'chat') {
    const id = Number(parts[1]);
    return { view, id: Number.isInteger(id) && id > 0 ? id : null, params };
  }
  if (['search', 'index'].includes(view)) {
    return { view, params };
  }
  return { view: 'chat', id: null, params: {} };
}

export function routeHash(view, params = {}, ...parts) {
  const clean = Object.fromEntries(Object.entries(params).filter(([, v]) => v !== undefined && v !== null && v !== ''));
  const qs = new URLSearchParams(clean).toString();
  const path = ['', view, ...parts].join('/');
  return '#' + path + (qs ? '?' + qs : '');
}

export function readHash(book, section, chunkId, variant) {
  return routeHash('read', { c: chunkId, v: variant }, book, section);
}

// ---------- числа и слова ----------

export function plural(n, one, few, many) {
  const m10 = n % 10;
  const m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return one;
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
  return many;
}

export const pct = (x) => (x === undefined || x === null ? '—' : `${Math.round(x * 100)}%`);

export function fmtNum(n) {
  if (n === undefined || n === null) return '—';
  return Math.round(n).toLocaleString('ru-RU');
}

export function fmtSeconds(s) {
  if (!s && s !== 0) return '—';
  if (s < 1) return `${Math.round(s * 1000)} мс`;
  if (s < 60) return `${s.toFixed(1)} с`;
  const m = Math.floor(s / 60);
  return `${m} мин ${Math.round(s - m * 60)} с`;
}

export function fmtMs(ms) {
  if (ms === undefined || ms === null) return '—';
  return ms < 10 ? `${ms.toFixed(1)} мс` : `${Math.round(ms)} мс`;
}

export function fmtMB(bytes) {
  return `${Math.round(bytes / (1 << 20)).toLocaleString('ru-RU')} МБ`;
}

// ---------- главы ----------

// Глава кратко: «гл. II», «предисловие».
export function shortSection(s) {
  if (!s) return '';
  if (/^Chapter /.test(s.label)) return 'гл. ' + s.label.replace(/^Chapter /, '').replace(/^the Last$/, 'последняя');
  return { Preface: 'предисловие', Conclusion: 'заключение', Notice: 'уведомление', Explanatory: 'пояснение' }[s.label] ||
    s.label;
}

// Диапазон глав чанка: «гл. II» или «гл. I → II».
export function sectionsLabel(sections) {
  if (!sections || sections.length === 0) return '';
  const first = shortSection(sections[0]);
  if (sections.length === 1) return first;
  const last = shortSection(sections[sections.length - 1]).replace(/^гл\. /, '');
  return `${first} → ${last}`;
}

// Цвет главы: одна и та же глава одной книги -- один цвет во всех колонках.
export function chapterHue(book, n) {
  let h = 0;
  const key = `${book}:${n}`;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
  return (h * 47) % 360;
}

// ---------- разметка главы под подсветку ----------

// segments режет абзац [start, end) на куски по границам отметок.
// marks: [{start, end, kind, idx}] в символах от начала книги.
// Каждый кусок: {text, start, end, marks: [...отметки, которые его покрывают]}.
export function segments(para, marks) {
  const cuts = new Set([para.start, para.end]);
  for (const m of marks) {
    if (m.end <= para.start || m.start >= para.end) continue;
    cuts.add(Math.max(m.start, para.start));
    cuts.add(Math.min(m.end, para.end));
  }
  const points = [...cuts].sort((a, b) => a - b);
  const out = [];
  for (let i = 0; i + 1 < points.length; i++) {
    const a = points[i];
    const b = points[i + 1];
    if (a === b) continue;
    out.push({
      text: para.text.slice(a - para.start, b - para.start),
      start: a,
      end: b,
      marks: marks.filter((m) => m.start <= a && m.end >= b),
    });
  }
  return out;
}

// hitMarks -- отметки найденного чанка: перекрытие с предыдущим бледнее.
export function hitMarks(hit) {
  if (!hit) return [];
  const marks = [];
  if (hit.start < hit.bodyStart) marks.push({ start: hit.start, end: hit.bodyStart, kind: 'overlap' });
  marks.push({ start: hit.bodyStart, end: hit.end, kind: 'hit' });
  return marks;
}

// cutMarks -- нарезка варианта: чередующийся фон и номер чанка.
export function cutMarks(chunks) {
  return (chunks || []).map((c, i) => ({ start: c.bodyStart, end: c.end, kind: 'cut', idx: c.rank, parity: i % 2 }));
}

// Где в главе начинается каждый чанк нарезки: туда ставится метка «#N».
export function cutStarts(chunks) {
  return new Map((chunks || []).map((c) => [c.bodyStart, c.rank]));
}

// ---------- сравнение ----------

// best -- лучшее значение метрики среди вариантов (для выделения).
export function best(values, higherIsBetter = true) {
  const nums = values.filter((v) => typeof v === 'number' && !Number.isNaN(v));
  if (nums.length < 2) return undefined;
  return higherIsBetter ? Math.max(...nums) : Math.min(...nums);
}

export function placeLabel(rank) {
  if (!rank) return 'не найдена в первой десятке';
  return `нужная глава на ${rank}-м месте`;
}

// placeTone -- цвет ячейки «где нашлась нужная глава».
export function placeTone(rank) {
  if (rank === 1) return 'good';
  if (rank && rank <= 5) return 'fair';
  return 'bad';
}

// Выбор до трёх вариантов: четвёртый вытесняет самый давний.
export function toggleVariant(selected, id, limit = 3) {
  if (selected.includes(id)) {
    return selected.length > 1 ? selected.filter((v) => v !== id) : selected;
  }
  const next = [...selected, id];
  return next.length > limit ? next.slice(next.length - limit) : next;
}

// Примеры вопросов: поровну из каждой книги, в порядке файла.
export function pickExamples(questions, n = 8) {
  const byBook = new Map();
  for (const q of questions.filter((x) => x.valid)) {
    if (!byBook.has(q.book)) byBook.set(q.book, []);
    byBook.get(q.book).push(q);
  }
  const lists = [...byBook.values()];
  const out = [];
  for (let i = 0; out.length < n && lists.some((l) => i < l.length); i++) {
    for (const l of lists) {
      if (i < l.length && out.length < n) out.push(l[i]);
    }
  }
  return out;
}

// ---------- RAG ----------

// splitCitations режет ответ на текст и ссылки на отрывки: «[1]», «[1, 3]», «[2][4]».
export function splitCitations(text) {
  const out = [];
  const re = /\[(\d+(?:\s*[,;]\s*\d+)*)\]/g;
  let last = 0;
  let m;
  while ((m = re.exec(text || '')) !== null) {
    if (m.index > last) out.push({ text: text.slice(last, m.index) });
    for (const n of m[1].split(/[,;]/)) out.push({ cite: Number(n.trim()) });
    last = m.index + m[0].length;
  }
  if (last < (text || '').length) out.push({ text: text.slice(last) });
  return out;
}

// ---------- воронка второго этапа ----------

export const STAGE = {
  kept: { text: 'в ответе', tone: 'good' },
  top: { text: 'не вошёл в top-K', tone: 'fair' },
  rel: { text: 'отсёк реранкер', tone: 'bad' },
  sim: { text: 'ниже порога косинуса', tone: 'muted' },
};

// funnelLine -- «20 кандидатов → 14 прошли порог косинуса → 5 одобрил реранкер → 3 в ответе».
export function funnelLine(f) {
  if (!f) return '';
  const parts = [`${f.total} ${plural(f.total, 'кандидат', 'кандидата', 'кандидатов')}`];
  if (f.passedSim !== f.total) parts.push(`${f.passedSim} прошли порог косинуса`);
  parts.push(`${f.passedRel} ${plural(f.passedRel, 'одобрил', 'одобрил', 'одобрил')} реранкер`);
  parts.push(`${f.kept} в ответе`);
  return parts.join(' → ');
}

// clampSettings -- числа в допустимых диапазонах, как проверяет сервер.
export function clampSettings(s) {
  const c = (v, lo, hi, d) => {
    const n = Number(v);
    return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : d;
  };
  return {
    query: ['hyde', 'en', 'raw'].includes(s.query) ? s.query : 'hyde',
    kBefore: Math.round(c(s.kBefore, 1, 50, 20)),
    simMin: c(s.simMin, 0, 1, 0),
    relMin: c(s.relMin, 0, 1, 0.5),
    kAfter: Math.round(c(s.kAfter, 1, 10, 5)),
    order: ['rerank', 'cosine', 'fused'].includes(s.order) ? s.order : 'cosine',
    compressAfter: Math.round(c(s.compressAfter, 4, 50, 12)),
    ...clampGen(s),
  };
}

// clampGen -- параметры генерации; пусто -- значение модели (null или 0).
export function clampGen(s) {
  const blank = (v) => v === undefined || v === null || v === '' || !Number.isFinite(Number(v));
  const opt = (v, lo, hi) => (blank(v) || Number(v) === 0 ? 0 : Math.round(Math.min(hi, Math.max(lo, Number(v)))));
  return {
    temperature: blank(s.temperature) ? null : Math.min(2, Math.max(0, Number(s.temperature))),
    maxTokens: opt(s.maxTokens, 16, 100000),
    ctx: opt(s.ctx, 2048, 262144),
  };
}

// ---------- чат ----------

// fmtWhen -- время сообщения или чата: «только что», «5 мин назад», «14:30», «12 сен».
export function fmtWhen(iso, now = new Date()) {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '';
  const min = Math.floor((now - t) / 60000);
  if (min < 1) return 'только что';
  if (min < 60) return `${min} мин назад`;
  if (t.toDateString() === now.toDateString()) {
    return t.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  }
  const opts = { day: 'numeric', month: 'short' };
  if (t.getFullYear() !== now.getFullYear()) opts.year = 'numeric';
  return t.toLocaleDateString('ru-RU', opts).replace('.', '');
}

// memoryLine -- память задачи одной строкой для свёрнутой карточки.
export function memoryLine(chat) {
  const s = chat?.state || {};
  const parts = [];
  const n = (k, one, few, many) => `${k} ${plural(k, one, few, many)}`;
  if (s.theses?.length) parts.push(n(s.theses.length, 'тезис', 'тезиса', 'тезисов'));
  if (s.open?.length) parts.push(n(s.open.length, 'открытый вопрос', 'открытых вопроса', 'открытых вопросов'));
  if (chat?.summarizedUpto) parts.push(`${n(chat.summarizedUpto, 'сообщение', 'сообщения', 'сообщений')} в сводке`);
  return parts.join(' · ');
}

// citedNumbers -- номера отрывков, на которые сослался ответ.
export function citedNumbers(text) {
  return new Set(splitCitations(text).filter((s) => s.cite).map((s) => s.cite));
}

// settingsLine -- настройки чата кратко, для свёрнутой панели.
export function settingsLine(s) {
  if (!s) return '';
  const q = { hyde: 'HyDE', en: 'перевод', raw: 'как есть' }[s.query] || s.query;
  const gen = [];
  if (s.temperature !== undefined && s.temperature !== null) gen.push(`t=${s.temperature}`);
  if (s.maxTokens > 0) gen.push(`≤${s.maxTokens} ток.`);
  if (s.ctx > 0) gen.push(`ctx ${s.ctx}`);
  return (
    `поиск: ${q}, top-${s.kBefore} → ${s.kAfter}, косинус ≥ ${s.simMin}, реранкер ≥ ${s.relMin} · сжатие после ${s.compressAfter} сообщений` +
    (gen.length ? ` · ${gen.join(', ')}` : '')
  );
}

// isFresh -- пункт памяти, появившийся, после реплики seq (подсветка «новое»).
export function isFresh(item, seq) {
  return !!seq && item.by === 'model' && item.since === seq;
}
