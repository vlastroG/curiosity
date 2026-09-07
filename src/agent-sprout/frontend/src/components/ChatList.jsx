import { formatUSD } from '../api.js';

/**
 * Левая колонка: список чатов, создание и удаление.
 *
 * Каждый чат -- отдельный агент со своими настройками, поэтому в строке списка
 * видно модель и сколько этот чат уже стоил.
 */
export function ChatList({ chats, activeId, busy, onSelect, onCreate, onDelete }) {
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

        {chats.map((chat) => (
          <div
            key={chat.id}
            className={`chat-row${chat.id === activeId ? ' chat-row--active' : ''}`}
            onClick={() => onSelect(chat.id)}
          >
            <div className="chat-row__main">
              <span className="chat-row__title">{chat.title}</span>
              <span className="chat-row__meta">
                {chat.config.model.split('/').pop()} · {chat.messages} сообщ. · {formatUSD(chat.totalUsd)}
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
        ))}
      </div>
    </aside>
  );
}
