import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { createChat, deleteChat, getChat, listChats, patchChat, putState, sendMessage } from '../api.js';
import { fmtWhen, plural, routeHash } from '../format.js';
import ChatSettings from './ChatSettings.jsx';
import Reply, { Waiting } from './Reply.jsx';
import TaskMemory from './TaskMemory.jsx';

const STARTERS = [
  'Как Твен понимает совесть и мораль — на примере Гека и Джима?',
  'Что Твен думал о цивилизации и прогрессе?',
  'Детство и свобода у Тома Сойера и Гека',
  'Твен о человеческой природе: «Таинственный незнакомец»',
];

// ChatView -- беседы с экспертом: список чатов и диалог с памятью задачи.
export default function ChatView({ route, status, indexReady }) {
  const [chats, setChats] = useState(null);
  const [chat, setChat] = useState(null);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(''); // реплика, на которую ждём ответ
  const [freshSeq, setFreshSeq] = useState(0);
  const [listOpen, setListOpen] = useState(false);
  const id = route.id;

  const reloadList = useCallback(() => {
    return listChats()
      .then((r) => setChats(r.chats))
      .catch((e) => setError(e.message));
  }, []);

  // чаты появляются и из терминала (chat, scenario) -- список обновляется при смене чата
  useEffect(() => {
    reloadList();
  }, [reloadList, id]);

  // без номера в адресе -- последний чат
  useEffect(() => {
    if (!id && chats?.length) window.location.replace(routeHash('chat', {}, chats[0].id));
  }, [id, chats]);

  useEffect(() => {
    setChat(null);
    setFreshSeq(0);
    setListOpen(false);
    if (!id) return;
    let alive = true;
    getChat(id)
      .then((r) => alive && setChat(r.chat))
      .catch((e) => alive && setError(e.message));
    return () => {
      alive = false;
    };
  }, [id]);

  const replace = (c) => {
    setChat(c);
    setChats((list) => {
      const rest = (list || []).filter((x) => x.id !== c.id);
      const last = c.messages?.length ? c.messages[c.messages.length - 1].text.slice(0, 160) : '';
      return [{ ...c, messages: undefined, count: c.messages?.length ?? c.count, last }, ...rest];
    });
  };

  const send = async (text, target = chat) => {
    setError('');
    setPending(text);
    try {
      const r = await sendMessage(target.id, text);
      replace(r.chat);
      setFreshSeq(r.chat.messages.length - 1);
      return true;
    } catch (e) {
      setError(e.message);
      // сервер мог сохранить реплику с ошибкой ответа
      getChat(target.id).then((r) => setChat(r.chat)).catch(() => {});
      return false;
    } finally {
      setPending('');
    }
  };

  const create = async (starter) => {
    setError('');
    try {
      const r = await createChat();
      replace(r.chat);
      window.location.hash = routeHash('chat', {}, r.chat.id);
      if (starter) send(starter, r.chat);
    } catch (e) {
      setError(e.message);
    }
  };

  const remove = async (c) => {
    if (!window.confirm(`Удалить чат «${c.title}» вместе со всей историей? Это нельзя отменить.`)) return;
    try {
      await deleteChat(c.id);
      const rest = (chats || []).filter((x) => x.id !== c.id);
      setChats(rest);
      if (c.id === id) window.location.hash = rest.length ? routeHash('chat', {}, rest[0].id) : routeHash('chat');
    } catch (e) {
      setError(e.message);
    }
  };

  const rag = status?.rag || {};
  const books = Object.fromEntries((status?.books || []).map((b) => [b.id, b]));
  const current = chats?.find((c) => c.id === id);

  return (
    <div className="chat-view">
      <aside className={`chat-list ${listOpen ? 'open' : ''}`}>
        <div className="chat-list-head">
          <button className="chat-list-toggle" onClick={() => setListOpen(!listOpen)} aria-expanded={listOpen}>
            {current?.title || 'Чаты'} <span className="muted small">· {chats?.length || 0} ▾</span>
          </button>
          <button className="btn small primary" onClick={() => create()}>
            + Новый чат
          </button>
        </div>
        <nav className="chat-items">
          {chats?.length === 0 && <div className="muted small empty">Чатов пока нет.</div>}
          {chats?.map((c) => (
            <a key={c.id} className={`chat-item ${c.id === id ? 'active' : ''}`} href={routeHash('chat', {}, c.id)}>
              <span className="chat-item-title">{c.title}</span>
              <span className="muted small">
                {fmtWhen(c.updated)} · {c.count} {plural(c.count, 'сообщение', 'сообщения', 'сообщений')}
              </span>
            </a>
          ))}
        </nav>
      </aside>

      <section className="chat-main">
        {!indexReady && status && (
          <div className="banner">
            Индекс ещё не построен — искать отрывки негде. <a href={routeHash('index')}>Построить на вкладке «Индекс» →</a>
          </div>
        )}
        {rag.error && <div className="error">Модель ответов недоступна: {rag.error}</div>}
        {error && <div className="error">{error}</div>}

        {!id && chats?.length === 0 && <Welcome onStart={create} />}
        {id && !chat && !error && <Waiting text="Загружаю чат…" />}
        {chat && (
          <Dialog
            key={chat.id}
            chat={chat}
            rag={rag}
            books={books}
            pending={pending}
            freshSeq={freshSeq}
            onSend={send}
            onRename={async (title) => replace((await patchChat(chat.id, { title })).chat)}
            onDelete={() => remove(chat)}
            onSettings={async (settings) => replace((await patchChat(chat.id, { settings })).chat)}
            onState={async (state) => replace((await putState(chat.id, state)).chat)}
            onStart={(text) => send(text)}
          />
        )}
      </section>
    </div>
  );
}

function Welcome({ onStart }) {
  return (
    <div className="welcome">
      <h2>Беседа с экспертом по Марку Твену</h2>
      <p className="muted">
        Начните разговор на любую тему — хоть философскую — в контексте книг Твена. Эксперт отвечает по отрывкам из 13 книг,
        ссылается на них и помнит цель беседы.
      </p>
      <div className="chips">
        {STARTERS.map((s) => (
          <button key={s} className="chip" onClick={() => onStart(s)}>
            {s}
          </button>
        ))}
      </div>
      <button className="btn primary" onClick={() => onStart()}>
        + Новый чат
      </button>
    </div>
  );
}

function Dialog({ chat, rag, books, pending, freshSeq, onSend, onRename, onDelete, onSettings, onState, onStart }) {
  const [text, setText] = useState('');
  const [renaming, setRenaming] = useState(null);
  const end = useRef(null);
  const count = chat.messages.length;

  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' });
  }, [count, !!pending]);

  const submit = async (e) => {
    e?.preventDefault();
    const t = text.trim();
    if (!t || pending) return;
    setText('');
    if (!(await onSend(t))) setText(t);
  };
  const rename = async (e) => {
    e.preventDefault();
    await onRename(renaming);
    setRenaming(null);
  };

  return (
    <>
      <header className="chat-head">
        {renaming !== null ? (
          <form className="chat-rename" onSubmit={rename}>
            <input value={renaming} onChange={(e) => setRenaming(e.target.value)} maxLength={120} autoFocus
              placeholder="пусто — название по теме" aria-label="Название чата" />
            <button className="btn small primary" type="submit">
              Сохранить
            </button>
            <button className="link" type="button" onClick={() => setRenaming(null)}>
              Отмена
            </button>
          </form>
        ) : (
          <>
            <h2 className="chat-title">{chat.title}</h2>
            <button className="link" onClick={() => setRenaming(chat.named ? chat.title : '')}>
              переименовать
            </button>
            <button className="link danger" onClick={onDelete}>
              удалить чат
            </button>
          </>
        )}
      </header>

      <ChatSettings chat={chat} rag={rag} onSave={onSettings} />
      <TaskMemory chat={chat} freshSeq={freshSeq} onSave={onState} />

      <div className="messages">
        {count === 0 && !pending && (
          <div className="welcome small">
            <p className="muted">С чего начнём? Например:</p>
            <div className="chips">
              {STARTERS.map((s) => (
                <button key={s} className="chip" onClick={() => onStart(s)}>
                  {s}
                </button>
              ))}
            </div>
          </div>
        )}
        {chat.messages.map((m) => (
          <Fragment key={m.seq}>
            <Message m={m} books={books} compressed={m.seq <= chat.summarizedUpto} />
            {m.seq === chat.summarizedUpto && (
              <details className="compress-divider">
                <summary>выше — сжато в сводку: модель видит её вместо этих сообщений</summary>
                <p className="memory-summary">{chat.summary}</p>
              </details>
            )}
          </Fragment>
        ))}
        {pending && (
          <>
            <div className="msg user">
              <div className="bubble">{pending}</div>
            </div>
            <div className="msg assistant">
              <Waiting text="Ищу в книгах, обновляю память, отвечаю…" />
            </div>
          </>
        )}
        <div ref={end} />
      </div>

      <form className="composer" onSubmit={submit}>
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) submit(e);
          }}
          placeholder={count ? 'Ваша реплика…' : 'О чём поговорим? Например: совесть у Гека Финна'}
          title="Enter — отправить, Shift+Enter — перенос строки"
          rows={2}
          maxLength={2000}
          autoFocus
        />
        <button className="btn primary" type="submit" disabled={!text.trim() || !!pending}>
          {pending ? 'Думаю…' : 'Отправить'}
        </button>
      </form>
    </>
  );
}

function Message({ m, books, compressed }) {
  return (
    <div className={`msg ${m.role} ${compressed ? 'compressed' : ''}`}>
      <div className="msg-meta muted small">
        #{m.seq} · {m.role === 'user' ? 'вы' : 'эксперт'} · {fmtWhen(m.created)}
      </div>
      {m.role === 'user' ? <div className="bubble">{m.text}</div> : <Reply m={m} books={books} />}
    </div>
  );
}
