import { useState } from 'react';
import { plural } from '../api.js';

/**
 * Полоса состояния машины задач.
 *
 * Видна всегда, даже когда задачи нет: машина состояний — главное, что показывает
 * день 11, и угадывать её состояние по репликам агента неправильно. Отсюда же
 * разворачивается рабочая память — чеклист исходных данных.
 */

const STATES = {
  idle: {
    label: 'ожидание задачи',
    hint:
      'Активной задачи нет. Опишите, какие строительные работы нужно выполнить, — ' +
      'агент заведёт задачу и начнёт собирать исходные данные.',
  },
  confirming: {
    label: 'сверка данных',
    hint:
      'Чеклист заполнен, агент показывает собранное и просит подтвердить. ' +
      'Обязательный шаг: диспетчер способен пометить пункт собранным, когда о нём ' +
      'и не заикались, — здесь это видно и поправимо. Следующим сообщением будет план.',
  },
  collecting: {
    label: 'сбор исходных данных',
    hint:
      'Идёт опрос. План появится, когда чеклист будет заполнен: это решает код, ' +
      'а не модель. Если опрос затянется, план выдадут с явными допущениями.',
  },
  done: {
    label: 'план выдан',
    hint: 'Задача закрыта. Её итог переехал в память диалога — можно начинать следующую.',
  },
  cancelled: {
    label: 'задача прервана',
    hint: 'Рабочая память этой задачи освобождена. История диалога осталась на месте.',
  },
};

function stateOf(task) {
  if (!task) return 'idle';
  if (task.status === 'cancelled') return 'cancelled';
  if (task.status !== 'collecting') return 'done';

  // чеклист заполнен, но задача ещё открыта -- значит идёт сверка перед планом
  const reqs = task.requirements ?? [];
  const full = reqs.length > 0 && reqs.every((req) => Boolean(req.value));
  return full ? 'confirming' : 'collecting';
}

/** Чеклист исходных данных — рабочая память задачи. */
function Checklist({ requirements }) {
  return (
    <dl className="checklist">
      {requirements.map((req) => {
        const filled = Boolean(req.value);
        return (
          <div key={req.key} className={`checklist__item${filled ? '' : ' checklist__item--empty'}`}>
            <dt>
              <span className="checklist__mark">{filled ? '✓' : '—'}</span>
              {req.key}
            </dt>
            <dd>{filled ? req.value : req.question}</dd>
          </div>
        );
      })}
    </dl>
  );
}

export function TaskStatusBar({ task, solved, knowledge, busy, onCancel }) {
  const [open, setOpen] = useState(false);

  const state = stateOf(task);
  const meta = STATES[state];
  const requirements = task?.requirements ?? [];
  const filled = requirements.filter((req) => Boolean(req.value)).length;
  const expandable = requirements.length > 0 || solved.length > 0;

  return (
    <div className={`task task--${state}`} title={meta.hint}>
      <div className="task__line">
        <span className="task__label">{meta.label}</span>

        {task ? (
          <>
            <span className="task__title">{task.title}</span>
            <span className="task__progress">
              собрано {filled} из {requirements.length}
            </span>
          </>
        ) : (
          <span className="task__hint">опишите, какие работы нужно выполнить</span>
        )}

        <span className="task__spacer" />

        {expandable && (
          <button className="task__toggle" onClick={() => setOpen(!open)}>
            {open ? '▾ свернуть' : '▸ рабочая память'}
          </button>
        )}
        {(state === 'collecting' || state === 'confirming') && (
          <button
            className="btn btn--ghost"
            onClick={onCancel}
            disabled={busy}
            title="освободить рабочую память и вернуться к ожиданию задачи"
          >
            прервать
          </button>
        )}
      </div>

      {open && (
        <div className="task__body">
          {requirements.length > 0 && <Checklist requirements={requirements} />}

          {knowledge.length > 0 && (
            <div className="task__knowledge">
              <span className="task__section">знания под задачу</span>
              {knowledge.map((item) => (
                <span key={item.id} className="chip chip--knowledge">
                  {item.title}
                </span>
              ))}
            </div>
          )}

          {solved.length > 0 && (
            <div className="task__solved">
              <span className="task__section">
                решено в этом диалоге: {solved.length}{' '}
                {plural(solved.length, 'задача', 'задачи', 'задач')}
              </span>
              {solved.map((item) => (
                <div key={item.id} className="task__solved-item">
                  <strong>{item.title}</strong>
                  <span>{item.summary}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
