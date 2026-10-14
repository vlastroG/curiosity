import { useEffect, useState } from 'react';
import { api } from './api.js';
import { indexLine } from './format.js';
import Chat from './components/Chat.jsx';
import TicketPanel from './components/TicketPanel.jsx';

const COLLAPSED_KEY = 'support-desk:tickets-collapsed';

function readCollapsed() {
  try {
    return localStorage.getItem(COLLAPSED_KEY) === '1';
  } catch {
    return false;
  }
}

export default function App() {
  const [status, setStatus] = useState(null);
  const [attached, setAttached] = useState(null);
  const [panelOpen, setPanelOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(() => readCollapsed());

  useEffect(() => {
    try {
      localStorage.setItem(COLLAPSED_KEY, collapsed ? '1' : '0');
    } catch {
      // без хранилища панель просто не запомнит состояние
    }
  }, [collapsed]);

  // пока индекс строится -- опрашиваем чаще, чтобы показывать ход
  useEffect(() => {
    let timer;
    let alive = true;
    const poll = async () => {
      let next = 2000;
      try {
        const s = await api.status();
        if (!alive) return;
        setStatus(s);
        if (s.index.state !== 'indexing') next = 30000;
      } catch {
        if (alive) setStatus(null);
      }
      if (alive) timer = setTimeout(poll, next);
    };
    poll();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, []);

  const product = status?.product || 'продукта';
  useEffect(() => {
    document.title = status ? `Поддержка ${status.product}` : 'Поддержка';
  }, [status?.product]);

  return (
    <div className="app">
      <header className="header">
        <h1>Поддержка {status?.product}</h1>
        <div className="muted small">
          {status
            ? [indexLine(status.index), status.model, `защита: ${status.guard.enabled ? 'вкл' : 'выкл'}`].join(' · ')
            : 'Сервер недоступен'}
        </div>
        <button type="button" className="ghost panel-toggle" onClick={() => setPanelOpen((v) => !v)}>
          Тикеты
        </button>
      </header>
      <main className={`layout ${panelOpen ? 'panel-open' : ''} ${collapsed ? 'panel-collapsed' : ''}`}>
        <Chat
          product={product}
          ready={status?.index?.state === 'ready'}
          attached={attached}
          onDetach={() => setAttached(null)}
        />
        <TicketPanel attachedId={attached?.id} onAttach={setAttached} onToggle={() => setCollapsed((v) => !v)} />
      </main>
    </div>
  );
}
