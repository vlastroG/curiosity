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

export const getState = () => request('/api/state');
export const getModels = () => request('/api/models');
export const getForecasts = (limit = 10) => request(`/api/forecasts?limit=${limit}`);
export const runForecast = () => request('/api/forecast', { method: 'POST' });
export const saveSettings = (settings) =>
  request('/api/settings', { method: 'PUT', body: JSON.stringify(settings) });
