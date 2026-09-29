import { Fragment, useEffect, useRef, useState } from 'react';
import { getExperiment, getRagEval, ragEvalEvents, startRagEval } from '../api.js';
import { VERDICT, fmtSeconds, pct, queryModeTitle, routeHash, splitCitations } from '../format.js';

// QualityView -- два вопроса качества: как отвечает RAG по сравнению с моделью
// без RAG (10 контрольных вопросов) и почему поиск устроен именно так (эксперимент).
export default function QualityView({ status }) {
  return (
    <div className="quality">
      <RagEval status={status} />
      <Experiment status={status} />
    </div>
  );
}

function RagEval({ status }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [events, setEvents] = useState([]);
  const [running, setRunning] = useState(false);
  const [open, setOpen] = useState('');
  const stop = useRef(null);

  const load = () =>
    getRagEval()
      .then(setData)
      .catch((e) => setError(e.message));

  const follow = () => {
    stop.current?.();
    setRunning(true);
    stop.current = ragEvalEvents(
      0,
      (e) => {
        setEvents((prev) => (prev.some((x) => x.seq === e.seq) ? prev : [...prev, e]));
        load();
      },
      () => {
        setRunning(false);
        load();
      }
    );
  };

  useEffect(() => {
    load().then(() => {});
    return () => stop.current?.();
  }, []);
  useEffect(() => {
    if (data?.job?.running && !running) follow();
  }, [data?.job?.running]);

  const run = async (force) => {
    if (force && !window.confirm('Пересчитать все 10 вопросов заново? Это около 40 вызовов модели.')) return;
    setError('');
    setEvents([]);
    try {
      await startRagEval(force);
      follow();
    } catch (e) {
      setError(e.message);
    }
  };

  const rep = data?.report;
  const rows = rep?.rows || [];
  const last = events[events.length - 1];
  const books = Object.fromEntries((status?.books || []).map((b) => [b.id, b]));
  const llmError = status?.rag?.error;

  return (
    <section className="panel">
      <div className="run-head">
        <div>
          <h2>Контрольные вопросы: с RAG и без</h2>
          <p className="muted">
            Десять вопросов по разным книгам и один вопрос, ответа на который в книгах нет. Для каждого записано, что должно быть
            в ответе и где это в книгах. Модель-судья сравнивает оба ответа с ожиданием, код проверяет, попали ли нужные главы
            в найденные отрывки.
          </p>
        </div>
        <div className="run-buttons">
          <button className="btn primary" disabled={running || !!llmError} onClick={() => run(false)}>
            {running ? 'Идёт прогон…' : rows.length ? 'Досчитать' : 'Прогнать 10 вопросов'}
          </button>
          {rows.length > 0 && (
            <button className="btn" disabled={running || !!llmError} onClick={() => run(true)}>
              Заново
            </button>
          )}
        </div>
      </div>
      {llmError && <div className="error">Модель недоступна: {llmError}</div>}
      {error && <div className="error">{error}</div>}
      {running && last && (
        <div className="now live">
          <div className="now-stage">
            Вопрос {last.done || 0} из {last.total || 10}
          </div>
          <div className="now-text">{last.message}</div>
          {last.total > 0 && (
            <div className="progress">
              <div className="progress-fill" style={{ width: `${(100 * last.done) / last.total}%` }} />
            </div>
          )}
        </div>
      )}

      {rows.length > 0 && (
        <>
          <div className="tally">
            <Tally title="Без RAG" t={rep.noRag} n={rows.length} />
            <Tally title="С RAG" t={rep.rag} n={rows.length} />
            <div className="tally-card">
              <div className="tally-title">Нужные главы в отрывках</div>
              <div className="tally-big">
                {rep.sourcesHit} из {rep.withSource}
              </div>
              <div className="muted">вопросов с источником в книгах</div>
            </div>
          </div>
          <p className="muted">
            Модель: {rep.model} · поиск: {rep.config} · обновлено {new Date(rep.updated).toLocaleString('ru-RU')}
          </p>

          <div className="table-wrap">
            <table className="eval">
              <thead>
                <tr>
                  <th>Вопрос</th>
                  <th>Без RAG</th>
                  <th>С RAG</th>
                  <th>Источник в отрывках</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const c = row.control;
                  const isOpen = open === c.id;
                  return (
                    <Fragment key={c.id}>
                      <tr className="eval-row" onClick={() => setOpen(isOpen ? '' : c.id)}>
                        <td>
                          <div className="eval-q">
                            <span className="toggle-arrow">{isOpen ? '▾' : '▸'}</span> {c.q}
                          </div>
                          <div className="muted small">
                            {c.book ? `${books[c.book]?.titleRu || c.book}, гл. ${c.chapters.join(', ')}` : 'ответа в книгах нет'}
                          </div>
                        </td>
                        <td>
                          <VerdictChip v={row.noRagVerdict} />
                        </td>
                        <td>
                          <VerdictChip v={row.ragVerdict} />
                        </td>
                        <td className="center">
                          {row.sourcesHit === undefined || row.sourcesHit === null ? '—' : row.sourcesHit ? '✔' : '✘'}
                        </td>
                      </tr>
                      {isOpen && (
                        <tr className="eval-detail">
                          <td colSpan={4}>
                            <div className="expect">
                              <b>Ожидание.</b> {c.expected}
                            </div>
                            {row.judgeError && <div className="error">{row.judgeError}</div>}
                            <div className="answers compact">
                              <div className="answer-col">
                                <div className="answer-title">Без RAG</div>
                                <p className="judge">{row.noRagVerdict?.reason}</p>
                                <Plain text={row.result.noRag.text || row.result.noRag.error} />
                              </div>
                              <div className="answer-col rag">
                                <div className="answer-title">С RAG</div>
                                <p className="judge">{row.ragVerdict?.reason}</p>
                                <Plain text={row.result.rag.text || row.result.rag.error} />
                                <div className="muted small">
                                  Отрывки:{' '}
                                  {(row.result.rag.sources || [])
                                    .map((s) => `[${s.n}] ${books[s.book]?.titleRu || s.book}, ${s.section.split('.')[0]}`)
                                    .join('; ')}
                                </div>
                              </div>
                            </div>
                            <a href={routeHash('ask', { q: c.q })}>Задать этот вопрос заново →</a>
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
      {!rows.length && !running && <p className="muted">Прогона ещё не было.</p>}
    </section>
  );
}

function Tally({ title, t, n }) {
  return (
    <div className="tally-card">
      <div className="tally-title">{title}</div>
      <div className="tally-big">
        {t.correct} <span className="muted">из {n} верно</span>
      </div>
      <div className="tally-bar">
        <span className="good" style={{ flex: t.correct }} />
        <span className="fair" style={{ flex: t.partial }} />
        <span className="bad" style={{ flex: t.wrong }} />
      </div>
      <div className="muted small">
        частично {t.partial} · неверно {t.wrong}
      </div>
    </div>
  );
}

function VerdictChip({ v }) {
  const x = VERDICT[v?.verdict];
  if (!x) return <span className="muted">—</span>;
  return (
    <span className={`pill ${x.tone}`} title={v.reason}>
      {x.text}
    </span>
  );
}

function Plain({ text }) {
  return (
    <div className="answer-text">
      {(text || '').split(/\n{2,}/).map((p, i) => (
        <p key={i}>{splitCitations(p).map((s, j) => (s.cite ? <sup key={j}>[{s.cite}]</sup> : <span key={j}>{s.text}</span>))}</p>
      ))}
    </div>
  );
}

function Experiment({ status }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  useEffect(() => {
    getExperiment()
      .then(setData)
      .catch((e) => setError(e.message));
  }, []);

  const rep = data?.report;
  const current = status?.rag?.retrieval?.id;
  const rows = [...(rep?.rows || [])].sort((a, b) => b.all.hit5 - a.all.hit5 || b.all.mrr - a.all.mrr);

  return (
    <section className="panel">
      <h2>Как выбран способ поиска</h2>
      <p className="muted">
        Каждая модель эмбеддингов индексирует все книги одинаковыми чанками, и 44 вопроса по «Тому Сойеру» и «Гекльберри Финну»
        ищутся среди отрывков всех тринадцати книг. Главная метрика — hit@5: нужная глава среди пяти отрывков, которые получит
        модель. Победитель стал поиском по умолчанию.
      </p>
      {error && <div className="error">{error}</div>}
      {data && !data.exists && (
        <p>
          Эксперимента ещё не было. Запуск: <code>docker compose run --rm doc-index experiment</code>
        </p>
      )}
      {rep && data.exists && (
        <>
          <div className="models">
            {rep.models.map((m) => (
              <div key={m.model} className="model-card">
                <b>{m.model}</b>
                <span className="muted">
                  вектор {m.dims} · {m.chunks} чанков · индекс {fmtSeconds(m.indexSeconds)}
                </span>
              </div>
            ))}
          </div>
          <div className="table-wrap">
            <table className="metrics">
              <thead>
                <tr>
                  <th>Модель</th>
                  <th>Поиск</th>
                  <th>hit@1</th>
                  <th>hit@5</th>
                  <th>MRR</th>
                  <th>hit@5 рус. ({rows[0]?.byLang?.ru?.n ?? 0})</th>
                  <th>hit@5 англ. ({rows[0]?.byLang?.en?.n ?? 0})</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const win = r.config.id === rep.winner.config.id;
                  return (
                    <tr key={r.config.id} className={win ? 'winner' : ''}>
                      <td>
                        {r.model}
                        {r.config.id === current && <span className="pill good now-pill">сейчас</span>}
                      </td>
                      <td>{queryModeTitle(r.config)}</td>
                      <td>{pct(r.all.hit1)}</td>
                      <td className={win ? 'best' : ''}>{pct(r.all.hit5)}</td>
                      <td>{r.all.mrr.toFixed(2)}</td>
                      <td>{pct(r.byLang?.ru?.hit5)}</td>
                      <td>{pct(r.byLang?.en?.hit5)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <p>
            Победитель: <b>{rep.winner.model}</b>, {queryModeTitle(rep.winner.config)} — hit@5 {pct(rep.winner.all.hit5)}, MRR{' '}
            {rep.winner.all.mrr.toFixed(2)}.
          </p>
          <p className="muted">
            Переписывала вопросы модель {rep.rewriteModel || '—'}; прогон {new Date(rep.generatedAt).toLocaleString('ru-RU')}.
          </p>
          {rep.notes?.map((n, i) => (
            <p key={i} className="muted">
              {n}
            </p>
          ))}
        </>
      )}
    </section>
  );
}
