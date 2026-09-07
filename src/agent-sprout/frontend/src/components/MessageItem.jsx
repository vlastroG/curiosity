import { useState } from 'react';
import { Markdown } from '../markdown.jsx';
import { formatSeconds, formatUSD } from '../api.js';

/**
 * Метрики под ответом. Здесь и видно, что коробка сделала с запросом:
 * сколько времени, сколько токенов, сколько денег и по какому тарифу.
 */
function Meta({ meta }) {
  const usage = meta.usage ?? {};
  const cached = usage.prompt_cache_hit_tokens ?? 0;

  return (
    <div className="meta">
      <span>{formatSeconds(meta.latencyMs)}</span>
      <span>
        {usage.prompt_tokens ?? '?'} → {usage.completion_tokens ?? '?'} tok
      </span>
      {meta.reasoningTokens > 0 && <span>из них {meta.reasoningTokens} на рассуждение</span>}
      {cached > 0 && <span>{cached} из кеша</span>}
      <span className="meta__cost">
        {formatUSD(meta.totalUsd)}
        {meta.cost?.offPeak ? ' (off-peak)' : ''}
      </span>
      {meta.calls > 1 && <span>{meta.calls} вызова модели</span>}
      {meta.finishReason && meta.finishReason !== 'stop' && (
        <span className="meta__warn">finish: {meta.finishReason}</span>
      )}
    </div>
  );
}

/** Вердикт судьи: оценка и одно предложение по существу. */
function Judge({ judge }) {
  return (
    <div className="judge">
      <span className="judge__score" title={`оценка ${judge.score} из 5`}>
        {'★'.repeat(judge.score)}
        {'☆'.repeat(5 - judge.score)}
      </span>
      <span className="judge__verdict">{judge.verdict}</span>
    </div>
  );
}

/** Трейс конвейера. Свёрнут по умолчанию: нужен, когда что-то пошло не так. */
function Trace({ trace }) {
  const [open, setOpen] = useState(false);

  return (
    <div className="trace">
      <button className="trace__toggle" onClick={() => setOpen(!open)}>
        {open ? '▾' : '▸'} трейс агента ({trace.length})
      </button>

      {open && (
        <ol className="trace__list">
          {trace.map((step, index) => (
            <li key={index} className={step.ok ? '' : 'trace__step--failed'}>
              <span className="trace__name">{step.name}</span>
              <span className="trace__time">{step.durationMs} мс</span>
              {step.detail && <span className="trace__detail">{step.detail}</span>}
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

export function MessageItem({ message }) {
  if (message.kind === 'question') {
    return <div className="bubble bubble--user">{message.content}</div>;
  }

  const meta = message.meta;
  // отказ политики -- это штатная работа агента, а сбой провайдера -- авария снаружи;
  // показываем их по-разному, чтобы не путать одно с другим
  const failed = message.kind === 'failed';
  const blocked = message.kind === 'blocked';

  return (
    <div className="bubble bubble--assistant">
      {meta && meta.calls > 0 && <Meta meta={meta} />}

      {blocked && (
        <div className="notice notice--blocked">
          <span className="notice__label">политика агента</span>
          {message.content}
        </div>
      )}

      {failed && (
        <div className="notice notice--failed">
          <span className="notice__label">сбой провайдера</span>
          {message.content}
        </div>
      )}

      {!blocked && !failed && <Markdown text={message.content} />}

      {meta?.warnings?.map((warning, index) => (
        <div key={index} className="notice notice--warn">
          {warning}
        </div>
      ))}

      {meta?.judge && <Judge judge={meta.judge} />}
      {meta?.trace?.length > 0 && <Trace trace={meta.trace} />}
    </div>
  );
}
