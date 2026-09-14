import { useState } from 'react';

/**
 * Снимок памяти хода — что именно уехало в запрос из каждого слоя.
 *
 * Ради этого блока задание дня 11 и существует: без него «модель памяти» остаётся
 * словами в README. Три секции помечены цветом слоя, и цвет здесь не украшение —
 * он отвечает на вопрос «что где лежит» раньше, чем человек прочтёт подписи.
 */

const HINTS = {
  long:
    'Долговременная память: знания из общего справочника, которые агент отобрал ' +
    'под этот вид работ. Живут вне чатов и не стираются ни очисткой истории, ни закрытием задачи.',
  work:
    'Рабочая память: чеклист исходных данных текущей задачи. Живёт ровно столько, ' +
    'сколько задача, и освобождается при её закрытии или прерывании.',
  short:
    'Краткосрочная память: окно последних сообщений, пересказ закрытого окна ' +
    'и итоги решённых задач этого диалога.',
};

export function MemoryDisclosure({ memory, decision }) {
  const [open, setOpen] = useState(false);

  if (!memory) return null;

  const knowledge = memory.knowledge ?? [];
  const requirements = memory.requirements ?? [];
  const solved = memory.solvedTasks ?? [];
  const filled = requirements.filter((req) => Boolean(req.value)).length;

  return (
    <div className="memory">
      <button className="trace__toggle" onClick={() => setOpen(!open)}>
        {open ? '▾' : '▸'} память хода
        <span className="memory__counts">
          {' '}
          знаний {knowledge.length} · исходных данных {filled}/{requirements.length} · история{' '}
          {memory.historyMessages}
          {memory.windowSummary ? ' + пересказ' : ''}
        </span>
      </button>

      {open && (
        <div className="memory__body">
          <section className="memory__layer memory__layer--long" title={HINTS.long}>
            <span className="memory__label">долговременная — знания</span>
            {knowledge.length === 0 ? (
              <span className="muted">ничего не подставлялось</span>
            ) : (
              <div className="memory__chips">
                {knowledge.map((item) => (
                  <span key={item.id} className="chip chip--knowledge">
                    {item.title}
                  </span>
                ))}
              </div>
            )}
          </section>

          <section className="memory__layer memory__layer--work" title={HINTS.work}>
            <span className="memory__label">
              рабочая — исходные данные
              {memory.taskTitle ? `: ${memory.taskTitle}` : ''}
            </span>
            {requirements.length === 0 ? (
              <span className="muted">активной задачи нет</span>
            ) : (
              <dl className="checklist">
                {requirements.map((req) => (
                  <div
                    key={req.key}
                    className={`checklist__item${req.value ? '' : ' checklist__item--empty'}`}
                  >
                    <dt>
                      <span className="checklist__mark">{req.value ? '✓' : '—'}</span>
                      {req.key}
                    </dt>
                    <dd>{req.value || req.question}</dd>
                  </div>
                ))}
              </dl>
            )}
          </section>

          <section className="memory__layer memory__layer--short" title={HINTS.short}>
            <span className="memory__label">краткосрочная — диалог</span>
            <div className="memory__chips">
              <span className="chip">окно: {memory.historyMessages} сообщ.</span>
              {memory.windowSummary && <span className="chip">пересказ окна</span>}
              {solved.map((item) => (
                <span key={item.id} className="chip">
                  решено: {item.title}
                </span>
              ))}
              {solved.length === 0 && !memory.windowSummary && memory.historyMessages === 0 && (
                <span className="muted">диалог только начался</span>
              )}
            </div>
          </section>

          {decision && (
            <div className="memory__decision">
              решение машины состояний: <code>{decision}</code>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
