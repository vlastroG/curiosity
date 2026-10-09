import { useRef, useState } from 'react';
import { STAGE, chapterHue, citedNumbers, fmtMs, funnelLine, readHash, sectionsLabel } from '../format.js';
import Markdown from './Markdown.jsx';

export function Waiting({ text }) {
  return (
    <div className="waiting">
      <span className="spinner" aria-hidden="true" />
      {text}
    </div>
  );
}

// Reply -- ответ эксперта: текст со сносками, строка источников (всегда)
// и свёрнутый блок «Как искали». plain -- RAG выключен: только текст.
export default function Reply({ m, books, plain }) {
  const [open, setOpen] = useState(false);
  const [lit, setLit] = useState(0);
  const ref = useRef(null);
  const r = m.result || {};
  const sources = r.sources || [];
  const cited = citedNumbers(m.text);
  const cite = (n) => {
    setOpen(true);
    setLit(n);
    setTimeout(() => ref.current?.querySelector(`[data-source="${n}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }), 50);
  };

  if (plain) {
    return (
      <div className="reply">
        {m.error ? <div className="error">{m.error}</div> : <Markdown text={m.text} />}
      </div>
    );
  }

  return (
    <div className="reply">
      {m.error ? <div className="error">{m.error}</div> : <Markdown text={m.text} onCite={cite} lit={lit} />}
      <div className="sources-line">
        <span className="sources-label">Источники:</span>
        {sources.length === 0 ? (
          <span className="muted">в книгах не нашлось подходящих отрывков</span>
        ) : (
          sources.map((s) => (
            <button key={s.chunkId} type="button" className={`source-chip ${cited.has(s.n) ? '' : 'uncited'}`} onClick={() => cite(s.n)}
              title={cited.has(s.n) ? 'Ответ ссылается на этот отрывок' : 'Отрывок был в контексте, но ответ на него не сослался'}>
              <span className="source-n">{s.n}</span>
              {books[s.book]?.titleRu || s.bookTitle}, {sectionsLabel(s.sections)}
            </button>
          ))
        )}
      </div>
      {sources.length > 0 && cited.size === 0 && !m.error && (
        <div className="muted small">Ответ не сослался на отрывки — они использованы как контекст.</div>
      )}
      <details className="how" open={open} onToggle={(e) => setOpen(e.target.open)}>
        <summary>Как искали</summary>
        <div ref={ref}>
          <How r={r} lit={lit} books={books} />
        </div>
      </details>
    </div>
  );
}

const ORDER = { cosine: 'по косинусу', rerank: 'по реранкеру', fused: 'RRF' };

function How({ r, lit, books }) {
  const f = r.funnel;
  const st = r.settings || {};
  const byID = Object.fromEntries((r.sources || []).map((s) => [s.chunkId, s.n]));
  return (
    <>
      {r.planError && <div className="error">Планировщик не ответил, искали по реплике как есть: {r.planError}</div>}
      {r.rewrite?.en && (
        <div className="rewrite">
          <div>
            <span className="control-label">Запрос с учётом диалога (его видит реранкер):</span> <q>{r.rewrite.en}</q>
          </div>
          {st.query === 'hyde' && (
            <div>
              <span className="control-label">HyDE (по нему искали):</span> <q>{r.rewrite.hyde}</q>
            </div>
          )}
        </div>
      )}
      <div className="steps-line">
        {[
          ['планировщик', r.planMs],
          ['поиск', r.searchMs],
          ['реранкер', f?.rerankMs],
          ['всего', r.ms],
        ]
          .filter(([, v]) => v >= 1)
          .map(([name, v]) => (
            <span key={name} className="pill">
              {name} {fmtMs(v)}
            </span>
          ))}
        {r.model && <span className="pill muted">{r.model}</span>}
      </div>
      {f && (
        <>
          <div className="control-label">
            {funnelLine(f)} · top-{st.kBefore} по косинусу, порог косинуса {st.simMin}, порог реранкера {st.relMin}, top-{st.kAfter} после,{' '}
            {ORDER[st.order]}
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
                {(f.candidates || []).map((c, i) => (
                  <tr key={c.chunkId} className={`stage-${c.stage} ${lit && byID[c.chunkId] === lit ? 'lit' : ''}`}>
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
        </>
      )}
      <ol className="sources">
        {(r.sources || []).map((s) => (
          <Source key={s.chunkId} s={s} lit={lit === s.n} books={books} />
        ))}
      </ol>
      {r.sources?.length === 0 && <p className="muted">Ни один отрывок не прошёл отбор — модель получила пустой контекст.</p>}
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
        <code className="muted small">{s.chunkId}</code>
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
