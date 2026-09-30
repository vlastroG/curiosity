import { useEffect, useMemo, useRef, useState } from 'react';
import { ask } from '../api.js';
import {
  STAGE,
  chapterHue,
  clampSettings,
  fmtMs,
  funnelLine,
  pickExamples,
  readHash,
  routeHash,
  sameSettings,
  sectionsLabel,
  splitCitations,
} from '../format.js';

const STORAGE = 'twain:settings';

function loadSaved() {
  try {
    const v = JSON.parse(localStorage.getItem(STORAGE));
    return v && typeof v === 'object' ? v : null;
  } catch {
    return null;
  }
}

function save(v) {
  try {
    localStorage.setItem(STORAGE, JSON.stringify(v));
  } catch {
    /* не страшно: настройки просто не переживут перезагрузку */
  }
}

// AskView -- вопрос и два ответа по отрывкам из книг: базовый RAG и RAG
// с переписыванием вопроса, порогами и реранкером.
export default function AskView({ route, status, questions, indexReady }) {
  const q = route.params.q || '';
  const t = route.params.t || '';
  const defaults = status?.rag?.defaults;
  const [settings, setSettings] = useState(() => loadSaved());
  const [input, setInput] = useState(q);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  // пока сервер не ответил -- сохранённые или ничего; потом -- с сервера
  const current = settings || (defaults ? clampSettings(defaults) : null);

  useEffect(() => setInput(q), [q]);

  useEffect(() => {
    if (!q || !indexReady || !current) {
      setResult(null);
      return;
    }
    let alive = true;
    setBusy(true);
    setError('');
    setResult(null);
    ask(q, current)
      .then((r) => alive && setResult(r.result))
      .catch((e) => alive && setError(e.message))
      .finally(() => alive && setBusy(false));
    return () => {
      alive = false;
    };
  }, [q, t, indexReady, !!current]);

  const submit = (e) => {
    e.preventDefault();
    const text = input.trim();
    if (text) window.location.hash = routeHash('ask', { q: text, t: text === q ? Date.now() : '' });
  };
  const again = () => {
    window.location.hash = routeHash('ask', { q, t: Date.now() });
  };
  const update = (patch) => {
    const next = clampSettings({ ...current, ...patch });
    setSettings(next);
    save(next);
  };
  const reset = () => {
    setSettings(null);
    save(null);
  };

  const examples = useMemo(() => {
    const inBooks = pickExamples(questions.filter((x) => x.book), 5);
    const off = questions.filter((x) => x.valid && !x.book).slice(0, 2);
    return [...inBooks, ...off];
  }, [questions]);
  const rag = status?.rag || {};
  const books = Object.fromEntries((status?.books || []).map((b) => [b.id, b]));
  const changed = result && current && !sameSettings(result.settings, current);

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

      {examples.length > 0 && (
        <div className="chips" aria-label="Примеры вопросов">
          {examples.map((x) => (
            <a key={x.id} className={`chip ${x.q === q ? 'active' : ''}`} href={routeHash('ask', { q: x.q })}
              title={x.book ? 'Вопрос по книгам' : 'Ответа в книгах нет — хороший фильтр оставит 0 отрывков'}>
              <span className={`book-dot ${x.book ? x.book : 'none'}`} aria-hidden="true" />
              {x.q}
            </a>
          ))}
        </div>
      )}

      {current && (
        <Settings s={current} rag={rag} onChange={update} onReset={reset} custom={!!settings} />
      )}

      {changed && !busy && (
        <div className="banner">
          Настройки изменились после ответа. <button className="link" onClick={again}>Спросить снова с новыми настройками</button>
        </div>
      )}

      {error && <div className="error">{error}</div>}

      {(busy || result) && (
        <div className="answers">
          <section className="answer-col">
            <header className="answer-head">
              <div className="answer-title">Базовый RAG</div>
              <div className="answer-sub">
                вопрос как есть → top-{result?.settings.baseK ?? current?.baseK} по косинусу → модель
              </div>
            </header>
            {busy && !result && <Waiting text="Ищу отрывки и читаю их…" />}
            {result && <Base r={result.base} books={books} />}
          </section>

          <section className="answer-col rag">
            <header className="answer-head">
              <div className="answer-title">RAG с фильтром и реранкером</div>
              <div className="answer-sub">переписанный вопрос → порог косинуса → реранкер → порог → модель</div>
            </header>
            {busy && !result && <Waiting text="Переписываю вопрос, отбираю отрывки…" />}
            {result && <Improved r={result.improved} settings={result.settings} books={books} />}
          </section>
        </div>
      )}
      {result && <div className="timing">модель ответов: {result.model}</div>}
    </div>
  );
}

// ---------- настройки ----------

const QUERY = {
  hyde: 'HyDE — абзац «в духе книги», который мог бы быть ответом',
  en: 'перевод вопроса на английский',
  raw: 'вопрос как есть',
};

function Settings({ s, rag, onChange, onReset, custom }) {
  const num = (key, label, hint, step, min, max) => (
    <label className="setting" title={hint}>
      <span className="setting-label">{label}</span>
      <input
        type="number"
        step={step}
        min={min}
        max={max}
        value={s[key]}
        onChange={(e) => onChange({ [key]: e.target.value })}
      />
      <span className="setting-hint">{hint}</span>
    </label>
  );
  return (
    <section className="settings">
      <div className="settings-row">
        <div className="settings-group">
          <div className="settings-title">С фильтром и реранкером</div>
          <label className="setting" title="Что превращать в вектор для поиска">
            <span className="setting-label">запрос для поиска</span>
            <select value={s.query} onChange={(e) => onChange({ query: e.target.value })}>
              {Object.entries(QUERY).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </select>
            <span className="setting-hint">HyDE и перевод — один вызов модели, кэшируется</span>
          </label>
          {num('kBefore', 'top-K до', 'сколько кандидатов берём из векторного поиска', 1, 1, 50)}
          {num('simMin', 'порог косинуса', 'кандидаты с меньшим сходством отбрасываются сразу', 0.01, 0, 1)}
          {num('relMin', 'порог реранкера', 'оценка реранкера 0–1: ниже — отрывок не про вопрос', 0.05, 0, 1)}
          {num('kAfter', 'top-K после', 'сколько отрывков максимум уходит в модель', 1, 1, 10)}
          <label className="setting" title="Порядок отрывков, прошедших пороги">
            <span className="setting-label">порядок</span>
            <select value={s.order} onChange={(e) => onChange({ order: e.target.value })}>
              <option value="cosine">по косинусу</option>
              <option value="rerank">по реранкеру</option>
              <option value="fused">косинус + реранкер (RRF)</option>
            </select>
            <span className="setting-hint">реранкер только отсекает или ещё и сортирует</span>
          </label>
        </div>
        <div className="settings-group">
          <div className="settings-title">Базовый</div>
          {num('baseK', 'top-K', 'сколько отрывков уходит в модель, без проверки', 1, 1, 10)}
        </div>
        <div className="settings-group readonly">
          <div className="settings-title">Модели</div>
          <div className="setting">
            <span className="setting-label">эмбеддинги</span>
            <code>{rag.embedModel}</code>
          </div>
          <div className="setting">
            <span className="setting-label">реранкер</span>
            <code>{rag.reranker}</code>
          </div>
          <div className="setting">
            <span className="setting-label">ответы</span>
            <code>{rag.model}</code>
          </div>
        </div>
      </div>
      <div className="settings-foot">
        {custom ? 'Свои настройки сохранены в этом браузере.' : rag.tuned ? 'Подобранные значения (experiment).' : 'Значения по умолчанию.'}{' '}
        {custom && (
          <button className="link" onClick={onReset}>
            Сбросить к подобранным
          </button>
        )}
      </div>
    </section>
  );
}

// ---------- ответы ----------

function Waiting({ text }) {
  return (
    <div className="waiting">
      <span className="spinner" aria-hidden="true" />
      {text}
    </div>
  );
}

// useDetails -- раскрывающийся блок и подсветка отрывка по клику на сноску.
function useDetails() {
  const [open, setOpen] = useState(false);
  const [lit, setLit] = useState(0);
  const ref = useRef(null);
  const cite = (n) => {
    setOpen(true);
    setLit(n);
    setTimeout(() => ref.current?.querySelector(`[data-source="${n}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }), 50);
  };
  return { open, setOpen, lit, cite, ref };
}

function AnswerText({ answer, onCite, lit }) {
  if (answer.error) return <div className="error">{answer.error}</div>;
  return (
    <div className="answer-text">
      {answer.text.split(/\n{2,}/).map((para, i) => (
        <p key={i}>
          {splitCitations(para).map((seg, j) =>
            seg.cite ? (
              <button key={j} type="button" className={`cite ${lit === seg.cite ? 'lit' : ''}`} onClick={() => onCite(seg.cite)}>
                {seg.cite}
              </button>
            ) : (
              <span key={j}>{seg.text}</span>
            )
          )}
        </p>
      ))}
    </div>
  );
}

function Base({ r, books }) {
  const d = useDetails();
  return (
    <>
      <AnswerText answer={r} onCite={d.cite} lit={d.lit} />
      <div className="summary">
        {r.sources?.length || 0} отрывков · {(r.ms / 1000).toFixed(1)} с
      </div>
      <details className="how" open={d.open} onToggle={(e) => d.setOpen(e.target.open)}>
        <summary>Как искали</summary>
        <div ref={d.ref}>
          <Steps steps={[['поиск', r.searchMs], ['всего', r.ms]]} />
          <ol className="sources">
            {(r.sources || []).map((s) => (
              <Source key={s.chunkId} s={s} lit={d.lit === s.n} books={books} />
            ))}
          </ol>
        </div>
      </details>
    </>
  );
}

function Improved({ r, settings, books }) {
  const d = useDetails();
  const f = r.funnel;
  const byID = Object.fromEntries((r.sources || []).map((s) => [s.chunkId, s.n]));
  return (
    <>
      <AnswerText answer={r} onCite={d.cite} lit={d.lit} />
      {f && <div className="summary">{funnelLine(f)} · {(r.ms / 1000).toFixed(1)} с</div>}
      <details className="how" open={d.open} onToggle={(e) => d.setOpen(e.target.open)}>
        <summary>Как искали</summary>
        <div ref={d.ref}>
          {settings.query !== 'raw' && r.rewrite?.en && (
            <div className="rewrite">
              <div>
                <span className="control-label">Перевод (его видит реранкер):</span> <q>{r.rewrite.en}</q>
              </div>
              {settings.query === 'hyde' && (
                <div>
                  <span className="control-label">HyDE (по нему искали):</span> <q>{r.rewrite.hyde}</q>
                </div>
              )}
              <div className="muted small">{r.rewritten ? 'переписано моделью' : 'из кэша'}</div>
            </div>
          )}
          <Steps
            steps={[
              ['переписывание', r.rewritten ? r.rewriteMs : 0],
              ['поиск', r.searchMs],
              ['реранкер', f?.rerankMs],
              ['всего', r.ms],
            ]}
          />
          <div className="control-label">
            Кандидаты: top-{settings.kBefore} по косинусу · порог косинуса {settings.simMin} · порог реранкера{' '}
            {settings.relMin} · top-{settings.kAfter} после · порядок: {{ cosine: 'по косинусу', rerank: 'по реранкеру', fused: 'RRF' }[settings.order]}
          </div>
          <div className="table-wrap">
            <table className="funnel">
              <thead>
                <tr>
                  <th>#</th>
                  <th>Книга и глава</th>
                  <th>косинус</th>
                  <th>реранкер</th>
                  <th>итог</th>
                </tr>
              </thead>
              <tbody>
                {(f?.candidates || []).map((c, i) => (
                  <tr key={c.chunkId} className={`stage-${c.stage} ${d.lit && byID[c.chunkId] === d.lit ? 'lit' : ''}`}
                    data-source={byID[c.chunkId] || undefined}>
                    <td>{i + 1}</td>
                    <td>
                      {books[c.book]?.titleRu || c.bookTitle}, {sectionsLabel(c.sections)}
                      <div className="muted small">{c.snippet.slice(0, 120)}…</div>
                    </td>
                    <td className="num">{c.cosine.toFixed(3)}</td>
                    <td className="num">{c.rel === undefined || c.rel === null ? '—' : c.rel.toFixed(2)}</td>
                    <td>
                      <span className={`pill ${STAGE[c.stage]?.tone}`} title={c.reason}>
                        {c.stage === 'kept' ? `[${c.final}] в ответе` : STAGE[c.stage]?.text}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {r.sources?.length > 0 && <div className="control-label">Ушло в модель:</div>}
          <ol className="sources">
            {(r.sources || []).map((s) => (
              <Source key={s.chunkId} s={s} lit={d.lit === s.n} books={books} />
            ))}
          </ol>
          {r.sources?.length === 0 && <p className="muted">Ни один отрывок не прошёл отбор — модель получила пустой контекст.</p>}
        </div>
      </details>
    </>
  );
}

function Steps({ steps }) {
  return (
    <div className="steps-line">
      {steps
        .filter(([, v]) => v !== undefined && v !== null && v >= 1)
        .map(([name, v]) => (
          <span key={name} className="pill">
            {name} {fmtMs(v)}
          </span>
        ))}
    </div>
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
        <span className="source-score">
          {s.rel !== undefined && s.rel !== null && <>реранкер {s.rel.toFixed(2)} · </>}cos {s.cosine.toFixed(3)}
        </span>
      </div>
      <p className="snippet">{s.snippet}</p>
      {first && (
        <a className="open" href={readHash(s.book, s.main ?? first.n, s.chunkId, 'structure')}>
          Открыть в книге →
        </a>
      )}
    </li>
  );
}
