import { chapterHue, fmtNum, readHash, sectionsLabel } from '../format.js';

// ResultCard -- найденное место: сходство, книга и глава, фрагмент, ссылка в читалку.
export default function ResultCard({ hit, variant, expected }) {
  const first = hit.sections?.[0];
  const hue = first ? chapterHue(hit.book, first.n) : 0;
  const crosses = (hit.sections || []).length > 1;
  const title = first?.title;

  return (
    <article className={`card ${expected ? 'expected' : ''}`} style={{ '--hue': hue }}>
      <div className="card-top">
        <span className="rank">{hit.rank}</span>
        <div className="score" title={`косинус ${hit.cosine.toFixed(3)}; сходство (1 + cos) / 2`}>
          <div className="score-bar">
            <div className="score-fill" style={{ width: `${Math.max(0, Math.min(1, hit.score)) * 100}%` }} />
          </div>
          <span className="score-num">{hit.score.toFixed(3)}</span>
        </div>
        {expected && (
          <span className="tick" title="глава из контрольного ответа">
            ✔
          </span>
        )}
      </div>
      <div className="card-where">
        <span className={`book-tag ${hit.book}`}>{hit.book === 'tom' ? 'Том Сойер' : hit.book === 'huck' ? 'Гек Финн' : hit.book}</span>
        <span className="chapter-tag">{sectionsLabel(hit.sections)}</span>
      </div>
      {title && <div className="card-title">{title}</div>}
      <p className="snippet">{hit.snippet}</p>
      <div className="card-flags">
        {crosses && <span className="flag warn">захватывает {sectionsLabel(hit.sections)}</span>}
        {hit.cutStart && <span className="flag">начинается посреди предложения</span>}
        {hit.cutEnd && <span className="flag">обрывается посреди предложения</span>}
        <span className="flag plain">
          {fmtNum(hit.tokens)} ток. · #{hit.chunkId.split('-').pop().replace(/^0+(?=\d)/, '')}
        </span>
      </div>
      {first && (
        <a className="open" href={readHash(hit.book, hit.main ?? first.n, hit.chunkId, variant)}>
          Открыть в книге →
        </a>
      )}
    </article>
  );
}
