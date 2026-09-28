import { useCallback, useEffect, useState } from 'react';
import { getMeta, listRuns } from './api.js';
import Sidebar from './components/Sidebar.jsx';
import NewPlan from './components/NewPlan.jsx';
import RunView from './components/RunView.jsx';

// Маршруты -- в адресе после #: #/new -- форма, #/runs/<id> -- прогон.
// Адрес можно сохранить в закладки и открыть снова: история лежит на сервере.
function parseRoute() {
  const hash = window.location.hash.replace(/^#/, '');
  const match = hash.match(/^\/runs\/(run_[0-9a-f]{10})$/);
  return match ? { page: 'run', id: match[1] } : { page: 'new' };
}

export default function App() {
  const [route, setRoute] = useState(parseRoute);
  const [meta, setMeta] = useState(null);
  const [runs, setRuns] = useState([]);
  const [menuOpen, setMenuOpen] = useState(false);

  useEffect(() => {
    const onHash = () => {
      setRoute(parseRoute());
      setMenuOpen(false);
    };
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  const refreshRuns = useCallback(() => {
    listRuns().then(setRuns).catch(() => {});
  }, []);

  useEffect(() => {
    getMeta().then(setMeta).catch(() => {});
    refreshRuns();
    const timer = setInterval(refreshRuns, 5000);
    return () => clearInterval(timer);
  }, [refreshRuns]);

  return (
    <div className="app">
      <Sidebar runs={runs} currentId={route.id} open={menuOpen} onClose={() => setMenuOpen(false)} />
      <main className="main">
        <button className="menu-btn" onClick={() => setMenuOpen(true)} aria-label="История планов">
          ☰ История
        </button>
        {route.page === 'run' ? (
          <RunView key={route.id} id={route.id} onChange={refreshRuns} />
        ) : (
          <NewPlan meta={meta} onCreated={(run) => {
            refreshRuns();
            window.location.hash = `/runs/${run.id}`;
          }} />
        )}
      </main>
    </div>
  );
}
