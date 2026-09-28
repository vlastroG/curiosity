import { useState } from 'react';
import { SERVERS, argsLine, duration, serverTitle, toolTitle } from '../tools.js';

// Ход работы: по ходам модели, внутри -- вызовы инструментов.
export default function Timeline({ state }) {
  const { turns, notices } = state;
  if (turns.length === 0 && notices.length === 0) {
    return <div className="card empty">Шаги появятся здесь, как только модель начнёт работу.</div>;
  }
  const nudges = notices.filter((n) => n.kind === 'nudge');
  return (
    <ol className="timeline">
      {turns.map((turn) => (
        <li key={turn.turn} className="turn">
          <div className="turn-head">
            <span className="turn-dot">🧠</span>
            <span>
              <b>Ход {turn.turn}.</b> Модель решила вызвать{' '}
              {plural(turn.calls.length, 'инструмент', 'инструмента', 'инструментов')}
              {turn.calls.length > 1 && <span className="muted"> — параллельно</span>}
              {turn.durationMs ? <span className="muted"> · думала {duration(turn.durationMs)}</span> : null}
            </span>
          </div>
          {turn.text && <p className="turn-text">«{turn.text}»</p>}
          <div className={`calls ${turn.calls.length > 1 ? 'multi' : ''}`}>
            {turn.calls.map((call) => (
              <Call key={call.callId} call={call} />
            ))}
          </div>
          {nudges
            .filter((n) => n.turn === turn.turn + 1)
            .map((n, i) => (
              <div key={i} className="notice info">
                ↩ Модель остановилась раньше времени. Агент напомнил: {n.text}
              </div>
            ))}
        </li>
      ))}
    </ol>
  );
}

function plural(n, one, few, many) {
  const mod10 = n % 10;
  const mod100 = n % 100;
  const word = mod10 === 1 && mod100 !== 11 ? one : mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14) ? few : many;
  return `${n} ${word}`;
}

function Call({ call }) {
  const [open, setOpen] = useState(false);
  const server = SERVERS[call.server];
  const statusIcon = { planned: '…', running: '⏳', ok: '✔', error: '✘' }[call.status] || '';
  return (
    <div className={`call server-${call.server} status-${call.status}`}>
      <div className="call-head">
        <span className="call-server">
          {server?.icon} {serverTitle(call.server)}
        </span>
        <span className="call-status">{statusIcon}</span>
      </div>
      <div className="call-title">{toolTitle(call.tool)}</div>
      <div className="call-tool">
        <code>
          {call.server}__{call.tool}
        </code>{' '}
        {argsLine(call.tool, call.args)}
      </div>
      {call.note && <div className="call-note">⚙ {call.note}</div>}
      {call.summary && <div className={`call-summary ${call.status === 'error' ? 'bad' : ''}`}>{call.summary}</div>}
      {call.injection && (
        <div className="notice warn">
          ⚠ В ответе сервера найден текст, похожий на инструкцию для модели. Он обработан как обычные данные:
          <q>{call.injection}</q>
        </div>
      )}
      <div className="call-foot">
        {call.durationMs ? <span className="muted">{duration(call.durationMs)}</span> : <span />}
        <button type="button" className="link" onClick={() => setOpen(!open)}>
          {open ? 'Скрыть данные' : 'Данные'}
        </button>
      </div>
      {open && (
        <div className="raw">
          <div className="raw-label">Аргументы</div>
          <pre>{JSON.stringify(call.args, null, 2)}</pre>
          {call.result && (
            <>
              <div className="raw-label">Ответ сервера</div>
              <pre>{typeof call.result === 'string' ? call.result : JSON.stringify(call.result, null, 2)}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}
