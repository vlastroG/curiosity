import { useState } from 'react';
import { BoundaryMessage } from './BoundaryMessage.jsx';
import { Markdown } from '../markdown.jsx';
import { formatSeconds, formatTokens, formatUSD } from '../api.js';

/**
 * Метрики хода разнесены по двум сообщениям, и это главное, что нужно понимать
 * про цифры в ленте.
 *
 * Каждый запрос уезжает в модель целиком заново, поэтому prompt_tokens -- это весь
 * вызов: system prompt плюс история плюс новый вопрос. Число относится к запросу,
 * который породил вопрос пользователя, и стоит под ним. completion_tokens -- только
 * этот ответ, и стоит под ответом. Раньше оба показывались одной строкой через
 * стрелку под ответом, и понять, почему у соседних ходов «432» и «234» не сходятся,
 * было невозможно: это разные величины.
 */

const INPUT_HINT =
  'prompt_tokens: весь запрос целиком — system prompt, история диалога и этот вопрос. ' +
  'Растёт с каждым ходом, потому что диалог уезжает в модель заново.';

const OUTPUT_HINT =
  'completion_tokens: только этот ответ. Рассуждение оплачивается наравне с текстом, ' +
  'но в видимый ответ не попадает и в историю следующего запроса не уезжает.';

/** Метрики входа. Стоят под вопросом пользователя -- это его вызов. */
function InputMeta({ input }) {
  return (
    <div className="meta meta--input" title={INPUT_HINT}>
      <span className="meta__label">вход</span>
      <span className="meta__tokens">{formatTokens(input.tokens)} tok</span>

      {input.hasDelta && (
        <span
          className="meta__delta"
          title="насколько запрос вырос против прошлого хода этого чата — точная разница двух чисел API"
        >
          {input.delta >= 0 ? '+' : '−'}
          {formatTokens(Math.abs(input.delta))} к прошлому
        </span>
      )}

      <span title="сколько сообщений истории уехало в запрос вместе с вопросом">
        история {input.historyMessages}
      </span>

      {input.cacheHit > 0 && (
        <span title="попадания в кеш промпта тарифицируются в разы дешевле">
          {formatTokens(input.cacheHit)} из кеша
        </span>
      )}

      <span className="meta__cost">{formatUSD(input.usd)}</span>
    </div>
  );
}

/** Метрики выхода. Стоят под ответом модели. */
function OutputMeta({ meta }) {
  const usage = meta.usage ?? {};
  const cost = meta.cost ?? {};

  return (
    <div className="meta" title={OUTPUT_HINT}>
      <span className="meta__label">выход</span>
      <span className="meta__tokens">{formatTokens(usage.completion_tokens)} tok</span>

      {meta.reasoningTokens > 0 && (
        <span title="эти токены оплачены, но в видимом ответе их нет">
          из них {formatTokens(meta.reasoningTokens)} на рассуждение
        </span>
      )}

      <span className="meta__cost">{formatUSD(cost.outputUsd)}</span>
      <span>{formatSeconds(meta.latencyMs)}</span>

      {cost.offPeak && <span title="вне пиковых часов ставка вдвое ниже">off-peak</span>}

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
      {judge.cost?.usd > 0 && (
        <span className="meta__cost" title="второй вызов модели тоже стоит денег">
          судья {formatUSD(judge.cost.usd)}
        </span>
      )}
    </div>
  );
}

/**
 * Длинные вопросы сворачиваются.
 *
 * Диалог, упирающийся в окно контекста, состоит из простыней на десятки тысяч
 * символов -- без сворачивания лента превращается в сплошную стену, и метрик,
 * ради которых всё затевалось, в ней не найти.
 */
const LONG_TEXT = 600;

function QuestionText({ text }) {
  const [open, setOpen] = useState(false);

  if (text.length <= LONG_TEXT) {
    return <div className="bubble__text">{text}</div>;
  }

  return (
    <>
      <div className="bubble__text">{open ? text : `${text.slice(0, LONG_TEXT)}…`}</div>
      <button className="trace__toggle" onClick={() => setOpen(!open)}>
        {open ? '▾ свернуть' : `▸ показать целиком (${formatTokens(text.length)} символов)`}
      </button>
    </>
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
  // граница окна истории -- не реплика, а служебная отметка во всю ширину ленты
  if (message.kind === 'summary' || message.kind === 'dropped') {
    return <BoundaryMessage message={message} />;
  }

  if (message.kind === 'question') {
    return (
      <div className="bubble bubble--user">
        <QuestionText text={message.content} />
        {/* у вопросов из чатов дня 6 и у отклонённых политикой метрик входа нет:
            вызова не было либо он был до того, как их начали сохранять */}
        {message.input && <InputMeta input={message.input} />}
      </div>
    );
  }

  const meta = message.meta;
  // отказ политики -- это штатная работа агента, а сбой провайдера -- авария снаружи;
  // показываем их по-разному, чтобы не путать одно с другим
  const failed = message.kind === 'failed';
  const blocked = message.kind === 'blocked';
  // переполнение окна -- не авария провайдера, а прямое следствие размера диалога,
  // поэтому у него своя плашка с объяснением, что делать
  const overflow = message.kind === 'overflow';
  const answered = meta && meta.calls > 0;

  return (
    <div className="bubble bubble--assistant">
      {answered && <OutputMeta meta={meta} />}

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

      {overflow && (
        <div className="notice notice--overflow">
          <span className="notice__label">переполнение контекста</span>
          Запрос не поместился в окно модели. Вопрос в контекст не попал, поэтому
          следующая попытка не станет тяжелее — сократите вопрос, очистите историю
          или уменьшите её глубину.
          <div className="notice__raw">{message.content}</div>
        </div>
      )}

      {!blocked && !failed && !overflow && <Markdown text={message.content} />}

      {meta?.warnings?.map((warning, index) => (
        <div key={index} className="notice notice--warn">
          {warning}
        </div>
      ))}

      {meta?.judge && <Judge judge={meta.judge} />}

      {answered && (
        <div className="turn-total" title="вход этого хода плюс выход, включая судью, если он включён">
          итого за ход {formatUSD(meta.totalUsd)}
        </div>
      )}

      {meta?.trace?.length > 0 && <Trace trace={meta.trace} />}
    </div>
  );
}
