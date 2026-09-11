import { useState } from 'react';

/**
 * Sticky facts — key-value память чата.
 *
 * Показывать её обязательно: это буквально то, что уезжает в модель вместо забытой
 * части диалога. Без панели остаётся гадать, что агент про вас помнит, а что уже нет.
 *
 * Память копится через весь чат и переживает закрытие окна истории — в отличие
 * от пересказа, который каждый раз переписывается заново.
 */

const HINT =
  'Память обновляется отдельным вызовом модели после каждой пары вопрос-ответ ' +
  'и уезжает в каждый следующий запрос. Факт, названный в последнем ходе, ' +
  'попадёт в запрос начиная со следующего.';

export function FactsPanel({ facts }) {
  const [open, setOpen] = useState(false);

  // панели нет, пока память пуста: в свежем чате пустая таблица только мешает
  if (!facts || facts.length === 0) return null;

  return (
    <div className="facts" title={HINT}>
      <button className="facts__toggle" onClick={() => setOpen(!open)}>
        {open ? '▾' : '▸'} <span className="facts__label">факты</span> ({facts.length})
      </button>

      {open && (
        <dl className="facts__list">
          {facts.map((fact) => (
            <div key={fact.key} className="facts__item">
              <dt>{fact.key}</dt>
              <dd>{fact.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}
