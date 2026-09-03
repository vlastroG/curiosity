import { useState } from 'react';
import { MAX_TOKENS } from './api.js';
import { Markdown } from './markdown.jsx';
import { DEFAULT_TEMPERATURES, runAt } from './temperatures.js';

const IDLE = { status: 'idle' };

function plural(count, one, few, many) {
  const mod10 = count % 10;
  const mod100 = count % 100;
  if (mod10 === 1 && mod100 !== 11) return one;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
  return many;
}

function Meta({ latencyMs, usage, calls, finishReason }) {
  return (
    <div className="meta">
      <span>{(latencyMs / 1000).toFixed(1)} с</span>
      <span>{usage?.total_tokens ?? '?'} tok</span>
      <span>
        {calls} {plural(calls, 'вызов', 'вызова', 'вызовов')}
      </span>
      {finishReason && finishReason !== 'stop' && (
        <span className="meta__warn">finish: {finishReason}</span>
      )}
    </div>
  );
}

/**
 * Модель рассуждающая, и max_tokens тратится сначала на внутреннее рассуждение.
 * Если лимит выбран целиком, приходит finish_reason "length" и пустой content --
 * без этого пояснения панель выглядела бы просто пустой.
 */
function Answer({ text, finishReason }) {
  if (!text || !text.trim()) {
    return (
      <div className="answer--empty">
        {finishReason === 'length'
          ? `Модель не вернула текст: весь бюджет max_tokens (${MAX_TOKENS}) ушёл на внутреннее рассуждение. Поднимите MAX_TOKENS в src/api.js или упростите вопрос.`
          : `Модель вернула пустой ответ (finish_reason: ${finishReason ?? 'неизвестно'}).`}
      </div>
    );
  }

  return <Markdown text={text} />;
}

function TemperatureCard({ index, temperature, onTemperatureChange, state, disabled }) {
  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Карточка {index + 1}</h2>
        <label className="temp-field">
          <span>temperature =</span>
          <input
            className="temp-input"
            type="number"
            step="0.1"
            value={temperature}
            onChange={(event) => onTemperatureChange(event.target.value)}
            disabled={disabled}
          />
        </label>
      </div>

      {state.status === 'done' && (
        <Meta
          latencyMs={state.latencyMs}
          usage={state.usage}
          calls={state.calls}
          finishReason={state.finishReason}
        />
      )}

      <div className="panel__body">
        {state.status === 'idle' && <span className="muted">Ответ появится здесь</span>}
        {state.status === 'loading' && (
          <span className="muted">
            думаю
            <span className="cursor" />
          </span>
        )}
        {state.status === 'error' && <span className="panel__err">{state.error}</span>}
        {state.status === 'done' && <Answer text={state.text} finishReason={state.finishReason} />}
      </div>
    </section>
  );
}

export default function App() {
  const [input, setInput] = useState('');
  const [temperatures, setTemperatures] = useState(DEFAULT_TEMPERATURES);
  const [isBusy, setIsBusy] = useState(false);
  const [question, setQuestion] = useState(null);
  const [results, setResults] = useState([IDLE, IDLE, IDLE]);

  const parsedTemperatures = temperatures.map((value) => parseFloat(value));
  const hasInvalidTemperature = parsedTemperatures.some((value) => Number.isNaN(value));

  function handleTemperatureChange(index, value) {
    setTemperatures((prev) => prev.map((current, i) => (i === index ? value : current)));
  }

  async function handleCompare() {
    const text = input.trim();
    if (!text || hasInvalidTemperature || isBusy) return;

    const payload = [{ role: 'user', content: text }];
    setInput('');
    setQuestion(text);
    setResults(parsedTemperatures.map(() => ({ status: 'loading' })));
    setIsBusy(true);

    await Promise.allSettled(
      parsedTemperatures.map(async (temperature, index) => {
        const patch = (state) =>
          setResults((prev) => prev.map((current, i) => (i === index ? state : current)));
        try {
          const result = await runAt(temperature, { messages: payload });
          patch({ status: 'done', ...result });
        } catch (caught) {
          patch({ status: 'error', error: caught.message });
        }
      })
    );

    setIsBusy(false);
  }

  function handleKeyDown(event) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      handleCompare();
    }
  }

  return (
    <div className="app">
      <header className="app__header">
        <h1>Heat Forge</h1>
        <span className="app__header-note">неделя 1 · день 4 — влияние temperature</span>
      </header>

      {/* поле ввода очищается при отправке, поэтому вопрос показываем здесь */}
      <div className="question">
        <span className="question__label">Вопрос</span>
        {question === null ? (
          <p className="muted">
            Задайте запрос — он уйдёт с тремя значениями temperature сразу, и ответы встанут
            рядом. Температуру каждой карточки можно поменять перед отправкой.
          </p>
        ) : (
          <p>{question}</p>
        )}
      </div>

      <main className="compare">
        {temperatures.map((temperature, index) => (
          <TemperatureCard
            key={index}
            index={index}
            temperature={temperature}
            onTemperatureChange={(value) => handleTemperatureChange(index, value)}
            state={results[index]}
            disabled={isBusy}
          />
        ))}
      </main>

      <footer className="app__input">
        <textarea
          rows={2}
          placeholder="Запрос, который прогоним с тремя значениями temperature. Enter — отправить, Shift+Enter — перенос строки"
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={isBusy}
        />
        <div className="app__input-side">
          <button
            onClick={handleCompare}
            disabled={isBusy || !input.trim() || hasInvalidTemperature}
          >
            {isBusy ? 'Работаю…' : 'Сравнить 3 температуры'}
          </button>
          <span className="app__input-note">3 запроса к API</span>
        </div>
      </footer>
    </div>
  );
}
