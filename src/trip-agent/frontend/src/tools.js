// Как показывать серверы и инструменты человеку.

export const SERVERS = {
  places: { title: 'Места', hint: 'город, достопримечательности', icon: '📍' },
  weather: { title: 'Погода', hint: 'прогноз и климат', icon: '⛅' },
  money: { title: 'Деньги', hint: 'валюта, курс, бюджет', icon: '💱' },
  trip: { title: 'План', hint: 'сборка и публикация', icon: '🧭' },
};

export const SERVER_ORDER = ['places', 'weather', 'money', 'trip'];

const TOOLS = {
  find_city: 'Ищу город',
  sights: 'Ищу, что посмотреть рядом',
  sight_info: 'Читаю о месте',
  trip_weather: 'Узнаю погоду на даты поездки',
  find_place: 'Ищу место',
  get_forecast: 'Смотрю прогноз',
  country_currency: 'Узнаю валюту страны',
  convert: 'Перевожу бюджет по курсу ЦБ',
  budget_split: 'Раскладываю бюджет по дням',
  trip_create: 'Завожу план',
  trip_set_budget: 'Записываю бюджет в план',
  trip_add_day: 'Добавляю день в план',
  trip_publish: 'Публикую план',
  trip_get: 'Читаю план',
};

export function toolTitle(tool) {
  return TOOLS[tool] || tool;
}

export function serverTitle(server) {
  return SERVERS[server]?.title || server || '—';
}

// argsLine -- главное из аргументов одной строкой, без технических полей.
export function argsLine(tool, args) {
  if (!args || typeof args !== 'object') return '';
  const a = args;
  switch (tool) {
    case 'find_city':
      return a.name ? `«${a.name}»` : '';
    case 'sights':
    case 'trip_weather':
      return [coords(a), dates(a)].filter(Boolean).join(' · ');
    case 'sight_info':
      return a.page_id ? `статья ${a.page_id}${a.lang && a.lang !== 'ru' ? ` (${a.lang})` : ''}` : '';
    case 'country_currency':
      return a.country_code || '';
    case 'convert':
      return a.amount ? `${formatNumber(a.amount)} ${a.from} → ${a.to}` : '';
    case 'budget_split':
      return a.total ? `${formatNumber(a.total)} ${a.currency} · ${a.days} дн. · ${a.travelers} чел.` : '';
    case 'trip_create':
      return [a.city, dates(a), a.travelers ? `${a.travelers} чел.` : ''].filter(Boolean).join(' · ');
    case 'trip_add_day':
      return [a.date, Array.isArray(a.activities) ? `пунктов: ${a.activities.length}` : ''].filter(Boolean).join(' · ');
    case 'trip_set_budget':
      return a.local_total ? `${formatNumber(a.local_total)} ${a.local_currency}` : a.total ? `${formatNumber(a.total)} ${a.currency}` : '';
    default:
      return '';
  }
}

function coords(a) {
  if (typeof a.latitude !== 'number' || typeof a.longitude !== 'number') return '';
  return `${a.latitude.toFixed(4)}, ${a.longitude.toFixed(4)}`;
}

function dates(a) {
  return a.start_date && a.end_date ? `${a.start_date} — ${a.end_date}` : '';
}

export function formatNumber(v) {
  if (typeof v !== 'number' || Number.isNaN(v)) return String(v ?? '');
  return Math.round(v).toLocaleString('ru-RU').replace(/ /g, ' ');
}

export function formatDate(iso) {
  if (!iso) return '';
  const d = new Date(`${iso}T00:00:00`);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'long' });
}

export function formatDay(iso) {
  const d = new Date(`${iso}T00:00:00`);
  if (Number.isNaN(d.getTime())) return iso;
  const text = d.toLocaleDateString('ru-RU', { weekday: 'long', day: 'numeric', month: 'long' });
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function temp(v) {
  if (typeof v !== 'number') return '';
  const r = Math.round(v);
  return `${r > 0 ? '+' : ''}${r}°`;
}

export function duration(ms) {
  if (!ms && ms !== 0) return '';
  return ms < 1000 ? `${ms} мс` : `${(ms / 1000).toFixed(1)} с`;
}

export const STATUS = {
  queued: { title: 'в очереди', tone: 'muted' },
  running: { title: 'собирается', tone: 'active' },
  done: { title: 'готов', tone: 'ok' },
  done_with_errors: { title: 'готов с замечаниями', tone: 'warn' },
  failed: { title: 'ошибка', tone: 'bad' },
  refused: { title: 'отказ', tone: 'bad' },
};

// reduceEvents -- состояние ленты из событий: ходы модели, вызовы, текущее действие.
export function reduceEvents(events) {
  const turns = [];
  const calls = {};
  const counts = {};
  let current = null;
  let servers = null;
  let checks = null;
  let finished = null;
  const notices = [];

  for (const e of events) {
    switch (e.type) {
      case 'queued':
        current = { kind: 'queued', text: e.text };
        break;
      case 'started':
        current = { kind: 'started' };
        break;
      case 'servers':
        servers = e.servers;
        break;
      case 'thinking':
        current = { kind: 'thinking', turn: e.turn };
        break;
      case 'decided': {
        const turn = { turn: e.turn, text: e.text, durationMs: e.durationMs, calls: [] };
        for (const c of e.calls || []) {
          const call = { ...c, status: 'planned' };
          calls[c.callId] = call;
          turn.calls.push(call);
        }
        turns.push(turn);
        break;
      }
      case 'tool_started': {
        const call = calls[e.callId] || { callId: e.callId };
        Object.assign(call, { server: e.server, tool: e.tool, args: e.args, status: 'running', note: e.text });
        calls[e.callId] = call;
        current = { kind: 'tool', call };
        break;
      }
      case 'tool_finished': {
        const call = calls[e.callId] || { callId: e.callId };
        Object.assign(call, {
          server: e.server,
          tool: e.tool,
          status: e.ok ? 'ok' : 'error',
          summary: e.summary,
          result: e.result,
          durationMs: e.durationMs,
        });
        calls[e.callId] = call;
        counts[e.server] = (counts[e.server] || 0) + 1;
        current = { kind: 'tool', call };
        break;
      }
      case 'injection': {
        const call = calls[e.callId];
        if (call) call.injection = e.quote;
        notices.push({ kind: 'injection', server: e.server, tool: e.tool, quote: e.quote, turn: e.turn });
        break;
      }
      case 'nudge':
        notices.push({ kind: 'nudge', text: e.text, turn: e.turn });
        break;
      case 'answer':
        current = { kind: 'answer', text: e.text };
        break;
      case 'verify':
        checks = e.checks;
        current = { kind: 'verify' };
        break;
      case 'finished':
        finished = { status: e.status, error: e.error };
        current = { kind: 'finished', status: e.status, error: e.error };
        break;
      default:
    }
  }
  const active = current?.kind === 'tool' && current.call.status === 'running' ? current.call.server : null;
  return { turns, counts, current, servers, checks, finished, notices, active };
}
