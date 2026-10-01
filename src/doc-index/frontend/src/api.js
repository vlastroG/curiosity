// Тонкая обёртка над API бэкенда. Ошибка сервера превращается в Error с его текстом.

async function request(path, options = {}) {
  const resp = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  const body = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new Error(body.error || `сервер ответил ${resp.status}`);
  }
  return body;
}

export const getStatus = () => request('/api/status');
export const getQuestions = () => request('/api/questions');
export const getBook = (book) => request(`/api/books/${encodeURIComponent(book)}`);
export const getSection = (book, n) => request(`/api/books/${encodeURIComponent(book)}/sections/${n}`);
export const getSectionChunks = (book, n, variant) =>
  request(`/api/books/${encodeURIComponent(book)}/sections/${n}/chunks?variant=${encodeURIComponent(variant)}`);
export const getChunk = (variant, id) =>
  request(`/api/chunks/${encodeURIComponent(variant)}/${encodeURIComponent(id)}`);
export const ask = (question, settings) =>
  request('/api/ask', { method: 'POST', body: JSON.stringify({ question, settings }) });
export const search = (query, variants, book, k = 5) =>
  request('/api/search', { method: 'POST', body: JSON.stringify({ query, variants, book, k }) });
export const startIndex = (rebuild) =>
  request('/api/index', { method: 'POST', body: JSON.stringify({ rebuild }) });

// indexEvents -- поток событий индексации. Возвращает функцию отписки.
export function indexEvents(since, onEvent, onEnd) {
  return jobEvents('/api/index/events', since, onEvent, onEnd);
}

function jobEvents(path, since, onEvent, onEnd) {
  const src = new EventSource(`${path}?since=${since}`);
  src.onmessage = (m) => {
    try {
      onEvent(JSON.parse(m.data));
    } catch {
      /* битое событие пропускаем */
    }
  };
  src.addEventListener('end', () => {
    src.close();
    onEnd?.();
  });
  src.onerror = () => {
    src.close();
    onEnd?.();
  };
  return () => src.close();
}
