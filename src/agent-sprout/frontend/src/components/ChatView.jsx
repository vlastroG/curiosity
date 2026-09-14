import { useEffect, useRef, useState } from 'react';
import { CheckpointForm } from './CheckpointForm.jsx';
import { ContextBar } from './ContextBar.jsx';
import { TaskStatusBar } from './TaskStatusBar.jsx';
import { MessageItem } from './MessageItem.jsx';
import { StatsBar } from './StatsBar.jsx';

/** Центральная колонка: шапка со сводкой, лента сообщений и поле ввода. */
export function ChatView({
  chat,
  context,
  pending,
  settingsOpen,
  checkpoint,
  task,
  onSend,
  onCancelTask,
  onClear,
  onToggleSettings,
  onCheckpoint,
}) {
  const [input, setInput] = useState('');
  const bottomRef = useRef(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [chat.messages, pending]);

  // окно контекста кончилось: новый вопрос физически некуда положить.
  // Бэкенд отказал бы и сам, но глухая кнопка честнее потраченного запроса
  const full = Boolean(context?.full);

  const placeholder = task.active
    ? 'Ответьте на вопросы агента. Enter — отправить, Shift+Enter — перенос строки'
    : 'Опишите, какие строительные работы нужно выполнить';

  const send = () => {
    const text = input.trim();
    if (!text || pending || full) return;
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
          <button
            className="btn btn--ghost"
            onClick={() => checkpoint.toggle()}
            disabled={pending}
            title="клонировать чат в новую ветку вместе с историей, настройками и памятью"
          >
            чекпоинт
          </button>
          <button className="btn btn--ghost" onClick={onToggleSettings}>
            {settingsOpen ? 'скрыть настройки' : 'настройки'}
          </button>
        </div>
      </header>

      <StatsBar chat={chat} />
      <ContextBar context={context} />
      <TaskStatusBar
        task={task.active}
        solved={task.solved}
        knowledge={task.knowledge}
        busy={pending || task.busy}
        onCancel={onCancelTask}
      />

      {checkpoint.open && (
        <CheckpointForm
          busy={checkpoint.busy}
          error={checkpoint.error}
          onSubmit={onCheckpoint}
          onCancel={() => checkpoint.toggle()}
        />
      )}

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

        {pending && <Working />}

        <div ref={bottomRef} />
      </div>

      <footer className="chat__input">
        <textarea
          rows={2}
          placeholder={
            full ? 'Окно контекста заполнено — очистите историю или начните новый чат' : placeholder
          }
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={handleKeyDown}
          disabled={pending || full}
        />
        <button className="btn" onClick={send} disabled={pending || full || !input.trim()}>
          {pending ? 'жду…' : full ? 'нет места' : 'отправить'}
        </button>
      </footer>
    </main>
  );
}

/**
 * Пузырь ожидания со счётчиком секунд.
 *
 * На рассуждающей модели ход идёт десятками секунд: сначала думает диспетчер,
 * потом модель над самим планом. Неподвижная надпись в этот момент неотличима
 * от зависания, и человек жмёт перезагрузку ровно тогда, когда ждать оставалось
 * немного. Бегущая цифра стоит одного интервала и снимает весь вопрос.
 */
function Working() {
  const [seconds, setSeconds] = useState(0);

  useEffect(() => {
    const started = Date.now();
    const timer = setInterval(() => setSeconds(Math.round((Date.now() - started) / 1000)), 1000);
    return () => clearInterval(timer);
  }, []);

  return (
    <div className="bubble bubble--assistant">
      <span className="muted">
        агент работает
        <span className="cursor" />
        {seconds >= 3 && <span className="working__clock">{seconds} с</span>}
        {seconds >= 45 && (
          <span className="working__note">
            рассуждающая модель думает молча — ход не потеряется, даже если закрыть вкладку
          </span>
        )}
      </span>
    </div>
  );
}
