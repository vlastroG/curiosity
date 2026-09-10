import { useCallback, useEffect, useState } from 'react';
import { api, ApiError } from './api.js';
import { ChatList } from './components/ChatList.jsx';
import { ChatView } from './components/ChatView.jsx';
import { SettingsPanel } from './components/SettingsPanel.jsx';

/**
 * Интерфейс агента.
 *
 * Состояние здесь простое, потому что источник правды -- бэкенд: каждый ответ API
 * приносит чат целиком, и компонент просто кладёт его к себе. Никакой параллельной
 * копии истории на клиенте нет, и рассинхронизироваться нечему.
 */
export default function App() {
  const [catalog, setCatalog] = useState(null);
  const [chats, setChats] = useState([]);
  const [activeId, setActiveId] = useState(null);
  const [chat, setChat] = useState(null);
  // состояние окна контекста приезжает вместе с чатом в каждом ответе API
  const [context, setContext] = useState(null);
  const [pending, setPending] = useState(false);
  const [saving, setSaving] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [error, setError] = useState(null);
  const [settingsError, setSettingsError] = useState(null);

  const fail = useCallback((caught) => {
    setError(caught instanceof ApiError ? caught.message : String(caught));
  }, []);

  // стартовая загрузка: каталог моделей и список чатов
  useEffect(() => {
    (async () => {
      try {
        const [loadedCatalog, list] = await Promise.all([api.catalog(), api.listChats()]);
        setCatalog(loadedCatalog);
        setChats(list.chats);
        if (list.chats.length > 0) setActiveId(list.chats[0].id);
      } catch (caught) {
        fail(caught);
      }
    })();
  }, [fail]);

  // подгрузка выбранного чата целиком: в списке истории нет
  useEffect(() => {
    if (!activeId) {
      setChat(null);
      setContext(null);
      return;
    }
    (async () => {
      try {
        const loaded = await api.getChat(activeId);
        setChat(loaded.chat);
        setContext(loaded.context);
      } catch (caught) {
        fail(caught);
      }
    })();
  }, [activeId, fail]);

  /** Обновляет и открытый чат, и его строку в списке -- сводка в списке живая. */
  const applyChat = (updated, updatedContext) => {
    setChat(updated);
    if (updatedContext) setContext(updatedContext);
    setChats((prev) =>
      prev.map((row) =>
        row.id === updated.id
          ? {
              ...row,
              title: updated.title,
              config: updated.config,
              messages: updated.messages.length,
              totalIn: updated.messages.reduce((sum, m) => sum + (m.input?.tokens ?? 0), 0),
              totalOut: updated.messages.reduce(
                (sum, m) => sum + (m.meta?.usage?.completion_tokens ?? 0),
                0
              ),
              totalUsd: updated.messages.reduce((sum, m) => sum + (m.meta?.totalUsd ?? 0), 0),
            }
          : row
      )
    );
  };

  async function handleCreate() {
    setError(null);
    try {
      const created = await api.createChat({ title: `чат ${chats.length + 1}` });
      setChats((prev) => [...prev, summaryOf(created.chat)]);
      setActiveId(created.chat.id);
      setChat(created.chat);
      setContext(created.context);
    } catch (caught) {
      fail(caught);
    }
  }

  async function handleDelete(id) {
    setError(null);
    try {
      await api.deleteChat(id);
      const rest = chats.filter((row) => row.id !== id);
      setChats(rest);
      if (activeId === id) setActiveId(rest.length > 0 ? rest[0].id : null);
    } catch (caught) {
      fail(caught);
    }
  }

  async function handleSend(text) {
    setError(null);
    setPending(true);
    try {
      const result = await api.sendMessage(chat.id, text);
      applyChat(result.chat, result.context);
    } catch (caught) {
      // отказ политики и сбой провайдера уже лежат в ленте: бэкенд прикладывает
      // к таким ошибкам актуальный чат, и показывать отдельную плашку не нужно
      if (caught instanceof ApiError && caught.chat) {
        applyChat(caught.chat, caught.context);
      } else {
        fail(caught);
      }
    } finally {
      setPending(false);
    }
  }

  async function handleClear() {
    setError(null);
    try {
      const result = await api.clearMessages(chat.id);
      applyChat(result.chat, result.context);
    } catch (caught) {
      fail(caught);
    }
  }

  async function handleSaveSettings({ title, config }) {
    setSettingsError(null);
    setSaving(true);
    try {
      const result = await api.patchChat(chat.id, { title, config });
      applyChat(result.chat, result.context);
    } catch (caught) {
      setSettingsError(caught.message);
    } finally {
      setSaving(false);
    }
  }

  if (!catalog) {
    return (
      <div className="app app--boot">
        <span className="muted">
          подключаюсь к агенту
          <span className="cursor" />
        </span>
        {error && <div className="notice notice--failed">{error}</div>}
      </div>
    );
  }

  return (
    <div className="app">
      <header className="app__header">
        <h1>agent sprout</h1>
        <span className="app__note">неделя 2 · день 9 — сжатие истории</span>
        {error && <span className="app__error">{error}</span>}
      </header>

      <div className="app__body">
        <ChatList
          chats={chats}
          activeId={activeId}
          busy={pending}
          onSelect={setActiveId}
          onCreate={handleCreate}
          onDelete={handleDelete}
        />

        {chat ? (
          <>
            <ChatView
              chat={chat}
              context={context}
              pending={pending}
              settingsOpen={settingsOpen}
              onSend={handleSend}
              onClear={handleClear}
              onToggleSettings={() => setSettingsOpen((open) => !open)}
            />
            {settingsOpen && (
              <SettingsPanel
                key={chat.id}
                chat={chat}
                catalog={catalog}
                saving={saving}
                error={settingsError}
                onSave={handleSaveSettings}
                onClose={() => setSettingsOpen(false)}
              />
            )}
          </>
        ) : (
          <main className="chat chat--empty">
            <p className="muted">Выберите чат слева или создайте новый.</p>
          </main>
        )}
      </div>
    </div>
  );
}

/** Строка списка из полного чата -- чтобы не ходить за списком повторно. */
function summaryOf(chat) {
  return {
    id: chat.id,
    title: chat.title,
    config: chat.config,
    messages: chat.messages.length,
    totalIn: 0,
    totalOut: 0,
    totalUsd: 0,
    createdAt: chat.createdAt,
    updatedAt: chat.updatedAt,
  };
}
