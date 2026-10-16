import { useCallback, useEffect, useState } from 'react';
import { api } from '../api.js';
import { STATUSES, statusLabel } from '../format.js';

// TicketPanel -- тикеты для отладки: создать, сменить статус, прикрепить к вопросу.
// На широком экране панель сворачивается в узкую полосу (onToggle); на телефоне её
// показывает кнопка «Тикеты» в шапке.
export default function TicketPanel({ attachedId, onAttach, onToggle }) {
  const [filter, setFilter] = useState('');
  const [list, setList] = useState([]);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [error, setError] = useState('');

  const load = useCallback(() => {
    api.tickets(filter).then(setList, (err) => setError(err.message));
  }, [filter]);
  // эффект не должен ничего возвращать, кроме функции очистки
  useEffect(() => {
    load();
  }, [load]);

  async function create(e) {
    e.preventDefault();
    setError('');
    try {
      await api.createTicket(title, description);
      setTitle('');
      setDescription('');
      load();
    } catch (err) {
      setError(err.message);
    }
  }

  async function update(id, patch) {
    setError('');
    try {
      await api.updateTicket(id, patch);
      load();
    } catch (err) {
      setError(err.message);
    }
  }

  return (
    <aside className="tickets">
      <button type="button" className="ghost rail" onClick={onToggle} aria-label="Показать тикеты">
        Тикеты{list.length ? ` · ${list.length}` : ''}
      </button>
      <div className="tickets-body">
        <form className="ticket-form" onSubmit={create}>
          <div className="tickets-top">
            <h2>Новый тикет</h2>
            <button type="button" className="ghost collapse-btn" onClick={onToggle} aria-label="Свернуть панель тикетов">
              Свернуть
            </button>
          </div>
          <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Заголовок" maxLength={200} />
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Описание"
            maxLength={5000}
            rows={3}
          />
          <button type="submit" disabled={!title.trim() || !description.trim()}>
            Создать тикет
          </button>
        </form>
        {error && <div className="error small">{error}</div>}
        <div className="ticket-filter">
          <h2>Тикеты</h2>
          <select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Фильтр по статусу">
            <option value="">все</option>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {statusLabel(s)}
              </option>
            ))}
          </select>
        </div>
        {list.length === 0 && <p className="muted small">Тикетов нет.</p>}
        <ul className="ticket-list">
          {list.map((t) => (
            <Ticket key={t.id} t={t} attached={attachedId === t.id} onAttach={onAttach} onUpdate={update} />
          ))}
        </ul>
      </div>
    </aside>
  );
}

function Ticket({ t, attached, onAttach, onUpdate }) {
  const [resolution, setResolution] = useState(t.resolution);
  useEffect(() => {
    setResolution(t.resolution);
  }, [t.resolution]);

  return (
    <li className={`ticket ${attached ? 'attached' : ''}`}>
      <div className="ticket-head">
        <span className="ticket-id">#{t.id}</span>
        <span className={`status ${t.status}`}>{statusLabel(t.status)}</span>
      </div>
      <div className="ticket-title">{t.title}</div>
      <div className="ticket-desc">{t.description}</div>
      <div className="ticket-actions">
        <select value={t.status} onChange={(e) => onUpdate(t.id, { status: e.target.value })} aria-label="Статус">
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {statusLabel(s)}
            </option>
          ))}
        </select>
        <button type="button" className="ghost" onClick={() => onAttach(attached ? null : t)}>
          {attached ? 'Открепить' : 'Прикрепить к вопросу'}
        </button>
      </div>
      <div className="ticket-resolution">
        <input value={resolution} onChange={(e) => setResolution(e.target.value)} placeholder="Решение" />
        {resolution !== t.resolution && (
          <button type="button" className="ghost" onClick={() => onUpdate(t.id, { resolution })}>
            Сохранить
          </button>
        )}
      </div>
    </li>
  );
}
