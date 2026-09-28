import { useEffect, useMemo, useState } from 'react';
import { search } from '../api.js';
import { fmtMs, pickExamples, placeLabel, placeTone, routeHash, toggleVariant } from '../format.js';
import ResultCard from './ResultCard.jsx';

const DEFAULT_VARIANTS = ['fixed', 'structure', 'semantic'];

function loadVariants() {
  try {
    const v = JSON.parse(localStorage.getItem('doc-index:variants'));
    if (Array.isArray(v) && v.length) return v;
  } catch {
    /* нет хранилища -- берём по умолчанию */
  }
  return DEFAULT_VARIANTS;
}

function saveVariants(v) {
  try {
    localStorage.setItem('doc-index:variants', JSON.stringify(v));
  } catch {
    /* не страшно */
  }
}

export default function SearchView({ route, status, questions, indexReady }) {
  const q = route.params.q || '';
  const qid = route.params.qid || '';
  const bookFilter = route.params.b || '';
  const allVariants = status?.variants || [];
  const [selected, setSelected] = useState(loadVariants);
  const [input, setInput] = useState(q);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  // только существующие варианты, в порядке их описания
  const variants = useMemo(() => {
    const ids = allVariants.map((v) => v.id);
    const s = selected.filter((id) => ids.includes(id));
    return s.length ? ids.filter((id) => s.includes(id)) : ids.slice(0, 3);
  }, [allVariants, selected]);

  const question = questions.find((x) => x.id === qid && x.q === q);
  const examples = useMemo(() => pickExamples(questions, 8), [questions]);

  useEffect(() => setInput(q), [q]);

  useEffect(() => {
    if (!q || !indexReady || variants.length === 0) {
      setResult(null);
      return;
    }
    let alive = true;
    setBusy(true);
    setError('');
    search(q, variants, bookFilter, 5)
      .then((r) => alive && setResult(r))
      .catch((e) => alive && setError(e.message))
      .finally(() => alive && setBusy(false));
    return () => {
      alive = false;
    };
  }, [q, bookFilter, variants.join(','), indexReady]);

  const go = (params) => {
    window.location.hash = routeHash('search', { q, qid, b: bookFilter, ...params });
  };
  const submit = (e) => {
    e.preventDefault();
    const text = input.trim();
    if (text) go({ q: text, qid: text === question?.q ? qid : '' });
  };
  const toggle = (id) => {
    const next = toggleVariant(variants, id, 3);
    setSelected(next);
    saveVariants(next);
  };

  const books = status?.books || [];
  const embedMs = result ? Object.values(result.embedMs || {}).reduce((a, b) => a + b, 0) : 0;

  return (
    <div className="search">
      {!indexReady && status && (
        <div className="banner">
          Индекс ещё не построен — поиску не по чему искать.{' '}
          <a href={routeHash('index')}>Построить на вкладке «Индекс» →</a>
        </div>
      )}

      <form className="ask" onSubmit={submit}>
        <input
          className="ask-input"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Спросите что-нибудь о книгах — можно по-русски"
          maxLength={500}
          autoFocus
        />
        <button className="btn primary" type="submit" disabled={!input.trim() || busy}>
          {busy ? 'Ищу…' : 'Найти'}
        </button>
      </form>

      {examples.length > 0 && (
        <div className="chips" aria-label="Примеры вопросов">
          {examples.map((x) => (
            <a
              key={x.id}
              className={`chip ${x.id === qid ? 'active' : ''}`}
              href={routeHash('search', { q: x.q, qid: x.id, b: bookFilter })}
              title="Контрольный вопрос: для него известна правильная глава"
            >
              <span className={`book-dot ${x.book}`} aria-hidden="true" />
              {x.q}
            </a>
          ))}
        </div>
      )}

      <div className="controls">
        <div className="control-group">
          <span className="control-label">Варианты индекса, до трёх:</span>
          <div className="toggles">
            {allVariants.map((v) => (
              <button
                key={v.id}
                type="button"
                className={`toggle ${variants.includes(v.id) ? 'on' : ''}`}
                onClick={() => toggle(v.id)}
                disabled={!v.ready}
                title={v.hint}
              >
                <span className="toggle-title">{v.title}</span>
                <span className="toggle-hint">{v.hint}</span>
              </button>
            ))}
          </div>
        </div>
        <div className="control-group">
          <span className="control-label">Где искать:</span>
          <div className="segmented">
            <button type="button" className={!bookFilter ? 'on' : ''} onClick={() => go({ b: '' })}>
              Все книги
            </button>
            {books.map((b) => (
              <button key={b.id} type="button" className={bookFilter === b.id ? 'on' : ''} onClick={() => go({ b: b.id })}>
                {b.titleRu.replace('Приключения ', '')}
              </button>
            ))}
          </div>
        </div>
      </div>

      {question && (
        <div className="expect">
          Контрольный вопрос: ответ в книге «{books.find((b) => b.id === question.book)?.titleRu || question.book}», глава{' '}
          {question.chapters.join(', ')}. Карточки из этой главы отмечены ✔.
        </div>
      )}

      {error && <div className="error">{error}</div>}

      {result && (
        <>
          <div className="columns" style={{ '--cols': variants.length }}>
            {variants.map((id) => {
              const v = allVariants.find((x) => x.id === id);
              const hits = result.results?.[id] || [];
              const rank = question ? expectedRank(hits, question) : null;
              return (
                <section key={id} className="column">
                  <header className="column-head">
                    <div className="column-title">{v?.title}</div>
                    <div className="column-model">{v?.model}</div>
                    {question && (
                      <div className={`place ${placeTone(rank)}`}>
                        {rank ? placeLabel(rank) : 'нужной главы нет в первой пятёрке'}
                      </div>
                    )}
                  </header>
                  {hits.length === 0 && <div className="empty">ничего не найдено</div>}
                  {hits.map((h) => (
                    <ResultCard key={h.chunkId} hit={h} variant={id} expected={question && isExpected(h, question)} />
                  ))}
                </section>
              );
            })}
          </div>
          <div className="timing">
            эмбеддинг вопроса {embedMs ? fmtMs(embedMs) : 'из кэша'} · сравнение с векторами {fmtMs(result.rankMs)}
          </div>
        </>
      )}

      {!q && indexReady && (
        <div className="hint-block">
          <p>
            Вопрос превращается в вектор той же моделью, что и чанки книг, и сравнивается со всеми чанками обеих книг сразу.
            Книгу указывать не нужно: наверх поднимаются ближайшие по смыслу места.
          </p>
          <p>
            Каждая колонка — отдельный вариант индекса. Одинаковые главы подсвечены одинаковым цветом, так видно, кто нашёл
            то же место и выше ли.
          </p>
        </div>
      )}
    </div>
  );
}

function isExpected(hit, question) {
  if (hit.book !== question.book) return false;
  const want = question.chapters.map((c) => c.toUpperCase());
  return (hit.sections || []).some((s) => want.includes(s.key));
}

function expectedRank(hits, question) {
  const h = hits.find((x) => isExpected(x, question));
  return h ? h.rank : 0;
}
