import { useState } from 'react';
import { isFresh, memoryLine } from '../format.js';

const MAX_TOPIC = 200;
const MAX_ITEM = 300;
const MAX_ITEMS = 12;

// TaskMemory -- цель беседы и к чему в ней пришли. Модель ведёт память
// сама; пользователь правит тему (и закрепляет её), тезисы и открытые
// вопросы. Каждая правка сразу уходит на сервер целиком.
export default function TaskMemory({ chat, freshSeq, onSave }) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState('');
  const s = chat.state;
  const lastSeq = chat.messages?.length || 0;

  const save = async (next) => {
    setError('');
    try {
      await onSave(next);
      return true;
    } catch (e) {
      setError(e.message);
      return false;
    }
  };

  const freshCount = [...s.theses, ...s.open].filter((it) => isFresh(it, freshSeq)).length;
  const line = memoryLine(chat);

  return (
    <section className={`memory ${open ? 'open' : ''}`}>
      <button type="button" className="memory-bar" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="memory-label">Память задачи</span>
        <span className="memory-topic">
          {s.topicLocked && <span title="Тема закреплена">📌 </span>}
          {s.topic || <span className="muted">тема появится после первой реплики</span>}
        </span>
        {line && <span className="muted small">{line}</span>}
        {freshCount > 0 && <span className="pill good">+{freshCount} новое</span>}
        <span className="memory-caret" aria-hidden="true">
          {open ? '▴' : '▾'}
        </span>
      </button>
      {open && (
        <div className="memory-body">
          <Topic s={s} onSave={save} />
          <List
            title="Тезисы"
            hint="к чему пришли в беседе"
            items={s.theses}
            freshSeq={freshSeq}
            lastSeq={lastSeq}
            onChange={(theses) => save({ ...s, theses })}
          />
          <List
            title="Открытые вопросы"
            hint="что ещё хотели обсудить"
            items={s.open}
            freshSeq={freshSeq}
            lastSeq={lastSeq}
            onChange={(o) => save({ ...s, open: o })}
            onDone={
              s.theses.length < MAX_ITEMS
                ? (i) => save({ ...s, open: s.open.filter((_, j) => j !== i), theses: [...s.theses, { ...s.open[i], by: 'user' }] })
                : null
            }
          />
          {chat.summary && <Summary chat={chat} />}
          {error && <div className="error">{error}</div>}
          <p className="muted small">
            Модель видит память в каждом ответе. Пункты с пометкой «ваш» она не удаляет; закреплённую тему не меняет.
          </p>
        </div>
      )}
    </section>
  );
}

function Topic({ s, onSave }) {
  const [edit, setEdit] = useState(null);
  const submit = async (e) => {
    e.preventDefault();
    const topic = edit.trim();
    if (topic && (await onSave({ ...s, topic, topicLocked: true }))) setEdit(null);
  };
  if (edit !== null) {
    return (
      <form className="memory-topic-edit" onSubmit={submit}>
        <input value={edit} onChange={(e) => setEdit(e.target.value)} maxLength={MAX_TOPIC} autoFocus aria-label="Тема беседы" />
        <button className="btn small primary" type="submit" disabled={!edit.trim()}>
          Сохранить и закрепить
        </button>
        <button className="link" type="button" onClick={() => setEdit(null)}>
          Отмена
        </button>
      </form>
    );
  }
  return (
    <div className="memory-topic-row">
      <div className="memory-topic-big">
        {s.topic || <span className="muted">Тема ещё не определена</span>}
        {s.topicLocked && <span className="pill">📌 закреплена</span>}
      </div>
      <div className="memory-actions">
        <button className="link" onClick={() => setEdit(s.topic)}>
          изменить
        </button>
        {s.topic &&
          (s.topicLocked ? (
            <button className="link" onClick={() => onSave({ ...s, topicLocked: false })} title="Модель снова сможет уточнять тему">
              открепить
            </button>
          ) : (
            <button className="link" onClick={() => onSave({ ...s, topicLocked: true })}>
              закрепить
            </button>
          ))}
      </div>
    </div>
  );
}

function List({ title, hint, items, freshSeq, lastSeq, onChange, onDone }) {
  const [editing, setEditing] = useState(-1); // -1 -- нет, items.length -- новый пункт
  const [text, setText] = useState('');
  const start = (i) => {
    setEditing(i);
    setText(i < items.length ? items[i].text : '');
  };
  const commit = (e) => {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    const next =
      editing < items.length
        ? items.map((it, j) => (j === editing ? { ...it, text: t, by: 'user' } : it))
        : [...items, { text: t, by: 'user', since: lastSeq }];
    onChange(next);
    setEditing(-1);
  };
  const form = (
    <form className="memory-item-edit" onSubmit={commit}>
      <input value={text} onChange={(e) => setText(e.target.value)} maxLength={MAX_ITEM} autoFocus aria-label={title} />
      <button className="btn small primary" type="submit" disabled={!text.trim()}>
        Сохранить
      </button>
      <button className="link" type="button" onClick={() => setEditing(-1)}>
        Отмена
      </button>
    </form>
  );
  return (
    <div className="memory-list">
      <div className="settings-title">
        {title} <span className="memory-hint">— {hint}</span>
      </div>
      {items.length === 0 && editing < 0 && <div className="muted small">пока пусто</div>}
      <ul>
        {items.map((it, i) =>
          editing === i ? (
            <li key={i}>{form}</li>
          ) : (
            <li key={i} className={isFresh(it, freshSeq) ? 'fresh' : ''}>
              <span className="memory-text">{it.text}</span>
              {it.by === 'user' && <span className="pill">ваш</span>}
              {isFresh(it, freshSeq) && <span className="pill good">новое</span>}
              {it.since > 0 && <span className="muted small">после #{it.since}</span>}
              <span className="memory-actions">
                {onDone && (
                  <button className="link" onClick={() => onDone(i)} title="Перенести в тезисы">
                    обсудили
                  </button>
                )}
                <button className="link" onClick={() => start(i)} aria-label="Изменить">
                  ✎
                </button>
                <button className="link" onClick={() => onChange(items.filter((_, j) => j !== i))} aria-label="Удалить">
                  ×
                </button>
              </span>
            </li>
          )
        )}
        {editing === items.length && <li>{form}</li>}
      </ul>
      {editing < 0 && items.length < MAX_ITEMS && (
        <button className="link small" onClick={() => start(items.length)}>
          + добавить
        </button>
      )}
    </div>
  );
}

function Summary({ chat }) {
  const [show, setShow] = useState(false);
  return (
    <div className="memory-list">
      <button className="link" onClick={() => setShow(!show)}>
        {show ? 'Скрыть сводку' : `Показать сводку (сообщения 1–${chat.summarizedUpto})`}
      </button>
      {show && <p className="memory-summary">{chat.summary}</p>}
    </div>
  );
}
