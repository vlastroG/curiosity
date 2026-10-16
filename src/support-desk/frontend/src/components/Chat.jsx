import { useEffect, useRef, useState } from 'react';
import { api } from '../api.js';
import { blockedNote } from '../format.js';
import Markdown from './Markdown.jsx';

// Chat -- переписка живёт только здесь: на сервер уходит один вопрос, без истории.
export default function Chat({ product, ready, attached, onDetach }) {
  const [items, setItems] = useState([]);
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const end = useRef(null);

  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' });
  }, [items, busy]);

  async function send(e) {
    e.preventDefault();
    const question = text.trim();
    if (!question || busy) return;
    const ticketId = attached?.id || 0;
    setItems((xs) => [...xs, { role: 'user', text: question, ticketId }]);
    setText('');
    setBusy(true);
    try {
      const reply = await api.ask(question, ticketId);
      setItems((xs) => [...xs, { role: 'assistant', ...reply }]);
    } catch (err) {
      setItems((xs) => [...xs, { role: 'error', text: err.message }]);
    } finally {
      setBusy(false);
    }
  }

  function onKey(e) {
    if (e.key === 'Enter' && !e.shiftKey) send(e);
  }

  return (
    <section className="chat">
      <div className="transcript">
        {items.length === 0 && (
          <div className="empty">
            <p>Опишите проблему с {product} — отвечу по документации и тикетам поддержки.</p>
            <p className="muted small">Каждый вопрос обрабатывается отдельно: прошлые сообщения модель не видит.</p>
          </div>
        )}
        {items.map((m, i) => (
          <Message key={i} m={m} />
        ))}
        {busy && <div className="msg assistant waiting">Ищу ответ…</div>}
        <div ref={end} />
      </div>
      <form className="composer" onSubmit={send}>
        {attached && (
          <div className="chip">
            Прикреплён тикет #{attached.id}
            <button type="button" onClick={onDetach} aria-label="Открепить тикет">
              ×
            </button>
          </div>
        )}
        <div className="composer-row">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            placeholder={ready ? 'Например: почему не работает вход?' : 'Индекс строится…'}
            maxLength={2000}
            rows={2}
            disabled={busy}
          />
          <button type="submit" disabled={busy || !text.trim()}>
            Отправить
          </button>
        </div>
      </form>
    </section>
  );
}

function Message({ m }) {
  if (m.role === 'user') {
    return (
      <div className="msg user">
        {m.ticketId ? <div className="muted small">с тикетом #{m.ticketId}</div> : null}
        {m.text}
      </div>
    );
  }
  if (m.role === 'error') return <div className="msg error">{m.text}</div>;
  return (
    <div className={`msg assistant ${m.blocked ? 'blocked' : ''}`}>
      {m.blocked && <div className="note">{blockedNote(m.blocked)}</div>}
      <Markdown text={m.answer} />
      {m.tickets?.length > 0 && <div className="meta">Тикеты: {m.tickets.map((id) => `#${id}`).join(', ')}</div>}
      {m.sources?.length > 0 && (
        <details className="meta">
          <summary>Источники ({m.sources.length})</summary>
          <ul>
            {m.sources.map((s, i) => (
              <li key={i}>
                <code>{s.path}</code> — {s.title}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
