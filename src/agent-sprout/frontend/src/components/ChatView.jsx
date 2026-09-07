import { useEffect, useRef, useState } from 'react';
import { MessageItem } from './MessageItem.jsx';
import { StatsBar } from './StatsBar.jsx';

/** Центральная колонка: шапка со сводкой, лента сообщений и поле ввода. */
export function ChatView({ chat, pending, settingsOpen, onSend, onClear, onToggleSettings }) {
  const [input, setInput] = useState('');
  const bottomRef = useRef(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [chat.messages, pending]);

  const send = () => {
    const text = input.trim();
    if (!text || pending) return;
    setInput('');
    onSend(text);
  };

  const handleKeyDown = (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      send();
    }
  };

  return (
    <main className="chat">
      <header className="chat__head">
        <h2 className="chat__title">{chat.title}</h2>
        <div className="chat__actions">
          <button className="btn btn--ghost" onClick={onClear} disabled={pending || chat.messages.length === 0}>
            очистить историю
          </button>
          <button className="btn btn--ghost" onClick={onToggleSettings}>
            {settingsOpen ? 'скрыть настройки' : 'настройки'}
          </button>
        </div>
      </header>

      <StatsBar chat={chat} />

      <div className="chat__body">
        {chat.messages.length === 0 && !pending && (
          <p className="muted">
            Пустой чат. Запрос пройдёт через входную политику, уйдёт в модель и вернётся
            с метриками и трейсом.
          </p>
        )}

        {chat.messages.map((message) => (
          <MessageItem key={message.id} message={message} />
        ))}

        {pending && (
          <div className="bubble bubble--assistant">
            <span className="muted">
              агент работает
              <span className="cursor" />
            </span>
          </div>
        )}

        <div ref={bottomRef} />
      </div>

      <footer className="chat__input">
        <textarea
          rows={2}
          placeholder="Запрос агенту. Enter — отправить, Shift+Enter — перенос строки"
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={pending}
        />
        <button className="btn" onClick={send} disabled={pending || !input.trim()}>
          {pending ? 'жду…' : 'отправить'}
        </button>
      </footer>
    </main>
  );
}
