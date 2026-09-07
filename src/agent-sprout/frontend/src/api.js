/**
 * Обёртка над REST API агента.
 *
 * Ключей провайдеров здесь нет и быть не может: браузер ходит только на собственный
 * бэкенд, а тот уже знает, куда и с каким ключом идти. В проектах прошлых дней запросы
 * уходили из браузера напрямую в DeepSeek, и ключ лежал в исходниках страницы.
 */

const BASE = '/api';

/** Ошибка API с кодом: по нему интерфейс отличает отказ политики от аварии провайдера. */
export class ApiError extends Error {
  constructor(message, { code, chat } = {}) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    // бэкенд прикладывает актуальный чат к тем ошибкам, которые уже изменили ленту
    this.chat = chat;
  }
}

async function request(path, { method = 'GET', body } = {}) {
  let response;
  try {
    response = await fetch(BASE + path, {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch (cause) {
    throw new ApiError(`бэкенд недоступен: ${cause.message}`, { code: 'offline' });
  }

  if (response.status === 204) return null;

  const text = await response.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      throw new ApiError(`ответ бэкенда не разобрался: ${text.slice(0, 200)}`, { code: 'internal' });
    }
  }

  if (!response.ok) {
    throw new ApiError(data?.error?.message ?? `HTTP ${response.status}`, {
      code: data?.error?.code ?? 'internal',
      chat: data?.chat,
    });
  }

  return data;
}

export const api = {
  catalog: () => request('/catalog'),
  listChats: () => request('/chats'),
  getChat: (id) => request(`/chats/${id}`),
  createChat: (body) => request('/chats', { method: 'POST', body }),
  patchChat: (id, body) => request(`/chats/${id}`, { method: 'PATCH', body }),
  deleteChat: (id) => request(`/chats/${id}`, { method: 'DELETE' }),
  sendMessage: (id, content) => request(`/chats/${id}/messages`, { method: 'POST', body: { content } }),
  clearMessages: (id) => request(`/chats/${id}/messages`, { method: 'DELETE' }),
};

/** Форматирование чисел, общее для всех метрик интерфейса. */
export function formatUSD(value) {
  if (!value) return '$0';
  if (value < 0.000001) return '<$0.000001';
  return `$${value.toFixed(6)}`;
}

export function formatSeconds(ms) {
  if (ms === undefined || ms === null) return '—';
  return `${(ms / 1000).toFixed(1)} с`;
}
