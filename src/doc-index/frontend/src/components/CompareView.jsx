import { useEffect, useState } from 'react';
import { getCompare } from '../api.js';
import { best, fmtNum, fmtSeconds, pct, placeTone, routeHash } from '../format.js';

// Метрики таблицы: как посчитать, как показать, что лучше, человеческая подсказка.
const METRICS = [
  { group: 'Поиск по контрольным вопросам' },
  { key: 'hit1', title: 'hit@1', hint: 'нужная глава на первом месте', get: (v) => v.all.hit1, fmt: pct },
  { key: 'hit3', title: 'hit@3', hint: 'нужная глава в первой тройке', get: (v) => v.all.hit3, fmt: pct },
  { key: 'hit5', title: 'hit@5', hint: 'нужная глава в первой пятёрке', get: (v) => v.all.hit5, fmt: pct },
  { key: 'mrr', title: 'MRR@10', hint: 'в среднем 1/место нужной главы: 1 — всегда первая, 0.5 — в среднем вторая', get: (v) => v.all.mrr, fmt: (x) => x.toFixed(2) },
  { key: 'book1', title: 'книга на 1-м месте', hint: 'первое место — из правильной книги (книгу в вопросе не указывали)', get: (v) => v.all.book1, fmt: pct },
  { key: 'ru', title: 'hit@1, русские вопросы', hint: 'вопрос по-русски к английскому тексту', get: (v) => v.byLang?.ru?.hit1, fmt: pct },
  { key: 'en', title: 'hit@1, английские вопросы', hint: 'вопрос на языке книги', get: (v) => v.byLang?.en?.hit1, fmt: pct },
  { group: 'Форма чанков' },
  { key: 'chunks', title: 'чанков', hint: 'на обе книги', get: (v) => v.shape.chunks, fmt: fmtNum, neutral: true },
  { key: 'p50', title: 'токенов, медиана', hint: 'min … max в скобках', get: (v) => v.shape.p50, fmt: (x, v) => `${x} (${v.shape.min}…${v.shape.max})`, neutral: true },
  { key: 'inRange', title: 'в диапазоне 500–1000', hint: 'рекомендованный размер чанка', get: (v) => v.shape.inRange, fmt: pct },
  { key: 'overlap', title: 'перекрытие', hint: 'сколько токенов проиндексировано повторно', get: (v) => v.shape.overlapOverhead, fmt: (x) => `+${pct(x)}`, lower: true },
  { group: 'Целостность' },
  { key: 'cross', title: 'захватывают две главы', hint: 'чанк начинается в одной главе, кончается в другой', get: (v) => v.shape.crossChapter, fmt: fmtNum, lower: true },
  { key: 'cutStart', title: 'начинаются посреди предложения', get: (v) => v.shape.cutStart, fmt: fmtNum, lower: true },
  { key: 'cutEnd', title: 'обрываются посреди предложения', get: (v) => v.shape.cutEnd, fmt: fmtNum, lower: true },
  { group: 'Эмбеддинги' },
  { key: 'model', title: 'модель · размерность', get: (v) => `${v.model} · ${v.dims}`, fmt: (x) => x, neutral: true },
  { key: 'time', title: 'время эмбеддинга чанков', hint: 'на видеокарте, обе книги', get: (v) => v.embedSeconds, fmt: fmtSeconds, neutral: true },
  { key: 'total', title: 'время варианта целиком', hint: 'нарезка и эмбеддинги; у «По смыслу» сюда входят эмбеддинги всех предложений', get: (v) => v.totalSeconds, fmt: fmtSeconds, neutral: true },
];

export default function CompareView({ indexReady }) {
  const [rep, setRep] = useState(null);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!indexReady) return;
    setError('');
    getCompare()
      .then(setRep)
      .catch((e) => setError(e.message));
  }, [indexReady]);

  if (!indexReady) {
    return (
      <div className="banner">
        Сравнивать пока нечего — <a href={routeHash('index')}>постройте индекс</a>.
      </div>
    );
  }
  if (error) return <div className="error">{error}</div>;
  if (!rep) return <div className="empty">считаю метрики: каждый контрольный вопрос прогоняется через каждый вариант…</div>;

  const vs = rep.variants.filter((v) => v.ready);
  const sem = vs.find((v) => v.boundaries);

  return (
    <div className="compare">
      <section className="panel conclusion">
        <h2>Что показывают цифры</h2>
        <ul>
          {(rep.conclusion || []).map((c, i) => (
            <li key={i}>{c}</li>
          ))}
        </ul>
        <p className="muted">
          {rep.questions.length} контрольных вопросов по обеим книгам; для каждого известны книга и глава с ответом. Размеры
          чанков {rep.params.Min}–{rep.params.Max} токенов, окно {rep.params.Target}, перекрытие {rep.params.Overlap}.
        </p>
      </section>

      <section className="panel">
        <div className="table-wrap">
          <table className="metrics">
            <thead>
              <tr>
                <th />
                {vs.map((v) => (
                  <th key={v.variant.id}>
                    <div>{v.variant.title}</div>
                    <div className="th-hint">{v.variant.hint}</div>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {METRICS.map((m, i) => {
                if (m.group) {
                  return (
                    <tr key={i} className="group">
                      <td colSpan={vs.length + 1}>{m.group}</td>
                    </tr>
                  );
                }
                const values = vs.map((v) => m.get(v));
                const top = m.neutral ? undefined : best(values, !m.lower);
                return (
                  <tr key={m.key}>
                    <th scope="row">
                      <div>{m.title}</div>
                      {m.hint && <div className="th-hint">{m.hint}</div>}
                    </th>
                    {vs.map((v, j) => (
                      <td key={v.variant.id} className={top !== undefined && values[j] === top ? 'best' : ''}>
                        {values[j] === undefined ? '—' : m.fmt(values[j], v)}
                      </td>
                    ))}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      <section className="panel">
        <h2>Размеры чанков</h2>
        <p className="muted">Сколько чанков каждого размера, по 100 токенов. Зелёная полоса — рекомендованные 500–1000.</p>
        <div className="histos">
          {vs.map((v) => (
            <Histogram key={v.variant.id} title={v.variant.title} bins={v.shape.histogram} />
          ))}
        </div>
      </section>

      {sem && (
        <section className="panel">
          <h2>Нашла ли смысловая нарезка главы сама</h2>
          <p>
            «{sem.variant.title}» не знает, где кончаются главы. Из {sem.boundaries.chapters} границ глав рядом с её разрезом
            (±2 предложения) оказались <b>{sem.boundaries.matched}</b>. Если бы те же {sem.boundaries.cuts} разрезов стояли
            наугад, совпало бы около {sem.boundaries.random.toFixed(1)}.
          </p>
        </section>
      )}

      <section className="panel">
        <h2>Контрольные вопросы</h2>
        <p className="muted">
          Место, на котором вариант нашёл нужную главу (из первой десятки). Клик по вопросу — открыть его в поиске.
        </p>
        <div className="table-wrap">
          <table className="questions">
            <thead>
              <tr>
                <th>Вопрос</th>
                <th>Ответ</th>
                {vs.map((v) => (
                  <th key={v.variant.id}>{v.variant.title}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rep.questions.map((row) => {
                const q = row.question;
                return (
                  <tr key={q.id}>
                    <td>
                      <a href={routeHash('search', { q: q.q, qid: q.id })}>{q.q}</a>
                      <span className="lang">{q.lang}</span>
                    </td>
                    <td className="nowrap">
                      {q.book === 'tom' ? 'Том' : q.book === 'huck' ? 'Гек' : q.book}, гл. {q.chapters.join(', ')}
                    </td>
                    {vs.map((v) => {
                      const p = row.places[v.variant.id] || {};
                      return (
                        <td key={v.variant.id} className={`place-cell ${placeTone(p.rank)}`}>
                          {p.rank || '—'}
                          {p.topBook && p.topBook !== q.book && <span className="wrong-book" title="первое место — из другой книги">≠книга</span>}
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {rep.invalid?.length > 0 && (
          <div className="invalid">
            <h3>Не участвуют в метриках</h3>
            <ul>
              {rep.invalid.map((q) => (
                <li key={q.id}>
                  <b>{q.id}</b>: {q.problem}
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>
    </div>
  );
}

function Histogram({ title, bins }) {
  const max = Math.max(1, ...bins);
  const w = 24;
  const h = 90;
  return (
    <figure className="histo">
      <svg viewBox={`0 0 ${bins.length * w} ${h + 18}`} role="img" aria-label={`Размеры чанков: ${title}`}>
        <rect x={5 * w} y={0} width={6 * w} height={h} className="histo-range" />
        {bins.map((b, i) => {
          const bh = (b / max) * (h - 12);
          return (
            <g key={i}>
              <rect x={i * w + 3} y={h - bh} width={w - 6} height={bh} className="histo-bar" />
              {b > 0 && (
                <text x={i * w + w / 2} y={h - bh - 3} className="histo-num">
                  {b}
                </text>
              )}
              {i % 2 === 1 && (
                <text x={i * w + w / 2} y={h + 13} className="histo-axis">
                  {i === 11 ? '1100+' : i * 100}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      <figcaption>{title}</figcaption>
    </figure>
  );
}
