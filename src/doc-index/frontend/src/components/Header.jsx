import { fmtNum, plural, routeHash } from '../format.js';

const TABS = [
  { view: 'search', title: 'Поиск' },
  { view: 'read', title: 'Книга' },
  { view: 'compare', title: 'Сравнение' },
  { view: 'index', title: 'Индекс' },
];

// gpuLine -- «GPU ✔» / «CPU ✘» / «модель не загружена» по основной модели.
function gpuState(status) {
  const main = status?.variants?.[0]?.model;
  const p = status?.ollama?.models?.[main];
  if (status?.ollama?.error) return { tone: 'bad', text: 'Ollama недоступна' };
  if (!p || !p.loaded) return { tone: 'muted', text: 'GPU: модель не загружена' };
  if (p.sizeVram >= p.size && p.sizeVram > 0) return { tone: 'good', text: 'GPU ✔' };
  return { tone: 'bad', text: 'считает CPU ✘' };
}

export default function Header({ route, status, error }) {
  const ready = status?.variants?.filter((v) => v.ready) || [];
  const chunks = ready.reduce((s, v) => s + v.chunks, 0);
  const gpu = gpuState(status);
  const main = status?.variants?.[0]?.model || '…';

  return (
    <header className="header">
      <div className="header-row">
        <a className="brand" href={routeHash('search')}>
          <span className="brand-mark" aria-hidden="true">
            ≋
          </span>
          <span>
            <span className="brand-title">Твен · поиск по смыслу</span>
            <span className="brand-sub">
              {(status?.books || []).map((b) => b.titleRu).join(' · ') || 'индекс книг Project Gutenberg'}
            </span>
          </span>
        </a>
        <div className="header-status">
          {error ? (
            <span className="pill bad">сервер недоступен</span>
          ) : (
            <>
              <span className="pill">{main}</span>
              <span className={`pill ${gpu.tone}`}>{gpu.text}</span>
              <span className={`pill ${ready.length ? '' : 'warn'}`}>
                {ready.length
                  ? `${ready.length} ${plural(ready.length, 'вариант', 'варианта', 'вариантов')}, ${fmtNum(chunks)} ${plural(chunks, 'чанк', 'чанка', 'чанков')}`
                  : 'индекс не построен'}
              </span>
              {status?.job?.running && <span className="pill live">идёт индексация</span>}
            </>
          )}
        </div>
      </div>
      <nav className="tabs">
        {TABS.map((t) => (
          <a
            key={t.view}
            className={`tab ${route.view === t.view ? 'active' : ''}`}
            href={t.view === 'read' && route.view === 'read' ? window.location.hash : routeHash(t.view)}
          >
            {t.title}
          </a>
        ))}
      </nav>
    </header>
  );
}
