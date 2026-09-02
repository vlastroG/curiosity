import { useState } from 'react';
import { MAX_TOKENS } from './api.js';
import { Markdown, splitVerdict } from './markdown.jsx';
import { MODES, MODE_KEYS, runMode } from './modes.js';

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

  const { verdict, body } = splitVerdict(text);
  return (
    <>
      {verdict && (
        <div className="verdict">
          <span className="verdict__label">Ответ</span>
          <span className="verdict__value">{verdict}</span>
        </div>
      )}
      <Markdown text={body} />
    </>
  );
}

function Stages({ stages }) {
  if (!stages || stages.length === 0) return null;
  return (
    <div className="stages">
      {stages.map((stage, index) => (
        <details key={index}>
          <summary className={stage.failed ? 'stages__fail' : undefined}>{stage.title}</summary>
          <div className="stages__body">
            {stage.failed ? (
              <div className="answer--empty">{stage.text}</div>
            ) : (
              <Answer text={stage.text} finishReason={stage.finishReason} />
            )}
          </div>
        </details>
      ))}
    </div>
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
            {stage || 'думаю'}
            <span className="cursor" />
          </span>
        )}
        {state.status === 'error' && <span className="panel__err">{state.error}</span>}
        {state.status === 'done' && (
          <>
            <Stages stages={state.stages} />
            <Answer text={state.text} finishReason={state.finishReason} />
          </>
        )}
      </div>
    </section>
  );
}

export default function App() {
  const [input, setInput] = useState('');
  const [isBusy, setIsBusy] = useState(false);
  const [compare, setCompare] = useState(null);
  const [stages, setStages] = useState({});

  async function handleCompare(question) {
    const payload = [{ role: 'user', content: question }];
    setInput('');
    setStages({});
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
            onStage: (text) => setStages((prev) => ({ ...prev, [key]: text })),
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
    handleCompare(question);
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
      </header>

      {/* поле ввода очищается при отправке, поэтому вопрос показываем здесь */}
      <div className="question">
        <span className="question__label">Вопрос</span>
        {compare === null ? (
          <p className="muted">
            Задайте задачу — она уйдёт во все четыре режима сразу, и ответы встанут рядом.
          </p>
        ) : (
          <p>{compare.question}</p>
        )}
      </div>

      <main className="compare">
        {MODE_KEYS.map((key) => (
          <CompareCard
            key={key}
            modeKey={key}
            state={compare === null ? IDLE : compare.results[key]}
            stage={stages[key]}
          />
        ))}
      </main>

      <footer className="app__input">
        <textarea
          rows={2}
          placeholder="Задача, которую прогоним через все четыре режима. Enter — отправить, Shift+Enter — перенос строки"
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={isBusy}
        />
        <div className="app__input-side">
          <button onClick={handleSend} disabled={isBusy || !input.trim()}>
            {isBusy ? 'Работаю…' : 'Сравнить все 4'}
          </button>
          <span className="app__input-note">8 запросов к API</span>
        </div>
      </footer>
    </div>
  );
}
