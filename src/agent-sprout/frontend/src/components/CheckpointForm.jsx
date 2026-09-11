import { useState } from 'react';

/**
 * Чекпоинт — третья стратегия контекста.
 *
 * Клонирует чат целиком: история, настройки и память переезжают в новую ветку.
 * Активным остаётся исходный чат: ветка создаётся, чтобы к ней вернуться,
 * а не чтобы немедленно в неё уйти.
 */
export function CheckpointForm({ busy, error, onSubmit, onCancel }) {
  const [tag, setTag] = useState('');

  const submit = (event) => {
    event.preventDefault();
    const trimmed = tag.trim();
    if (!trimmed || busy) return;
    onSubmit(trimmed);
  };

  return (
    <form className="checkpoint" onSubmit={submit}>
      <label className="checkpoint__field">
        <span className="field__label">тэг ветки</span>
        <input
          autoFocus
          value={tag}
          placeholder="например: вариант-б"
          onChange={(event) => setTag(event.target.value)}
        />
      </label>

      <span className="field__hint">
        Ветка получит всю историю, настройки и память этого чата. Тэг нужен уникальный —
        по нему ветки различают в списке слева.
      </span>

      {error && <div className="notice notice--failed">{error}</div>}

      <div className="checkpoint__actions">
        <button className="btn" type="submit" disabled={busy || !tag.trim()}>
          {busy ? 'клонирую…' : 'создать ветку'}
        </button>
        <button className="btn btn--ghost" type="button" onClick={onCancel} disabled={busy}>
          отмена
        </button>
      </div>
    </form>
  );
}
