import { useEffect, useRef, useState } from 'react';
import { ask } from '../api.js';
import { chapterHue, fmtMs, queryModeTitle, readHash, routeHash, sectionsLabel, splitCitations } from '../format.js';

// AskView -- вопрос и два ответа рядом: модель по памяти и модель с отрывками из книг.
export default function AskView({ route, status, controls, indexReady }) {
  const q = route.params.q || '';
  const [input, setInput] = useState(q);
  const [result, setResult] = useState(null);
  const [control, setControl] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [lit, setLit] = useState(0);
  const sourcesRef = useRef(null);

  useEffect(() => setInput(q), [q]);

  useEffect(() => {
    if (!q || !indexReady) {
      setResult(null);
      return;
    }
    let alive = true;
    setBusy(true);
    setError('');
    setResult(null);
    setControl(controls.find((c) => c.q === q) || null);
    ask(q)
      .then((r) => {
        if (!alive) return;
        setResult(r.result);
        if (r.control) setControl(r.control);
      })
      .catch((e) => alive && setError(e.message))
      .finally(() => alive && setBusy(false));
    return () => {
      alive = false;
    };
  }, [q, indexReady]);

  const submit = (e) => {
    e.preventDefault();
    const text = input.trim();
    if (!text) return;
    if (text === q) {
      // тот же вопрос -- спросить ещё раз
      window.location.hash = routeHash('ask', { q: text, t: Date.now() });
    } else {
      window.location.hash = routeHash('ask', { q: text });
    }
  };

  const cite = (n) => {
    setLit(n);
    const el = sourcesRef.current?.querySelector(`[data-source="${n}"]`);
    el?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  };

  const rag = status?.rag || {};
  const books = Object.fromEntries((status?.books || []).map((b) => [b.id, b]));

  return (
    <div className="ask-view">
      {!indexReady && status && (
        <div className="banner">
          Индекс ещё не построен — искать отрывки негде. <a href={routeHash('index')}>Построить на вкладке «Индекс» →</a>
        </div>
      )}
      {rag.error && <div className="error">Модель ответов недоступна: {rag.error}</div>}

      <form className="ask" onSubmit={submit}>
        <input
          className="ask-input"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Спросите о книгах Марка Твена — можно по-русски"
          maxLength={500}
          autoFocus
        />
        <button className="btn primary" type="submit" disabled={!input.trim() || busy}>
          {busy ? 'Думаю…' : 'Спросить'}
        </button>
      </form>

      {controls.length > 0 && (
        <div className="chips" aria-label="Контрольные вопросы">
          {controls
            .filter((c) => c.valid)
            .map((c) => (
              <a key={c.id} className={`chip ${c.q === q ? 'active' : ''}`} href={routeHash('ask', { q: c.q })}
                title="Контрольный вопрос: известно, что должно быть в ответе">
                <span className={`book-dot ${c.book ? '' : 'none'}`} style={c.book ? { '--hue': chapterHue(c.book, 0) } : undefined} aria-hidden="true" />
                {c.q}
              </a>
            ))}
        </div>
      )}

      {control && (
        <div className="expect">
          <b>Ожидание.</b> {control.expected}
          <div className="expect-src">
            {control.book
              ? `Источник: «${books[control.book]?.titleRu || control.book}», гл. ${control.chapters.join(', ')}`
              : 'Источника в книгах нет: правильный ответ — честно это признать.'}
          </div>
        </div>
      )}

      {error && <div className="error">{error}</div>}

      {(busy || result) && (
        <div className="answers">
          <section className="answer-col">
            <header className="answer-head">
              <div className="answer-title">Без RAG</div>
              <div className="answer-sub">модель отвечает по памяти{result?.model ? ` · ${result.model}` : ''}</div>
            </header>
            {busy && !result && <Waiting text="Модель вспоминает…" />}
            {result && <AnswerText answer={result.noRag} />}
          </section>

          <section className="answer-col rag">
            <header className="answer-head">
              <div className="answer-title">С RAG</div>
              <div className="answer-sub">
                ответ по {rag.topK || 5} отрывкам из книг · поиск: {queryModeTitle(rag.retrieval)}
              </div>
            </header>
            {busy && !result && <Waiting text="Ищу отрывки и читаю их…" />}
            {result && (
              <>
                <AnswerText answer={result.rag} onCite={cite} lit={lit} />
                {result.rag.queries?.length > 0 && (
                  <div className="search-query">
                    <span className="control-label">В поиск ушло:</span>
                    {result.rag.queries.map((x, i) => (
                      <q key={i}>{x}</q>
                    ))}
                    <span className="muted">
                      {' '}
                      · поиск {fmtMs(result.rag.searchMs)}
                      {result.rag.rewritten ? ' · вопрос переписан моделью' : result.rag.rewrite?.en ? ' · перевод из кэша' : ''}
                    </span>
                  </div>
                )}
                <ol className="sources" ref={sourcesRef}>
                  {(result.rag.sources || []).map((s) => (
                    <Source key={s.chunkId} s={s} lit={lit === s.n} books={books} />
                  ))}
                </ol>
              </>
            )}
          </section>
        </div>
      )}

      {!q && indexReady && (
        <div className="hint-block">
          <p>
            Один вопрос — два ответа. Слева модель отвечает по памяти. Справа — по отрывкам, которые поиск нашёл в тринадцати
            книгах Твена: вопрос переводится на язык книг, превращается в вектор и сравнивается со всеми отрывками.
          </p>
          <p>Номера в квадратных скобках ведут к отрывкам, каждый открывается в читалке.</p>
        </div>
      )}
    </div>
  );
}

function Waiting({ text }) {
  return (
    <div className="waiting">
      <span className="spinner" aria-hidden="true" />
      {text}
    </div>
  );
}

function AnswerText({ answer, onCite, lit }) {
  if (answer.error) return <div className="error">{answer.error}</div>;
  return (
    <>
      <div className="answer-text">
        {answer.text.split(/\n{2,}/).map((para, i) => (
          <p key={i}>
            {splitCitations(para).map((seg, j) =>
              seg.cite ? (
                <button
                  key={j}
                  type="button"
                  className={`cite ${lit === seg.cite ? 'lit' : ''}`}
                  onClick={() => onCite?.(seg.cite)}
                  disabled={!onCite}
                >
                  {seg.cite}
                </button>
              ) : (
                <span key={j}>{seg.text}</span>
              )
            )}
          </p>
        ))}
      </div>
      <div className="timing">{(answer.ms / 1000).toFixed(1)} с</div>
    </>
  );
}

function Source({ s, lit, books }) {
  const first = s.sections?.[0];
  return (
    <li className={`source ${lit ? 'lit' : ''}`} data-source={s.n} style={{ '--hue': first ? chapterHue(s.book, first.n) : 0 }}>
      <div className="source-head">
        <span className="source-n">{s.n}</span>
        <span className="source-book">{books[s.book]?.titleRu || s.bookTitle}</span>
        <span className="chapter-tag">{sectionsLabel(s.sections)}</span>
        <span className="source-score" title={`косинус ${s.cosine.toFixed(3)}`}>
          {s.score.toFixed(3)}
        </span>
      </div>
      {first?.title && <div className="card-title">{first.title}</div>}
      <p className="snippet">{s.snippet}</p>
      {first && (
        <a className="open" href={readHash(s.book, s.main ?? first.n, s.chunkId, s.chunkId.split('-')[0])}>
          Открыть в книге →
        </a>
      )}
    </li>
  );
}
