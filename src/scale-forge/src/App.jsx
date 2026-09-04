import { useEffect, useRef, useState } from 'react';
import { complete } from './api.js';
import { Markdown } from './markdown.jsx';
import { MODELS, costOf } from './models.js';

const EMPTY_CHATS = Object.fromEntries(MODELS.map((model) => [model.id, []]));

function formatCost(value) {
  if (value === 0) return '$0';
  if (value < 0.000001) return '<$0.000001';
  return `$${value.toFixed(6)}`;
}

function formatSeconds(ms) {
  return `${(ms / 1000).toFixed(1)} с`;
}

/** Сводка по колонке: сколько всего потрачено этой моделью за диалог. */
function totalsOf(model, messages) {
  const answers = messages.filter((message) => message.role === 'assistant' && message.meta);
  if (answers.length === 0) return null;

  return {
    answers: answers.length,
    tokens: answers.reduce((sum, m) => sum + (m.meta.usage?.total_tokens ?? 0), 0),
    cost: answers.reduce((sum, m) => sum + costOf(model, m.meta.usage), 0),
    avgLatency: answers.reduce((sum, m) => sum + m.meta.latencyMs, 0) / answers.length,
  };
}

function MessageMeta({ model, meta }) {
  const usage = meta.usage ?? {};
  return (
    <div className="meta">
      <span>{formatSeconds(meta.latencyMs)}</span>
      <span>
        {usage.prompt_tokens ?? '?'} → {usage.completion_tokens ?? '?'} tok
      </span>
      {meta.reasoningTokens ? <span>из них {meta.reasoningTokens} на рассуждение</span> : null}
      <span className="meta__cost">{formatCost(costOf(model, usage))}</span>
      {meta.finishReason && meta.finishReason !== 'stop' && (
        <span className="meta__warn">finish: {meta.finishReason}</span>
      )}
    </div>
  );
}

function Chat({ model, messages, isPending }) {
  const bottomRef = useRef(null);
  const totals = totalsOf(model, messages);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [messages, isPending]);

  return (
    <section className="panel">
      <div className="panel__head">
        <div className="panel__title">
          <span className={`tier tier--${model.id}`}>{model.tier}</span>
          <h2>{model.title}</h2>
        </div>
        <p className="panel__subtitle">{model.subtitle}</p>
      </div>

      {totals ? (
        <div className="totals">
          <span>Σ {totals.tokens} tok</span>
          <span className="meta__cost">Σ {formatCost(totals.cost)}</span>
          <span>~{formatSeconds(totals.avgLatency)} на ответ</span>
        </div>
      ) : (
        <div className="totals totals--empty">
          {model.priceIn === 0 ? 'бесплатный тариф' : `$${model.priceIn} / $${model.priceOut} за 1M`}
        </div>
      )}

      <div className="panel__body">
        {messages.length === 0 && !isPending && (
          <p className="muted">Задайте вопрос — он уйдёт во все три модели сразу.</p>
        )}

        {messages.map((message, index) =>
          message.role === 'user' ? (
            <div key={index} className="bubble bubble--user">
              {message.content}
            </div>
          ) : (
            <div key={index} className="bubble bubble--assistant">
              {message.error ? (
                <div className="bubble__error">{message.error}</div>
              ) : (
                <>
                  <MessageMeta model={model} meta={message.meta} />
                  {message.content?.trim() ? (
                    <Markdown text={message.content} />
                  ) : (
                    <div className="bubble__error">
                      Модель вернула пустой ответ
                      {message.meta?.finishReason === 'length'
                        ? ': весь бюджет max_tokens ушёл на внутреннее рассуждение.'
                        : '.'}
                    </div>
                  )}
                </>
              )}
            </div>
          )
        )}

        {isPending && (
          <div className="bubble bubble--assistant">
            <span className="muted">
              думаю
              <span className="cursor" />
            </span>
          </div>
        )}

        <div ref={bottomRef} />
      </div>
    </section>
  );
}

export default function App() {
  const [input, setInput] = useState('');
  const [isBusy, setIsBusy] = useState(false);
  const [chats, setChats] = useState(EMPTY_CHATS);
  const [pending, setPending] = useState({});

  const hasHistory = MODELS.some((model) => chats[model.id].length > 0);

  async function handleSend() {
    const text = input.trim();
    if (!text || isBusy) return;

    setInput('');
    setIsBusy(true);
    setPending(Object.fromEntries(MODELS.map((model) => [model.id, true])));

    // каждая модель ведёт свою историю: вопросы общие, ответы у всех разные
    const histories = Object.fromEntries(
      MODELS.map((model) => [model.id, [...chats[model.id], { role: 'user', content: text }]])
    );
    setChats(histories);

    await Promise.allSettled(
      MODELS.map(async (model) => {
        const append = (message) =>
          setChats((prev) => ({ ...prev, [model.id]: [...prev[model.id], message] }));
        try {
          // сообщения с ошибкой остаются в ленте, но в модель их не отправляем
          const payload = histories[model.id].filter((message) => !message.error);
          const result = await complete({ model, messages: payload });
          append({
            role: 'assistant',
            content: result.text,
            meta: {
              latencyMs: result.latencyMs,
              usage: result.usage,
              reasoningTokens: result.reasoningTokens,
              finishReason: result.finishReason,
            },
          });
        } catch (caught) {
          append({ role: 'assistant', error: caught.message });
        } finally {
          setPending((prev) => ({ ...prev, [model.id]: false }));
        }
      })
    );

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
        <h1>Scale Forge</h1>
        <span className="app__header-note">неделя 1 · день 5 — версии моделей</span>
      </header>

      <main className="chats">
        {MODELS.map((model) => (
          <Chat
            key={model.id}
            model={model}
            messages={chats[model.id]}
            isPending={Boolean(pending[model.id])}
          />
        ))}
      </main>

      <footer className="app__input">
        <textarea
          rows={2}
          placeholder="Вопрос всем трём моделям сразу. Enter — отправить, Shift+Enter — перенос строки"
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={isBusy}
        />
        <div className="app__input-side">
          <button onClick={handleSend} disabled={isBusy || !input.trim()}>
            {isBusy ? 'Работаю…' : 'Спросить все 3'}
          </button>
          <button
            className="btn--ghost"
            onClick={() => setChats(EMPTY_CHATS)}
            disabled={isBusy || !hasHistory}
          >
            Очистить
          </button>
        </div>
      </footer>
    </div>
  );
}
