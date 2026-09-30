import { useEffect, useMemo, useRef, useState } from 'react';
import { getBook, getChunk, getSection, getSectionChunks } from '../api.js';
import { chapterHue, cutMarks, cutStarts, hitMarks, routeHash, segments, shortSection } from '../format.js';

// ReaderView -- читалка: глава из локальной копии книги. При переходе
// из поиска место прокручивается и подсвечивается; «показать нарезку»
// размечает главу чанками выбранного варианта.
export default function ReaderView({ route, status }) {
  const books = status?.books || [];
  const bookID = route.book || books[0]?.id || '';
  const [structure, setStructure] = useState(null);
  const [section, setSection] = useState(null);
  const [hit, setHit] = useState(null);
  const [cuts, setCuts] = useState([]);
  const [error, setError] = useState('');
  const hitRef = useRef(null);

  const n = route.section ?? firstChapter(structure);
  const chunkID = route.params.c || '';
  const hitVariant = route.params.v || '';
  const cutVariant = route.params.cut || '';

  useEffect(() => {
    if (!bookID) return;
    setError('');
    getBook(bookID)
      .then(setStructure)
      .catch((e) => setError(e.message));
  }, [bookID]);

  useEffect(() => {
    if (!bookID || n === null || n === undefined) return;
    setSection(null);
    getSection(bookID, n)
      .then(setSection)
      .catch((e) => setError(e.message));
  }, [bookID, n]);

  useEffect(() => {
    setHit(null);
    if (!chunkID || !hitVariant) return;
    getChunk(hitVariant, chunkID)
      .then((r) => setHit(r.chunk))
      .catch((e) => setError(e.message));
  }, [chunkID, hitVariant]);

  useEffect(() => {
    setCuts([]);
    if (!cutVariant || !bookID || n === null || n === undefined) return;
    getSectionChunks(bookID, n, cutVariant)
      .then((r) => setCuts(r.chunks || []))
      .catch((e) => setError(e.message));
  }, [cutVariant, bookID, n]);

  // прокрутка к найденному месту, когда глава отрисована
  useEffect(() => {
    if (section && hit && hitRef.current) {
      hitRef.current.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }, [section, hit]);

  const marks = useMemo(() => [...cutMarks(cuts), ...(hit?.book === bookID ? hitMarks(hit) : [])], [cuts, hit, bookID]);
  const starts = useMemo(() => cutStarts(cuts), [cuts]);
  const variants = status?.variants || [];
  const book = structure?.book || books.find((b) => b.id === bookID);
  const sections = structure?.sections || [];
  const cur = sections[n];

  // ссылка на главу k с сохранением найденного чанка и нарезки
  const at = (k, params = {}) => routeHash('read', { c: chunkID, v: hitVariant, cut: cutVariant, ...params }, bookID, k);

  if (!bookID) {
    return <div className="empty">Книг в индексе пока нет — постройте индекс на вкладке «Индекс».</div>;
  }

  let firstHitSeen = false;
  const hitOther = hit && hit.book === bookID ? hit.sections.filter((s) => s.n !== n) : [];

  return (
    <div className="reader">
      <aside className="toc">
        <div className="segmented books">
          {books.map((b) => (
            <a key={b.id} className={b.id === bookID ? 'on' : ''} href={routeHash('read', {}, b.id)}>
              {b.titleRu.replace('Приключения ', '')}
            </a>
          ))}
        </div>
        <details className="toc-list" open>
          <summary>Оглавление</summary>
          <ol>
            {sections.map((s) => (
              <li key={s.n}>
                <a
                  className={s.n === n ? 'active' : ''}
                  href={routeHash('read', { cut: cutVariant }, bookID, s.n)}
                  style={{ '--hue': chapterHue(bookID, s.n) }}
                >
                  <span className="toc-label">{shortSection(s)}</span>
                  {s.title && <span className="toc-title">{s.title}</span>}
                </a>
              </li>
            ))}
          </ol>
        </details>
      </aside>

      <article className="page-text">
        <div className="reader-bar">
          <div className="control-group">
            <span className="control-label">Показать нарезку:</span>
            <div className="segmented">
              <a className={!cutVariant ? 'on' : ''} href={at(n, { cut: '' })}>
                нет
              </a>
              {variants
                .filter((v) => v.ready)
                .map((v) => (
                  <a key={v.id} className={cutVariant === v.id ? 'on' : ''} href={at(n, { cut: v.id })} title={v.hint}>
                    {v.title}
                  </a>
                ))}
            </div>
          </div>
          {book && (
            <a className="ext" href={book.url} target="_blank" rel="noreferrer noopener">
              Книга на Project Gutenberg ↗
            </a>
          )}
        </div>

        {error && <div className="error">{error}</div>}

        {hit && hit.book === bookID && (
          <div className="hit-note">
            <span className="legend hit" /> найденный фрагмент <b>{hit.chunkId}</b>
            {hit.start < hit.bodyStart && (
              <>
                {' '}
                · <span className="legend overlap" /> перекрытие с предыдущим чанком
              </>
            )}
            {hitOther.length > 0 && (
              <span className="hit-other">
                {' '}
                · фрагмент захватывает и{' '}
                {hitOther.map((s, i) => (
                  <a key={s.n} href={at(s.n)}>
                    {i > 0 ? ', ' : ''}
                    {shortSection(s)}
                  </a>
                ))}
              </span>
            )}
          </div>
        )}
        {cutVariant && (
          <div className="hit-note">
            Нарезка «{variants.find((v) => v.id === cutVariant)?.title}»: {cuts.length} чанков задевают главу; фон чередуется,
            номер — начало чанка (без перекрытия).
          </div>
        )}

        {cur && (
          <header className="chapter-head">
            <div className="chapter-label">{cur.label}</div>
            {cur.title && <h1 className="chapter-title">{cur.title}</h1>}
            <div className="chapter-book">{book?.title}</div>
          </header>
        )}

        {!section && !error && <div className="empty">загружаю главу…</div>}
        {section?.paras?.map((p) => (
          <p key={p.start} className="para">
            {segments(p, marks).map((seg) => {
              const cls = [];
              let cutIdx = null;
              for (const m of seg.marks) {
                if (m.kind === 'hit') cls.push('hl-hit');
                if (m.kind === 'overlap') cls.push('hl-overlap');
                if (m.kind === 'cut') cls.push(m.parity ? 'cut-b' : 'cut-a');
              }
              if (starts.has(seg.start)) cutIdx = starts.get(seg.start);
              const isFirstHit = cls.includes('hl-hit') && !firstHitSeen;
              if (isFirstHit) firstHitSeen = true;
              return (
                <span key={seg.start} ref={isFirstHit ? hitRef : undefined} className={cls.join(' ')}>
                  {cutIdx !== null && <span className="cut-label">#{cutIdx}</span>}
                  {seg.text}
                </span>
              );
            })}
          </p>
        ))}

        {section && (
          <nav className="pager">
            {n > 0 ? <a href={at(n - 1)}>← {shortSection(sections[n - 1])}</a> : <span />}
            {n + 1 < sections.length ? (
              <a href={at(n + 1)}>{shortSection(sections[n + 1])} →</a>
            ) : (
              <span />
            )}
          </nav>
        )}
      </article>
    </div>
  );
}

function firstChapter(structure) {
  const ss = structure?.sections || [];
  const i = ss.findIndex((s) => /^Chapter /.test(s.label));
  return ss.length ? Math.max(0, i) : null;
}
