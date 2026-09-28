// Обёртка над API бэкенда. Ошибка сервера превращается в Error с его текстом.

async function request(path, options = {}) {
  const resp = await fetch(path, { headers: { 'Content-Type': 'application/json' }, ...options });
  const body = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(body.error || `сервер ответил ${resp.status}`);
  return body;
}

export const getMeta = () => request('/api/meta');
export const getServers = () => request('/api/servers');
export const listRuns = () => request('/api/runs');
export const getRun = (id) => request(`/api/runs/${id}`);
export const submitRun = (form) => request('/api/runs', { method: 'POST', body: JSON.stringify(form) });

// subscribe -- живые события прогона. EventSource сам переподключается;
// since не даёт получить уже показанные события повторно.
export function subscribe(id, since, onEvent, onEnd) {
  const source = new EventSource(`/api/runs/${id}/events?since=${since}`);
  source.onmessage = (msg) => {
    const event = JSON.parse(msg.data);
    onEvent(event);
    if (event.type === 'finished') {
      source.close();
      onEnd?.();
    }
  };
  return () => source.close();
}
