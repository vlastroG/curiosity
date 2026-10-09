import { useEffect, useState } from 'react';
import { clampSettings, fmtNum, settingsLine } from '../format.js';

const QUERY = {
  hyde: 'HyDE — абзац «в духе книги», который мог бы быть ответом',
  en: 'запрос по-английски с учётом диалога',
  raw: 'реплика как есть',
};

// ChatSettings -- свёрнутая панель настроек чата: модели (только чтение,
// задаются в .env), поиск, генерация и сжатие (сохраняются в чат на сервере).
export default function ChatSettings({ chat, rag, onSave }) {
  const [draft, setDraft] = useState(chat.settings);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => setDraft(chat.settings), [chat.id, chat.settings]);

  const set = (patch) => setDraft((d) => ({ ...d, ...patch }));
  const dirty = JSON.stringify(clampSettings(draft)) !== JSON.stringify(clampSettings(chat.settings));
  const save = async (s) => {
    setBusy(true);
    setError('');
    try {
      await onSave(clampSettings(s));
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const num = (key, label, hint, step, min, max) => (
    <label className="setting" title={hint}>
      <span className="setting-label">{label}</span>
      <input type="number" step={step} min={min} max={max} value={draft[key]} onChange={(e) => set({ [key]: e.target.value })} />
      <span className="setting-hint">{hint}</span>
    </label>
  );
  // необязательное число: пустое поле -- значение модели (null или 0 на сервере)
  const opt = (key, label, hint, step, min, max, placeholder) => (
    <label className="setting" title={hint}>
      <span className="setting-label">{label}</span>
      <input
        type="number"
        step={step}
        min={min}
        max={max}
        placeholder={placeholder}
        value={draft[key] === null || draft[key] === undefined || draft[key] === 0 ? '' : draft[key]}
        onChange={(e) => set({ [key]: e.target.value })}
      />
      <span className="setting-hint">{hint}</span>
    </label>
  );

  const local = rag.local ? rag.localInfo || {} : null;
  const off = !!rag.off; // RAG выключен: ни поиска, ни эмбеддингов

  return (
    <details className="settings chat-settings">
      <summary>
        <span className="settings-summary-title">Настройки чата</span>
        <span className="muted small">{settingsLine(chat.settings, off)}</span>
      </summary>
      <div className="settings-row">
        <div className="settings-group readonly">
          <div className="settings-title">Модели · задаются в .env</div>
          <div className="setting wide">
            <span className="setting-label">{off ? 'ответы и сжатие (RAG_MODEL)' : 'ответы, план, сжатие (RAG_MODEL)'}</span>
            <code>{local?.model || rag.model || 'не настроена'}</code>
            {local && !local.error && local.ready && (
              <span className="setting-hint">
                локально через llmcli · квантование {local.quantization} · {local.parameterSize} параметров · контекст до{' '}
                {fmtNum(local.contextLength)} токенов
              </span>
            )}
            {local && !local.error && !local.ready && <span className="setting-hint">llmcli: модель ещё скачивается</span>}
            {local?.error && <span className="setting-hint bad-text">{local.error}</span>}
            {rag.budget > 0 && <span className="setting-hint">бюджет вывода {fmtNum(rag.budget)} токенов</span>}
          </div>
          {off ? (
            <div className="setting wide">
              <span className="setting-label">поиск по книгам</span>
              <code>выключен (RAG=off)</code>
            </div>
          ) : (
            <>
              <div className="setting">
                <span className="setting-label">эмбеддинги</span>
                <code>{rag.embedModel}</code>
              </div>
              <div className="setting">
                <span className="setting-label">реранкер</span>
                <code>{rag.reranker}</code>
              </div>
            </>
          )}
        </div>
        {!off && (
          <div className="settings-group">
            <div className="settings-title">Поиск</div>
            <label className="setting wide" title="Что превращать в вектор для поиска">
              <span className="setting-label">запрос для поиска</span>
              <select value={draft.query} onChange={(e) => set({ query: e.target.value })}>
                {Object.entries(QUERY).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </label>
            {num('kBefore', 'top-K до', 'кандидатов из векторного поиска', 1, 1, 50)}
            {num('simMin', 'порог косинуса', 'меньшее сходство отбрасывается сразу', 0.01, 0, 1)}
            {num('relMin', 'порог реранкера', 'оценка 0–1: ниже — отрывок не про вопрос', 0.05, 0, 1)}
            {num('kAfter', 'top-K после', 'сколько отрывков максимум уходит в модель', 1, 1, 10)}
            <label className="setting" title="Порядок отрывков, прошедших пороги">
              <span className="setting-label">порядок</span>
              <select value={draft.order} onChange={(e) => set({ order: e.target.value })}>
                <option value="cosine">по косинусу</option>
                <option value="rerank">по реранкеру</option>
                <option value="fused">косинус + реранкер (RRF)</option>
              </select>
            </label>
          </div>
        )}
        <div className="settings-group">
          <div className="settings-title">Генерация</div>
          {opt('temperature', 'температура', '0 — точнее и однообразнее, 1–2 — разнообразнее; пусто — значение модели', 0.1, 0, 2, 'модели')}
          {opt(
            'maxTokens',
            'предел ответа',
            `токенов на один ответ модели; пусто — бюджет ${rag.budget ? fmtNum(rag.budget) : 'модели'}`,
            100,
            16,
            100000,
            'бюджет'
          )}
          {local ? (
            opt(
              'ctx',
              'контекстное окно',
              'смена окна перезагружает модель в Ollama; меньше — быстрее и помещается в видеопамять',
              1024,
              2048,
              local.contextLength || 262144,
              local.numCtx ? fmtNum(local.numCtx) : 'llmcli'
            )
          ) : (
            <div className="setting">
              <span className="setting-label">контекстное окно</span>
              <code>{rag.context ? `${fmtNum(rag.context)} токенов` : 'неизвестно'}</code>
              <span className="setting-hint">задаётся провайдером</span>
            </div>
          )}
        </div>
        <div className="settings-group">
          <div className="settings-title">Память</div>
          {num('compressAfter', 'сжимать после', 'когда несжатых сообщений больше — старые уходят в сводку, последние 4 остаются', 1, 4, 50)}
        </div>
      </div>
      <div className="settings-foot">
        <button className="btn small primary" disabled={!dirty || busy} onClick={() => save(draft)}>
          {busy ? 'Сохраняю…' : 'Сохранить'}
        </button>
        {dirty && (
          <button className="link" onClick={() => setDraft(chat.settings)}>
            Отменить
          </button>
        )}
        {rag.defaults && (
          <button className="link" disabled={busy} onClick={() => save(rag.defaults)}>
            {rag.tuned ? 'Вернуть подобранные (experiment)' : 'Вернуть значения по умолчанию'}
          </button>
        )}
        {error && <span className="bad-text">{error}</span>}
      </div>
    </details>
  );
}
