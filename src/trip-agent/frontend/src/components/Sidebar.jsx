import { STATUS, formatDate } from '../tools.js';

// История планов и кнопка нового плана.
export default function Sidebar({ runs, currentId, open, onClose }) {
  return (
    <>
      <div className={`scrim ${open ? 'show' : ''}`} onClick={onClose} />
      <aside className={`sidebar ${open ? 'open' : ''}`}>
        <div className="brand">
          <span className="brand-mark">⛵</span> Trip Planner
        </div>
        <a className="btn btn-primary new-btn" href="#/new">
          + Новый план
        </a>
        <div className="history-title">История</div>
        {runs.length === 0 && <p className="history-empty">Здесь появятся ваши планы.</p>}
        <nav className="history">
          {runs.map((run) => {
            const status = STATUS[run.status] || { title: run.status, tone: 'muted' };
            return (
              <a key={run.id} href={`#/runs/${run.id}`} className={`history-item ${run.id === currentId ? 'current' : ''}`}>
                <span className="history-city">{run.city}</span>
                <span className="history-dates">
                  {formatDate(run.startDate)} — {formatDate(run.endDate)}
                </span>
                <span className={`pill tone-${status.tone}`}>
                  {status.title}
                  {run.position ? ` · №${run.position}` : ''}
                </span>
              </a>
            );
          })}
        </nav>
      </aside>
    </>
  );
}
