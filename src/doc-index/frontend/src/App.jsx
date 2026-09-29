import { useCallback, useEffect, useState } from 'react';
import { getControls, getQuestions, getStatus } from './api.js';
import { parseRoute, routeHash } from './format.js';
import Header from './components/Header.jsx';
import SearchView from './components/SearchView.jsx';
import ReaderView from './components/ReaderView.jsx';
import AskView from './components/AskView.jsx';
import QualityView from './components/QualityView.jsx';
import IndexView from './components/IndexView.jsx';

function useRoute() {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return route;
}

export default function App() {
  const route = useRoute();
  const [status, setStatus] = useState(null);
  const [statusError, setStatusError] = useState('');
  const [questions, setQuestions] = useState([]);
  const [controls, setControls] = useState([]);

  const refresh = useCallback(async () => {
    try {
      const s = await getStatus();
      setStatus(s);
      setStatusError('');
      return s;
    } catch (e) {
      setStatusError(e.message);
      return null;
    }
  }, []);

  const loadQuestions = useCallback(() => {
    getQuestions()
      .then((r) => setQuestions(r.questions || []))
      .catch(() => setQuestions([]));
    getControls()
      .then((r) => setControls(r.controls || []))
      .catch(() => setControls([]));
  }, []);

  useEffect(() => {
    refresh().then((s) => {
      // индекса нет -- первым делом вкладка «Индекс»
      const empty = s && !s.variants?.some((v) => v.ready);
      if (empty && !window.location.hash) window.location.hash = routeHash('index');
    });
    loadQuestions();
  }, [refresh, loadQuestions]);

  // статус меняется и без нас (индексация из CLI) -- обновляем при смене экрана
  useEffect(() => {
    refresh();
  }, [route.view, refresh]);

  const indexReady = !!status?.variants?.some((v) => v.ready);

  // вопросы проверяются по тексту книг: без индекса они все «не подтверждены»
  useEffect(() => {
    if (indexReady) loadQuestions();
  }, [indexReady, loadQuestions]);

  return (
    <div className="app">
      <Header route={route} status={status} error={statusError} />
      <main className="main">
        {route.view === 'ask' && <AskView route={route} status={status} controls={controls} indexReady={indexReady} />}
        {route.view === 'search' && (
          <SearchView route={route} status={status} questions={questions} indexReady={indexReady} />
        )}
        {route.view === 'read' && <ReaderView route={route} status={status} />}
        {route.view === 'quality' && <QualityView status={status} />}
        {route.view === 'index' && (
          <IndexView
            status={status}
            onChanged={() => {
              refresh();
              loadQuestions();
            }}
          />
        )}
      </main>
    </div>
  );
}
