import { useState } from 'react';

/**
 * Долговременная память — справочник знаний.
 *
 * Единственный слой, который живёт вне чатов и заполняется руками: правило
 * «железобетон делаем по СП 63» не принадлежит ни одному разговору, оно должно
 * подставляться во все задачи, где уместно. Поэтому и панель общая, а не в чате.
 */

const HINT =
  'Долговременная память. Заголовок — то, по чему агент выбирает знание под вид работ, ' +
  'поэтому он должен называть тему, а не пересказывать текст. ' +
  'Эти записи не стираются ни очисткой истории, ни закрытием задачи.';

function Editor({ item, busy, error, onSubmit, onCancel }) {
  const [title, setTitle] = useState(item?.title ?? '');
  const [text, setText] = useState(item?.text ?? '');

  const submit = (event) => {
    event.preventDefault();
    if (!title.trim() || !text.trim() || busy) return;
    onSubmit({ title: title.trim(), text: text.trim() });
  };

  return (
    <form className="knowledge__editor" onSubmit={submit}>
      <label className="field">
        <span className="field__label">заголовок</span>
        <input
          autoFocus
          value={title}
          placeholder="например: Железобетонные конструкции"
          onChange={(event) => setTitle(event.target.value)}
        />
      </label>

      <label className="field">
        <span className="field__label">текст знания</span>
        <textarea
          rows={6}
          value={text}
          placeholder="Работы по железобетонным конструкциям вести строго по СП 63…"
          onChange={(event) => setText(event.target.value)}
        />
      </label>

      {error && <div className="notice notice--failed">{error}</div>}

      <div className="knowledge__actions">
        <button className="btn" type="submit" disabled={busy || !title.trim() || !text.trim()}>
          {busy ? 'сохраняю…' : 'сохранить'}
        </button>
        <button className="btn btn--ghost" type="button" onClick={onCancel} disabled={busy}>
          отмена
        </button>
      </div>
    </form>
  );
}

/**
 * Готовые знания под частые виды работ.
 *
 * Начинающему строителю неоткуда взять список сводов правил, и пустой справочник
 * он оставит пустым. Но сами заготовки не добавляются: что лежит в долговременной
 * памяти, пользователь решает явно — этого требует и задание.
 */
function Presets({ presets, items, busy, onAdd }) {
  const [open, setOpen] = useState(false);

  const taken = new Set(items.map((item) => item.title.trim().toLowerCase()));
  const available = presets.filter((preset) => !taken.has(preset.title.trim().toLowerCase()));

  if (available.length === 0) return null;

  return (
    <div className="knowledge__presets">
      <button className="trace__toggle" onClick={() => setOpen(!open)}>
        {open ? '▾' : '▸'} готовые знания ({available.length})
      </button>

      {open && (
        <>
          <span className="field__hint">
            Заготовки под частые виды работ. Это отправная точка, а не истина: редакции
            сводов правил меняются, у объекта бывает свой проект — добавьте и поправьте
            под себя.
          </span>
          <div className="knowledge__preset-list">
            {available.map((preset) => (
              <button
                key={preset.title}
                className="btn btn--chip"
                disabled={busy}
                title={preset.text.slice(0, 200)}
                onClick={() => onAdd({ title: preset.title, text: preset.text })}
              >
                + {preset.title}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export function KnowledgePanel({
  items,
  presets,
  busy,
  error,
  onCreate,
  onUpdate,
  onDelete,
  onClose,
}) {
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState(null);

  const stop = () => {
    setAdding(false);
    setEditing(null);
  };

  return (
    <aside className="knowledge" title={HINT}>
      <div className="knowledge__head">
        <span className="sidebar__title">долговременная память</span>
        <button className="btn btn--ghost" onClick={onClose} title="закрыть справочник">
          ×
        </button>
      </div>

      <div className="knowledge__body">
        <p className="field__hint">
          Знания общие для всех чатов. Агент сам выбирает по заголовку, какие из них
          подставить в задачу, и показывает выбранные в «памяти хода» под ответом.
        </p>

        <Presets presets={presets} items={items} busy={busy} onAdd={(payload) => onCreate(payload)} />

        {adding ? (
          <Editor
            busy={busy}
            error={error}
            onSubmit={(payload) => onCreate(payload, stop)}
            onCancel={stop}
          />
        ) : (
          <button className="btn" onClick={() => setAdding(true)}>
            + добавить знание
          </button>
        )}

        {items.length === 0 && !adding && (
          <p className="muted">Справочник пуст — агент будет опираться только на свои общие представления.</p>
        )}

        {items.map((item) =>
          editing === item.id ? (
            <Editor
              key={item.id}
              item={item}
              busy={busy}
              error={error}
              onSubmit={(payload) => onUpdate(item.id, payload, stop)}
              onCancel={stop}
            />
          ) : (
            <div key={item.id} className="knowledge__item">
              <div className="knowledge__item-head">
                <span className="knowledge__item-title">{item.title}</span>
                <button className="trace__toggle" onClick={() => setEditing(item.id)}>
                  править
                </button>
                <button className="chat-row__delete" title="удалить знание" onClick={() => onDelete(item.id)}>
                  ×
                </button>
              </div>
              <div className="knowledge__item-text">{item.text}</div>
            </div>
          )
        )}
      </div>
    </aside>
  );
}
