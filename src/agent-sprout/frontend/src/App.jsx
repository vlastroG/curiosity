import { useCallback, useEffect, useState } from 'react';
import { api, ApiError } from './api.js';
import { ChatList } from './components/ChatList.jsx';
import { ChatView } from './components/ChatView.jsx';
import { KnowledgePanel } from './components/KnowledgePanel.jsx';
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
  // форма чекпоинта живёт здесь, а не в ChatView: ошибку занятого тэга приносит API,
  // и показать её надо прямо в форме
  const [checkpointOpen, setCheckpointOpen] = useState(false);
  const [checkpointBusy, setCheckpointBusy] = useState(false);
  const [checkpointError, setCheckpointError] = useState(null);
  // долговременная память общая для всех чатов, поэтому живёт на уровне приложения
  const [knowledge, setKnowledge] = useState([]);
  const [knowledgePresets, setKnowledgePresets] = useState([]);
  const [knowledgeOpen, setKnowledgeOpen] = useState(false);
  const [knowledgeBusy, setKnowledgeBusy] = useState(false);
  const [knowledgeError, setKnowledgeError] = useState(null);
  const [taskBusy, setTaskBusy] = useState(false);
  const [error, setError] = useState(null);
  const [settingsError, setSettingsError] = useState(null);

  const fail = useCallback((caught) => {
    setError(caught instanceof ApiError ? caught.message : String(caught));
  }, []);

  // стартовая загрузка: каталог моделей и список чатов
  useEffect(() => {
    (async () => {
      try {
        const [loadedCatalog, list, memory] = await Promise.all([
          api.catalog(),
          api.listChats(),
          api.knowledge(),
        ]);
        setCatalog(loadedCatalog);
        setChats(list.chats);
        setKnowledge(memory.knowledge ?? []);
        setKnowledgePresets(memory.presets ?? []);
        if (list.chats.length > 0) setActiveId(list.chats[0].id);
      } catch (caught) {
        fail(caught);
      }
    })();
  }, [fail]);

  // подгрузка выбранного чата целиком: в списке истории нет
  useEffect(() => {
    setCheckpointOpen(false);
    setCheckpointError(null);

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
              tasks: updated.tasks?.length ?? 0,
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

  function toggleCheckpoint() {
    setCheckpointError(null);
    setCheckpointOpen((open) => !open);
  }

  async function handleCheckpoint(tag) {
    setCheckpointError(null);
    setCheckpointBusy(true);
    try {
      const created = await api.checkpoint(chat.id, tag);
      // ветка появляется в списке, но активным остаётся исходный чат:
      // чекпоинт делают, чтобы было куда вернуться, а не чтобы уйти прямо сейчас
      setChats((prev) => [...prev, summaryOf(created.chat)]);
      setCheckpointOpen(false);
    } catch (caught) {
      setCheckpointError(caught.message);
    } finally {
      setCheckpointBusy(false);
    }
  }

  async function handleCancelTask() {
    setError(null);
    setTaskBusy(true);
    try {
      const result = await api.cancelTask(chat.id);
      applyChat(result.chat, result.context);
    } catch (caught) {
      fail(caught);
    } finally {
      setTaskBusy(false);
    }
  }

  // --- долговременная память ---

  async function withKnowledge(action, done) {
    setKnowledgeError(null);
    setKnowledgeBusy(true);
    try {
      await action();
      const memory = await api.knowledge();
      setKnowledge(memory.knowledge ?? []);
      setKnowledgePresets(memory.presets ?? []);
      done?.();
    } catch (caught) {
      setKnowledgeError(caught.message);
    } finally {
      setKnowledgeBusy(false);
    }
  }

  const handleAddKnowledge = (body, done) => withKnowledge(() => api.addKnowledge(body), done);
  const handleUpdateKnowledge = (id, body, done) =>
    withKnowledge(() => api.updateKnowledge(id, body), done);
  const handleDeleteKnowledge = (id) => withKnowledge(() => api.deleteKnowledge(id));

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
        <span className="app__note">помощник строителя · день 11 — модель памяти</span>
        {error && <span className="app__error">{error}</span>}
        <button
          className="btn btn--ghost app__knowledge"
          onClick={() => setKnowledgeOpen((open) => !open)}
          title="долговременная память: знания, общие для всех чатов"
        >
          знания {knowledge.length > 0 ? `(${knowledge.length})` : ''}
        </button>
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
              checkpoint={{
                open: checkpointOpen,
                busy: checkpointBusy,
                error: checkpointError,
                toggle: toggleCheckpoint,
              }}
              task={{
                active: chat.tasks?.find((item) => item.status === 'collecting') ?? null,
                solved: (chat.tasks ?? []).filter((item) => item.status === 'done' && item.summary),
                knowledge: knowledgeFor(chat, knowledge),
                busy: taskBusy,
              }}
              onSend={handleSend}
              onClear={handleClear}
              onToggleSettings={() => setSettingsOpen((open) => !open)}
              onCheckpoint={handleCheckpoint}
              onCancelTask={handleCancelTask}
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
        {knowledgeOpen && (
          <KnowledgePanel
            items={knowledge}
            presets={knowledgePresets}
            busy={knowledgeBusy}
            error={knowledgeError}
            onCreate={handleAddKnowledge}
            onUpdate={handleUpdateKnowledge}
            onDelete={handleDeleteKnowledge}
            onClose={() => setKnowledgeOpen(false)}
          />
        )}
      </div>
    </div>
  );
}

/**
 * Знания, отобранные под активную задачу чата.
 *
 * Задача хранит только идентификаторы: тексты знаний могут поменяться, и показывать
 * надо актуальные, а не копию на момент отбора.
 */
function knowledgeFor(chat, knowledge) {
  const active = chat.tasks?.find((item) => item.status === 'collecting');
  if (!active?.knowledgeIds?.length) return [];
  return knowledge.filter((item) => active.knowledgeIds.includes(item.id));
}

/** Строка списка из полного чата -- чтобы не ходить за списком повторно. */
function summaryOf(chat) {
  return {
    id: chat.id,
    title: chat.title,
    config: chat.config,
    messages: chat.messages.length,
    tasks: chat.tasks?.length ?? 0,
    tag: chat.tag,
    clonedAt: chat.clonedAt,
    parentId: chat.parentId,
    totalIn: 0,
    totalOut: 0,
    totalUsd: 0,
    createdAt: chat.createdAt,
    updatedAt: chat.updatedAt,
  };
}
