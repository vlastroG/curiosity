import { formatSeconds, formatTokens, formatUSD, plural } from '../api.js';

/**
 * Сводка по всему диалогу.
 *
 * Вход и выход суммируются раздельно и ровно по тем же числам, что показаны
 * в ленте под сообщениями: вход берётся у вопросов, выход у ответов. Так итог
 * в шапке сходится с тем, что видно глазами, и заодно видно главное свойство
 * диалога -- вход растёт быстрее выхода, потому что история едет заново каждый раз.
 */
function totalsOf(messages) {
  const answers = messages.filter((message) => message.kind === 'answer' && message.meta);
  if (answers.length === 0) return null;

  return {
    answers: answers.length,
    // суммы идут по всем сообщениям, а не только по ответам: служебный вызов сжатия
    // тоже тратит токены и деньги, и его отметка несёт свои метрики
    tokensIn: messages.reduce((sum, m) => sum + (m.input?.tokens ?? 0), 0),
    tokensOut: messages.reduce((sum, m) => sum + (m.meta?.usage?.completion_tokens ?? 0), 0),
    cost: messages.reduce((sum, m) => sum + (m.meta?.totalUsd ?? 0), 0),
    compactions: messages.filter((m) => m.kind === 'summary').length,
    avgLatency: answers.reduce((sum, m) => sum + m.meta.latencyMs, 0) / answers.length,
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
          {totals.compactions > 0 && (
            <span title="сколько раз окно истории закрывалось и сворачивалось в пересказ">
              окно закрывалось {totals.compactions}{' '}
              {plural(totals.compactions, 'раз', 'раза', 'раз')}
            </span>
          )}
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
