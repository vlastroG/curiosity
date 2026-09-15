import { formatTokens } from '../api.js';

/**
 * Окно контекста модели: сколько из него уже расписано и сколько осталось.
 *
 * Всё считается по фактическим числам API, ничего не оценивается. Занято -- это
 * prompt_tokens последнего запроса плюс видимая часть последнего ответа: ровно то,
 * что уедет в модель в следующий раз. Резерв -- max_tokens: место, которое надо
 * оставить под будущий ответ, иначе провайдер отобьёт запрос.
 */

const HINT =
  'Занято = вход последнего запроса (prompt_tokens) + видимая часть последнего ответа: ' +
  'именно это уедет в модель в следующий раз. Резерв = max_tokens, место под будущий ответ. ' +
  'Провайдер требует, чтобы вход и резерв вместе помещались в окно модели. ' +
  'При глубине истории меньше длины диалога старые сообщения выпадают из запроса, ' +
  'и реальный вход может оказаться меньше показанного.';

export function ContextBar({ context }) {
  if (!context || !context.modelLimit) return null;

  const limit = context.modelLimit;
  const carriedPercent = Math.min(100, (context.carried / limit) * 100);
  const reservePercent = Math.min(100 - carriedPercent, (context.reserve / limit) * 100);

  const state = context.full ? 'full' : context.warning ? 'warn' : 'ok';

  return (
    <div className={`ctx ctx--${state}`} title={HINT}>
      <span className="ctx__label">контекст</span>

      <span className="ctx__track">
        <span className="ctx__fill" style={{ width: `${carriedPercent}%` }} />
        <span className="ctx__reserve" style={{ width: `${reservePercent}%` }} />
      </span>

      <span className="ctx__numbers">
        {formatTokens(context.carried)} + {formatTokens(context.reserve)} резерв
        {' из '}
        {formatTokens(limit)} ({context.percent.toFixed(context.percent < 10 ? 1 : 0)}%)
      </span>

      {context.full ? (
        <span className="ctx__note">
          окно заполнено — очистите историю, уменьшите max_tokens или глубину истории
        </span>
      ) : (
        <span className="ctx__note">
          осталось {formatTokens(context.available)} под новый вопрос
          {context.warning && ' — окно почти заполнено'}
        </span>
      )}
    </div>
  );
}
