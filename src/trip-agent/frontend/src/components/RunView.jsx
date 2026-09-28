import { useEffect, useMemo, useRef, useState } from 'react';
import { getRun, subscribe } from '../api.js';
import { SERVERS, SERVER_ORDER, STATUS, argsLine, formatDate, reduceEvents, serverTitle, toolTitle } from '../tools.js';
import Timeline from './Timeline.jsx';
import PlanView from './PlanView.jsx';

// Прогон: что происходит сейчас, ход работы по шагам, проверки и готовый план.
export default function RunView({ id, onChange }) {
  const [run, setRun] = useState(null);
  const [events, setEvents] = useState([]);
  const [error, setError] = useState('');
  const lastSeq = useRef(0);

  useEffect(() => {
    let stop = () => {};
    let alive = true;
    getRun(id)
      .then((loaded) => {
        if (!alive) return;
        setRun(loaded);
        setEvents(loaded.events || []);
        lastSeq.current = loaded.events?.length ? loaded.events[loaded.events.length - 1].seq : 0;
        if (loaded.status === 'queued' || loaded.status === 'running') {
          stop = subscribe(
            id,
            lastSeq.current,
            (event) => {
              if (event.seq <= lastSeq.current) return;
              lastSeq.current = event.seq;
              setEvents((list) => [...list, event]);
              if (event.type === 'started' || event.type === 'finished') onChange?.();
            },
            () => getRun(id).then((fresh) => alive && setRun(fresh))
          );
        }
      })
      .catch((err) => setError(err.message));
    return () => {
      alive = false;
      stop();
    };
  }, [id]);

  const state = useMemo(() => reduceEvents(events), [events]);

  if (error) return <div className="card empty">Не удалось открыть план: {error}</div>;
  if (!run) return <div className="card empty">Загружаю…</div>;

  const status = STATUS[state.finished?.status || run.status] || STATUS.running;
  const req = run.request || {};
  const plan = run.plan || null;

  return (
    <div className="run">
      <header className="run-head">
        <div>
          <h1>{req.city}</h1>
          <div className="run-meta">
            {formatDate(req.startDate)} — {formatDate(req.endDate)} · {req.travelers} чел.
            {req.hasBudget ? ` · бюджет ${Math.round(req.budget).toLocaleString('ru-RU')} ${req.currency}` : ''}
            {' · '}
            <span className={`pill tone-${status.tone}`}>{status.title}</span>
          </div>
        </div>
        {run.file && (
          <a className="btn" href={`/plans/${run.file}`} target="_blank" rel="noreferrer">
            Открыть HTML ↗
          </a>
        )}
      </header>

      <ServerBar counts={state.counts} active={state.active} servers={state.servers} />
      <NowCard state={state} run={run} />

      {plan && <PlanView plan={plan} answer={run.answer} />}

      {state.checks && <Checks checks={state.checks} />}

      <section>
        <h2 className="section-title">Ход работы</h2>
        <Timeline state={state} />
      </section>
    </div>
  );
}

function ServerBar({ counts, active, servers }) {
  const available = Object.fromEntries((servers || []).map((s) => [s.name, s]));
  return (
    <div className="servers">
      {SERVER_ORDER.map((name) => {
        const s = SERVERS[name];
        const info = available[name];
        return (
          <div key={name} className={`server server-${name} ${active === name ? 'active' : ''} ${info && !info.available ? 'down' : ''}`}>
            <span className="server-icon">{s.icon}</span>
            <span className="server-text">
              <b>{s.title}</b>
              <small>{info && !info.available ? 'недоступен' : s.hint}</small>
            </span>
            <span className="server-count">{counts[name] || 0}</span>
          </div>
        );
      })}
    </div>
  );
}

// NowCard -- что происходит прямо сейчас, крупно.
function NowCard({ state, run }) {
  const c = state.current;
  let icon = '⏳';
  let title = 'Готовлюсь…';
  let detail = '';
  let live = true;
  let tone = '';

  if (!c) {
    live = run.status === 'queued' || run.status === 'running';
  } else if (c.kind === 'queued') {
    title = 'Жду своей очереди';
    detail = 'Планы собираются по одному.';
  } else if (c.kind === 'started') {
    title = 'Проверяю, какие серверы на связи';
  } else if (c.kind === 'thinking') {
    icon = '🧠';
    title = 'Модель выбирает следующий шаг…';
    detail = `Ход ${c.turn}. Она видит результаты прошлых шагов и решает, какой инструмент какого сервера вызвать.`;
  } else if (c.kind === 'tool') {
    const call = c.call;
    icon = SERVERS[call.server]?.icon || '🔧';
    tone = `server-${call.server}`;
    const running = call.status === 'running';
    title = `${serverTitle(call.server)}: ${toolTitle(call.tool)}${running ? '…' : ''}`;
    detail = running ? argsLine(call.tool, call.args) : call.summary || '';
    if (!running && call.status === 'error') detail = `Не получилось: ${call.summary}`;
  } else if (c.kind === 'answer') {
    icon = '✍️';
    title = 'Модель закончила';
    detail = c.text;
  } else if (c.kind === 'verify') {
    icon = '🔎';
    title = 'Проверяю, как прошёл флоу';
  } else if (c.kind === 'finished') {
    live = false;
    const ok = c.status === 'done';
    icon = ok ? '✅' : c.status === 'done_with_errors' ? '⚠️' : '⛔';
    title = ok ? 'План готов' : STATUS[c.status]?.title || c.status;
    detail = c.error || run.answer || '';
  }

  return (
    <div className={`now card ${tone} ${live ? 'live' : ''}`}>
      <span className="now-icon">{icon}</span>
      <div>
        <div className="now-title">{title}</div>
        {detail && <div className="now-detail">{detail}</div>}
      </div>
      {live && <span className="spinner" aria-hidden="true" />}
    </div>
  );
}

function Checks({ checks }) {
  const passed = checks.filter((c) => c.ok).length;
  return (
    <section className="card checks">
      <h2 className="section-title">
        Проверка флоу <span className="muted">{passed} из {checks.length}</span>
      </h2>
      <ul>
        {checks.map((c) => (
          <li key={c.name} className={c.ok ? 'ok' : 'bad'}>
            <span className="check-mark">{c.ok ? '✔' : '✘'}</span>
            <span>
              <b>{c.name}</b>
              <span className="muted"> — {c.detail}</span>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
