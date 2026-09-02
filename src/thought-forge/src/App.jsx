import { useEffect, useRef, useState } from 'react';
import { MODES, MODE_KEYS, runMode } from './modes.js';

const IDLE = { status: 'idle' };

function plural(count, one, few, many) {
  const mod10 = count % 10;
  const mod100 = count % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}

function Meta({ latencyMs, usage, calls }) {
  return (
    <div className="meta">
      <span>{latencyMs} мс</span>
      <span>{usage?.total_tokens ?? '?'} tok</span>
      <span>
        {calls} {plural(calls, 'вызов', 'вызова', 'вызовов')}
      </span>
    </div>
  );
}

function Stages({ stages }) {
  if (!stages || stages.length === 0) return null;
  return (
    <div className="stages">
      {stages.map((stage, index) => (
        <details key={index}>
          <summary className={stage.failed ? 'stages__fail' : undefined}>{stage.title}</summary>
          <pre>{stage.text}</pre>
        </details>
      ))}
    </div>
  );
}

function Pending({ stage }) {
  return (
    <span className="muted">
      {stage || 'думаю'}
      <span className="cursor" />
    </span>
  );
}

function CompareCard({ modeKey, state, stage }) {
  const meta = MODES[modeKey];
  return (
    <section className="panel">
      <div className="panel__head">
        <h2>{meta.label}</h2>
        <p className="panel__subtitle">{meta.hint}</p>
      </div>

      {state.status === 'done' && (
        <Meta latencyMs={state.latencyMs} usage={state.usage} calls={state.calls} />
      )}

      <div className="panel__body">
        {state.status === 'idle' && <span className="muted">Ответ появится здесь</span>}
        {state.status === 'loading' && <Pending stage={stage} />}
        {state.status === 'error' && <span className="panel__err">{state.error}</span>}
        {state.status === 'done' && (
          <>
            <Stages stages={state.stages} />
            <div className="answer">{state.text}</div>
          </>
        )}
      </div>
    </section>
  );
}

export default function App() {
  const [view, setView] = useState('chat');
  const [mode, setMode] = useState('direct');
  const [messages, setMessages] = useState([]);
  const [input, setInput] = useState('');
  const [isBusy, setIsBusy] = useState(false);
  const [stage, setStage] = useState('');
  const [error, setError] = useState('');
  const [compare, setCompare] = useState(null);
  const [compareStages, setCompareStages] = useState({});
  const bottomRef = useRef(null);

  useEffect(() => {
    if (view === 'chat') bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, isBusy, view]);

  async function handleChatSend(question) {
    const history = [...messages, { role: 'user', content: question }];
    setMessages(history);
    setInput('');
    setError('');
    setStage('');
    setIsBusy(true);

    try {
      const result = await runMode(mode, {
        messages: history.map(({ role, content }) => ({ role, content })),
        onStage: setStage,
      });
      setMessages((prev) => [
        ...prev,
        {
          role: 'assistant',
          content: result.text,
          mode,
          stages: result.stages,
          latencyMs: result.latencyMs,
          usage: result.usage,
          calls: result.calls,
        },
      ]);
    } catch (caught) {
      setError(caught.message);
    } finally {
      setStage('');
      setIsBusy(false);
    }
  }

  async function handleCompare(question) {
    const payload = [{ role: 'user', content: question }];
    setInput('');
    setError('');
    setCompareStages({});
    setCompare({
      question,
      results: Object.fromEntries(MODE_KEYS.map((key) => [key, { status: 'loading' }])),
    });
    setIsBusy(true);

    await Promise.allSettled(
      MODE_KEYS.map(async (key) => {
        const patch = (state) =>
          setCompare((prev) => ({ ...prev, results: { ...prev.results, [key]: state } }));
        try {
          const result = await runMode(key, {
            messages: payload,
            onStage: (text) => setCompareStages((prev) => ({ ...prev, [key]: text })),
          });
          patch({ status: 'done', ...result });
        } catch (caught) {
          patch({ status: 'error', error: caught.message });
        }
      })
    );

    setIsBusy(false);
  }

  function handleSend() {
    const question = input.trim();
    if (!question || isBusy) return;
    if (view === 'chat') handleChatSend(question);
    else handleCompare(question);
  }

  function handleKeyDown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      handleSend();
    }
  }

  return (
    <div className="app">
      <header className="app__header">
        <h1>Thought Forge</h1>
        <span className="app__header-note">неделя 1 · день 3 — способы рассуждения</span>
        <div className="tabs">
          <button
            className={view === 'chat' ? 'tab tab--active' : 'tab'}
            onClick={() => setView('chat')}
          >
            Чат
          </button>
          <button
            className={view === 'compare' ? 'tab tab--active' : 'tab'}
            onClick={() => setView('compare')}
          >
            Сравнение
          </button>
        </div>
      </header>

      {view === 'chat' && (
        <>
          <nav className="modes">
            {MODE_KEYS.map((key) => (
              <button
                key={key}
                className={key === mode ? 'mode-btn mode-btn--active' : 'mode-btn'}
                onClick={() => setMode(key)}
                disabled={isBusy}
              >
                <b>{MODES[key].label}</b>
                <span>{MODES[key].hint}</span>
              </button>
            ))}
          </nav>

          <main className="app__chat">
            {messages.length === 0 && !isBusy && (
              <div className="app__empty">
                Задайте одну и ту же задачу в разных режимах — или перейдите на вкладку
                «Сравнение», чтобы прогнать её через все четыре сразу.
              </div>
            )}

            {messages.map((message, index) =>
              message.role === 'user' ? (
                <div key={index} className="bubble bubble--user">
                  {message.content}
                </div>
              ) : (
                <div key={index} className="bubble bubble--assistant">
                  <div className="bubble__head">
                    <span className="badge">{MODES[message.mode].label}</span>
                    <Meta
                      latencyMs={message.latencyMs}
                      usage={message.usage}
                      calls={message.calls}
                    />
                  </div>
                  <Stages stages={message.stages} />
                  <div className="answer">{message.content}</div>
                </div>
              )
            )}

            {isBusy && (
              <div className="bubble bubble--assistant">
                <div className="bubble__head">
                  <span className="badge">{MODES[mode].label}</span>
                </div>
                <Pending stage={stage} />
              </div>
            )}

            <div ref={bottomRef} />
          </main>
        </>
      )}

      {view === 'compare' && (
        <main className="compare">
          {MODE_KEYS.map((key) => (
            <CompareCard
              key={key}
              modeKey={key}
              state={compare === null ? IDLE : compare.results[key]}
              stage={compareStages[key]}
            />
          ))}
        </main>
      )}

      {error && <div className="app__error">{error}</div>}

      <footer className="app__input">
        <textarea
          rows={2}
          placeholder={
            view === 'chat'
              ? 'Задача для модели. Enter — отправить, Shift+Enter — перенос строки'
              : 'Задача, которую прогоним через все четыре режима'
          }
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={isBusy}
        />
        <div className="app__input-side">
          <button onClick={handleSend} disabled={isBusy || !input.trim()}>
            {isBusy ? 'Работаю…' : view === 'chat' ? 'Отправить' : 'Сравнить все 4'}
          </button>
          {view === 'chat' ? (
            <button
              className="btn--ghost"
              onClick={() => setMessages([])}
              disabled={isBusy || messages.length === 0}
            >
              Очистить
            </button>
          ) : (
            <span className="app__input-note">8 запросов к API</span>
          )}
        </div>
      </footer>
    </div>
  );
}
