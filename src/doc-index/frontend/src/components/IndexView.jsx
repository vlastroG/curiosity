import { useEffect, useRef, useState } from 'react';
import { indexEvents, startIndex } from '../api.js';
import { fmtMB, fmtNum, fmtSeconds, plural } from '../format.js';

// Этапы до вариантов: по ним строится «лестница» хода индексации.
const PREP = [
  { id: 'ollama', title: 'Ollama и модели', stages: ['ollama', 'pull'] },
  { id: 'books', title: 'Книги: скачать, очистить, разобрать на главы', stages: ['fetch', 'parse', 'reset'] },
  { id: 'calibrate', title: 'Калибровка токенов и проверка GPU', stages: ['calibrate', 'gpu'] },
];

export default function IndexView({ status, onChanged }) {
  const [events, setEvents] = useState([]);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState('');
  const [showLog, setShowLog] = useState(false);
  const stopRef = useRef(null);

  const subscribe = (since = 0) => {
    stopRef.current?.();
    setRunning(true);
    stopRef.current = indexEvents(
      since,
      (e) => setEvents((prev) => (prev.some((x) => x.seq === e.seq) ? prev : [...prev, e])),
      () => {
        setRunning(false);
        onChanged?.();
      }
    );
  };

  // открыли страницу посреди индексации -- подхватываем с начала
  useEffect(() => {
    if (status?.job?.running && !running) subscribe(0);
  }, [status?.job?.running]);
  useEffect(() => () => stopRef.current?.(), []);

  const start = async (rebuild) => {
    if (rebuild && !window.confirm('Стереть индекс и посчитать всё заново? Скачанные модели и книги останутся.')) return;
    setError('');
    setEvents([]);
    try {
      await startIndex(rebuild);
      subscribe(0);
    } catch (e) {
      setError(e.message);
    }
  };

  const variants = status?.variants || [];
  const books = status?.books || [];
  const ready = variants.some((v) => v.ready);
  const last = events[events.length - 1];
  const failed = events.find((e) => e.stage === 'error');
  const finished = events.find((e) => e.stage === 'done');
  const bookCount = new Set(events.filter((e) => e.stage === 'fetch').map((e) => e.book)).size;

  return (
    <div className="index-view">
      <section className="panel run">
        <div className="run-head">
          <div>
            <h2>Индексация</h2>
            <p className="muted">
              Книги → очистка → главы → нарезка четырьмя способами → эмбеддинги на видеокарте → SQLite. Повторный запуск
              пересчитывает только то, что изменилось.
            </p>
          </div>
          <div className="run-buttons">
            <button className="btn primary" disabled={running} onClick={() => start(false)}>
              {running ? 'Идёт индексация…' : ready ? 'Проверить и достроить' : 'Построить индекс'}
            </button>
            {ready && (
              <button className="btn" disabled={running} onClick={() => start(true)}>
                Построить заново
              </button>
            )}
          </div>
        </div>
        {error && <div className="error">{error}</div>}

        {events.length > 0 && (
          <>
            <Now event={failed || finished || last} running={running} />
            <ol className="steps">
              {PREP.map((p) => (
                <Step
                  key={p.id}
                  title={p.title}
                  events={events.filter((e) => p.stages.includes(e.stage) && !e.variant)}
                  running={running}
                  current={!last?.variant && p.stages.includes(last?.stage)}
                />
              ))}
              {variants.map((v) => (
                <Step
                  key={v.id}
                  title={`${v.title} · ${v.model}`}
                  events={events.filter((e) => e.variant === v.id)}
                  running={running}
                  current={last?.variant === v.id}
                  bookCount={bookCount}
                />
              ))}
            </ol>
            <button className="link" onClick={() => setShowLog((x) => !x)}>
              {showLog ? 'Скрыть' : 'Показать'} все события ({events.length})
            </button>
            {showLog && (
              <ol className="log">
                {events.map((e) => (
                  <li key={e.seq} className={e.level || ''}>
                    <span className="log-time">{new Date(e.time).toLocaleTimeString('ru-RU')}</span>
                    <span className="log-stage">{e.stage}</span>
                    {e.message}
                  </li>
                ))}
              </ol>
            )}
          </>
        )}
      </section>

      <section className="variant-cards">
        {variants.map((v) => (
          <article key={v.id} className={`panel vcard ${v.ready ? 'ready' : ''}`}>
            <div className="vcard-head">
              <h3>{v.title}</h3>
              <span className={`pill ${v.ready ? 'good' : 'warn'}`}>{v.ready ? 'готов' : 'не построен'}</span>
            </div>
            <p className="muted">{v.hint}</p>
            <dl className="facts">
              <dt>модель</dt>
              <dd>{v.model}</dd>
              {v.ready && (
                <>
                  <dt>чанков</dt>
                  <dd>
                    {fmtNum(v.chunks)} ({v.books.map((b) => `${b.book}: ${b.chunks}`).join(', ')})
                  </dd>
                  <dt>токенов</dt>
                  <dd>{fmtNum(v.tokens)}</dd>
                  <dt>вектор</dt>
                  <dd>{v.dims} чисел</dd>
                  <dt>эмбеддинги</dt>
                  <dd>{fmtSeconds(v.books.reduce((s, b) => s + b.embedSeconds, 0))}</dd>
                  <dt>построен</dt>
                  <dd>{new Date(Math.max(...v.books.map((b) => Date.parse(b.builtAt)))).toLocaleString('ru-RU')}</dd>
                </>
              )}
            </dl>
          </article>
        ))}
      </section>

      <section className="panel">
        <h2>Ollama и видеокарта</h2>
        {status?.ollama?.error ? (
          <div className="error">{status.ollama.error}</div>
        ) : (
          <>
            <p className="muted">
              Ollama {status?.ollama?.version} · {status?.ollama?.url}. Считать разрешено только на видеокарте: модель, которая
              не поместилась в видеопамять целиком, останавливает индексацию и поиск.
            </p>
            <ul className="gpu">
              {[...new Set(variants.map((v) => v.model))].map((m) => {
                const p = status?.ollama?.models?.[m];
                let text = 'не загружена (загрузится при первом запросе)';
                let tone = 'muted';
                if (p?.loaded && p.sizeVram >= p.size && p.sizeVram > 0) {
                  text = `в видеопамяти целиком, ${fmtMB(p.sizeVram)}`;
                  tone = 'good';
                } else if (p?.loaded) {
                  text = `на видеокарте только ${fmtMB(p.sizeVram)} из ${fmtMB(p.size)} — остальное считает процессор`;
                  tone = 'bad';
                }
                return (
                  <li key={m}>
                    <b>{m}</b> <span className={`pill ${tone}`}>{text}</span>
                  </li>
                );
              })}
            </ul>
          </>
        )}
      </section>

      {status?.sample && <Sample sample={status.sample} />}

      <section className="panel">
        <h2>Книги</h2>
        {books.length === 0 && <p className="muted">Ещё не скачаны.</p>}
        <ul className="books">
          {books.map((b) => (
            <li key={b.id}>
              <b>{b.title}</b> — {b.author}. {b.sections} {plural(b.sections, 'раздел', 'раздела', 'разделов')},{' '}
              {fmtNum(b.chars)} символов.{' '}
              <a href={b.url} target="_blank" rel="noreferrer noopener">
                gutenberg.org/ebooks/{b.gutenberg} ↗
              </a>
            </li>
          ))}
        </ul>
        <p className="muted">
          Тексты — общественное достояние. Служебная шапка и лицензия Project Gutenberg из них вырезаны; копия и индекс
          лежат только локально, в папке data.
        </p>
      </section>
    </div>
  );
}

// Now -- крупная карточка «что происходит сейчас».
function Now({ event, running }) {
  if (!event) return null;
  const tone = event.stage === 'error' ? 'bad' : event.stage === 'done' ? 'good' : 'live';
  const progress = event.total ? Math.round((100 * event.done) / event.total) : null;
  return (
    <div className={`now ${tone}`}>
      <div className="now-stage">
        {event.stage === 'done' ? 'Готово' : event.stage === 'error' ? 'Ошибка' : running ? 'Сейчас' : 'Остановлено'}
      </div>
      <div className="now-text">{event.message}</div>
      {progress !== null && running && (
        <div className="progress">
          <div className="progress-fill" style={{ width: `${progress}%` }} />
        </div>
      )}
    </div>
  );
}

// Step -- этап лестницы: ждёт, идёт, готов или пропущен. Этап подготовки
// готов, как только события пошли дальше; вариант -- когда записаны все книги.
function Step({ title, events, running, current, bookCount }) {
  const last = events[events.length - 1];
  let state = 'wait';
  if (bookCount !== undefined) {
    const finished = events.filter((e) => e.stage === 'store' || e.stage === 'skip').length;
    if (bookCount > 0 && finished >= bookCount) state = events.some((e) => e.stage === 'store') ? 'done' : 'skip';
    else if (current && running) state = 'live';
  } else if (events.length) {
    state = current && running ? 'live' : 'done';
  }
  const progress = last?.total && state === 'live' ? Math.round((100 * last.done) / last.total) : null;

  return (
    <li className={`step ${state}`}>
      <span className="step-dot" aria-hidden="true" />
      <div className="step-body">
        <div className="step-title">
          {title}
          <span className="step-state">
            {{ wait: 'ждёт', live: 'идёт', done: 'готово', skip: 'без изменений' }[state]}
          </span>
        </div>
        {last && <div className="step-last">{last.message}</div>}
        {progress !== null && (
          <div className="progress small">
            <div className="progress-fill" style={{ width: `${progress}%` }} />
          </div>
        )}
      </div>
    </li>
  );
}

// Sample -- один чанк с метаданными: что именно лежит в индексе.
function Sample({ sample }) {
  const c = sample.chunk;
  const rows = [
    ['chunk_id', c.chunkId],
    ['source', c.source],
    ['title', c.title],
    ['author', c.author],
    ['section', c.section],
    ['ordinal', c.ordinal],
    ['start / body_start / end', `${c.start} / ${c.bodyStart} / ${c.end} (байты в тексте книги)`],
    ['tokens', c.tokens],
    ['fingerprint', c.fingerprint],
    ['vector', `[${sample.vectorHead.map((x) => x.toFixed(4)).join(', ')}, …] — ${sample.vectorDims} чисел, длина 1`],
  ];
  return (
    <section className="panel">
      <h2>Что лежит в индексе: один чанк</h2>
      <table className="kv">
        <tbody>
          {rows.map(([k, v]) => (
            <tr key={k}>
              <th>{k}</th>
              <td>{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <blockquote className="sample-text">{c.text}</blockquote>
    </section>
  );
}
