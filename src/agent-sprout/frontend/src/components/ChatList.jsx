import { formatUSD } from '../api.js';

/**
 * Левая колонка: дерево чатов, создание и удаление.
 *
 * Дерево, а не список, из-за третьей стратегии контекста: чекпоинт клонирует чат
 * в новую ветку, и увидеть две ветки, отпочковавшиеся от одной точки, — это и есть
 * весь смысл ветвления. В плоском списке родословную пришлось бы держать в голове.
 */

/** Время отпочкования ветки — короткой меткой, дата нужна редко. */
function branchTime(iso) {
  const at = new Date(iso);
  const today = new Date().toDateString() === at.toDateString();
  return at.toLocaleString('ru-RU', {
    hour: '2-digit',
    minute: '2-digit',
    ...(today ? {} : { day: '2-digit', month: '2-digit' }),
  });
}

/**
 * Раскладывает плоский список в дерево.
 *
 * Чат с неизвестным родителем (родителя удалили) становится корневым: иначе ветка
 * исчезнет из интерфейса вместе со всей своей историей.
 */
function buildTree(chats) {
  const known = new Set(chats.map((chat) => chat.id));
  const children = new Map();
  const roots = [];

  for (const chat of chats) {
    if (chat.parentId && known.has(chat.parentId)) {
      const siblings = children.get(chat.parentId) ?? [];
      siblings.push(chat);
      children.set(chat.parentId, siblings);
    } else {
      roots.push(chat);
    }
  }

  const flat = [];
  const walk = (chat, depth) => {
    flat.push({ chat, depth });
    for (const child of children.get(chat.id) ?? []) walk(child, depth + 1);
  };
  roots.forEach((chat) => walk(chat, 0));

  return flat;
}

function ChatRow({ chat, depth, active, onSelect, onDelete }) {
  return (
    <div
      className={`chat-row${active ? ' chat-row--active' : ''}`}
      style={{ paddingLeft: 10 + depth * 14 }}
      onClick={() => onSelect(chat.id)}
    >
      {depth > 0 && <span className="chat-row__branch">└</span>}

      <div className="chat-row__main">
        <span className="chat-row__title">
          {chat.title}
          {chat.tag && (
            <span className="chat-row__tag" title={`ветка от ${branchTime(chat.clonedAt)}`}>
              {chat.tag}
            </span>
          )}
        </span>
        <span className="chat-row__meta">
          {chat.clonedAt && <>{branchTime(chat.clonedAt)} · </>}
          {chat.config.model.split('/').pop()} · {chat.messages} сообщ.
          {chat.tasks > 0 && <> · {chat.tasks} задач</>} · {formatUSD(chat.totalUsd)}
        </span>
      </div>

      <button
        className="chat-row__delete"
        title="удалить чат"
        onClick={(event) => {
          event.stopPropagation();
          onDelete(chat.id);
        }}
      >
        ×
      </button>
    </div>
  );
}

export function ChatList({ chats, activeId, busy, onSelect, onCreate, onDelete }) {
  const tree = buildTree(chats);

  return (
    <aside className="sidebar">
      <div className="sidebar__head">
        <span className="sidebar__title">чаты</span>
        <button className="btn btn--ghost" onClick={onCreate} disabled={busy}>
          + новый
        </button>
      </div>

      <div className="sidebar__list">
        {chats.length === 0 && <p className="muted">пока пусто — создайте первый чат</p>}

        {tree.map(({ chat, depth }) => (
          <ChatRow
            key={chat.id}
            chat={chat}
            depth={depth}
            active={chat.id === activeId}
            onSelect={onSelect}
            onDelete={onDelete}
          />
        ))}
      </div>
    </aside>
  );
}
