import { useState } from 'react';
import { Markdown } from '../markdown.jsx';
import { formatSeconds, formatTokens, formatUSD, plural } from '../api.js';

/**
 * Граница окна истории — момент, когда прошлые сообщения перестали уезжать в модель.
 *
 * Плашка обязана быть заметной: с этого места агент разговаривает не с диалогом,
 * а с его пересказом. Без отметки поведение выглядит как необъяснимая амнезия
 * посреди разговора.
 *
 * Сами свёрнутые сообщения из ленты никуда не деваются: сжимается то, что уходит
 * в модель, а не то, что видит человек.
 */

const SUMMARY_HINT =
  'Окно истории заполнилось. Прошлые сообщения свёрнуты в пересказ, и дальше ' +
  'в запрос уезжает он вместо них. Следующее сжатие сложит этот пересказ с новым окном.';

export function BoundaryMessage({ message }) {
  const [open, setOpen] = useState(false);

  const covered = message.compaction?.covered ?? 0;
  const recursive = message.compaction?.recursive;
  const meta = message.meta;

  return (
    <div className="boundary boundary--summary" title={SUMMARY_HINT}>
      <div className="boundary__line">
        <span className="boundary__label">история сжата</span>
      </div>

      <div className="boundary__meta">
        <span>
          свёрнуто {formatTokens(covered)}{' '}
          {plural(covered, 'сообщение', 'сообщения', 'сообщений')}
        </span>
        {recursive && (
          <span title="в пересказ вошёл предыдущий пересказ — память накапливается рекурсивно">
            вместе с прошлым пересказом
          </span>
        )}
        {meta && (
          <>
            <span>
              пересказ {formatTokens(meta.usage?.completion_tokens)} tok
              {meta.reasoningTokens > 0 &&
                `, ещё ${formatTokens(meta.reasoningTokens)} на рассуждение`}
            </span>
            <span className="meta__cost">{formatUSD(meta.totalUsd)}</span>
            <span>{formatSeconds(meta.latencyMs)}</span>
          </>
        )}
      </div>

      <button className="trace__toggle" onClick={() => setOpen(!open)}>
        {open ? '▾ скрыть пересказ' : '▸ показать пересказ'}
      </button>

      {open && (
        <div className="boundary__text">
          <Markdown text={message.content} />
        </div>
      )}
    </div>
  );
}
