import { formatSeconds, formatTokens, formatUSD } from '../api.js';

/**
 * Сводка по всему диалогу.
 *
 * Вход и выход суммируются раздельно и ровно по тем же числам, что показаны
 * в ленте под сообщениями: вход берётся у вопросов, выход у ответов. Так итог
 * в шапке сходится с тем, что видно глазами, и заодно видно главное свойство
 * диалога -- вход растёт быстрее выхода, потому что история едет заново каждый раз.
 */
function totalsOf(messages) {
  const answers = messages.filter((message) => message.meta && message.meta.calls > 0);
  if (answers.length === 0) return null;

  const scored = answers.filter((message) => message.meta.judge);

  return {
    answers: answers.length,
    tokensIn: messages.reduce((sum, m) => sum + (m.input?.tokens ?? 0), 0),
    tokensOut: answers.reduce((sum, m) => sum + (m.meta.usage?.completion_tokens ?? 0), 0),
    cost: answers.reduce((sum, m) => sum + (m.meta.totalUsd ?? 0), 0),
    avgLatency: answers.reduce((sum, m) => sum + m.meta.latencyMs, 0) / answers.length,
    avgScore: scored.length
      ? scored.reduce((sum, m) => sum + m.meta.judge.score, 0) / scored.length
      : null,
  };
}

export function StatsBar({ chat }) {
  const totals = totalsOf(chat.messages);
  const config = chat.config;

  return (
    <div className="stats">
      <span className="stats__model">{config.model}</span>
      <span>t° {config.temperature}</span>
      <span>max_tokens {config.maxTokens}</span>
      <span>история {config.historyDepth}</span>
      {config.judgeEnabled && <span className="stats__judge">судья вкл.</span>}

      {totals ? (
        <>
          <span className="stats__sep" />
          <span>{totals.answers} ответов</span>
          <span title="сумма prompt_tokens по всем запросам чата">
            Σ вход {formatTokens(totals.tokensIn)}
          </span>
          <span title="сумма completion_tokens по всем ответам чата">
            Σ выход {formatTokens(totals.tokensOut)}
          </span>
          <span className="meta__cost">Σ {formatUSD(totals.cost)}</span>
          <span>~{formatSeconds(totals.avgLatency)} на ответ</span>
          {totals.avgScore !== null && <span>средняя оценка {totals.avgScore.toFixed(1)}</span>}
        </>
      ) : (
        <>
          <span className="stats__sep" />
          <span className="muted">ответов ещё нет</span>
        </>
      )}
    </div>
  );
}
