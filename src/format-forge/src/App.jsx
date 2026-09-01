import { useState } from 'react';
import { complete } from './api.js';
import { FORMATS, buildSystemPrompt, needsJsonMode } from './constraints.js';
import { validateOutput } from './validate.js';

const IDLE = { status: 'idle' };
const LOADING = { status: 'loading' };

function parseStopSequences(raw) {
  return raw
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .slice(0, 16);
}

function toPanelState(settled, format) {
  if (settled.status === 'rejected') {
    return { status: 'error', error: settled.reason?.message ?? String(settled.reason) };
  }
  const { text, finishReason, usage, latencyMs } = settled.value;
  return {
    status: 'done',
    text,
    finishReason,
    usage,
    latencyMs,
    validation: validateOutput(format, text),
  };
}

function Panel({ title, subtitle, state }) {
  return (
    <section className="panel">
      <div className="panel__head">
        <h2>{title}</h2>
        <p className="panel__subtitle">{subtitle}</p>
      </div>

      {state.status === 'done' && (
        <div className="panel__meta">
          <span>{state.latencyMs} мс</span>
          <span>{state.usage?.completion_tokens ?? '?'} tok</span>
          <span>finish: {state.finishReason}</span>
        </div>
      )}

      {state.status === 'done' && state.validation && (
        <div className={state.validation.ok ? 'badge badge--ok' : 'badge badge--fail'}>
          {state.validation.ok ? '✓ ' : '✗ '}
          {state.validation.message}
        </div>
      )}

      <pre className="panel__out">
        {state.status === 'idle' && <span className="muted">Ответ появится здесь</span>}
        {state.status === 'loading' && (
          <span className="muted">
            Запрос
            <span className="cursor" />
          </span>
        )}
        {state.status === 'error' && <span className="panel__err">{state.error}</span>}
        {state.status === 'done' && state.text}
      </pre>
    </section>
  );
}

export default function App() {
  const [prompt, setPrompt] = useState('');
  const [format, setFormat] = useState('json');
  const [temperature, setTemperature] = useState(0);
  const [maxTokens, setMaxTokens] = useState(300);
  const [maxWords, setMaxWords] = useState('');
  const [stopText, setStopText] = useState('');
  const [left, setLeft] = useState(IDLE);
  const [right, setRight] = useState(IDLE);
  const [isBusy, setIsBusy] = useState(false);

  const stopSequences = parseStopSequences(stopText);

  const constraintsSummary = [
    FORMATS[format].label,
    `temp ${temperature}`,
    `${maxTokens} tok`,
    maxWords ? `≤ ${maxWords} слов` : null,
    stopSequences.length > 0 ? `stop: ${stopSequences.length}` : null,
  ]
    .filter(Boolean)
    .join(' · ');

  async function handleSend() {
    const trimmed = prompt.trim();
    if (!trimmed || isBusy) return;

    const system = buildSystemPrompt({
      format,
      maxWords: maxWords === '' ? undefined : Number(maxWords),
    });

    setIsBusy(true);
    setLeft(LOADING);
    setRight(LOADING);

    const [rawResult, constrainedResult] = await Promise.allSettled([
      complete({ prompt: trimmed }),
      complete({
        prompt: trimmed,
        system,
        temperature,
        maxTokens,
        stop: stopSequences,
        jsonMode: needsJsonMode(format),
      }),
    ]);

    setLeft(toPanelState(rawResult, format));
    setRight(toPanelState(constrainedResult, format));
    setIsBusy(false);
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
        <h1>Format Forge</h1>
        <span className="app__header-note">неделя 1 · день 2 — формат ответа</span>
      </header>

      <main className="app__panels">
        <Panel title="Без ограничений" subtitle="только запрос пользователя" state={left} />
        <Panel title="С ограничениями" subtitle={constraintsSummary} state={right} />
      </main>

      <section className="controls">
        <label className="control">
          <span className="control__label">Формат ответа</span>
          <select
            value={format}
            onChange={(event) => setFormat(event.target.value)}
            disabled={isBusy}
          >
            {Object.entries(FORMATS).map(([key, meta]) => (
              <option key={key} value={key}>
                {meta.label}
              </option>
            ))}
          </select>
        </label>

        <label className="control">
          <span className="control__label">
            temperature <b>{temperature}</b>
          </span>
          <input
            type="range"
            min="0"
            max="2"
            step="0.1"
            value={temperature}
            onChange={(event) => setTemperature(Number(event.target.value))}
            disabled={isBusy}
          />
        </label>

        <label className="control">
          <span className="control__label">
            max_tokens <b>{maxTokens}</b>
          </span>
          <input
            type="range"
            min="16"
            max="2048"
            step="16"
            value={maxTokens}
            onChange={(event) => setMaxTokens(Number(event.target.value))}
            disabled={isBusy}
          />
        </label>

        <label className="control">
          <span className="control__label">Макс. слов (в промпте)</span>
          <input
            type="number"
            min="1"
            placeholder="без лимита"
            value={maxWords}
            onChange={(event) => setMaxWords(event.target.value)}
            disabled={isBusy}
          />
        </label>

        <label className="control control--wide">
          <span className="control__label">Стоп-последовательности (по одной на строку)</span>
          <textarea
            rows={2}
            placeholder="##"
            value={stopText}
            onChange={(event) => setStopText(event.target.value)}
            disabled={isBusy}
          />
        </label>
      </section>

      <footer className="app__input">
        <textarea
          rows={2}
          placeholder="Запрос к модели. Enter — отправить"
          value={prompt}
          onChange={(event) => setPrompt(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={isBusy}
        />
        <button onClick={handleSend} disabled={isBusy || !prompt.trim()}>
          {isBusy ? 'Отправка...' : 'Отправить'}
        </button>
      </footer>
    </div>
  );
}
