async function call(method, url, body) {
  const resp = await fetch(url, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || `HTTP ${resp.status}`);
  return data;
}

export const api = {
  status: () => call('GET', '/api/status'),
  ask: (question, ticketId) => call('POST', '/api/ask', { question, ticketId: ticketId || 0 }),
  tickets: (status) => call('GET', `/api/tickets${status ? `?status=${status}` : ''}`).then((d) => d.tickets),
  createTicket: (title, description) => call('POST', '/api/tickets', { title, description }),
  updateTicket: (id, patch) => call('PATCH', `/api/tickets/${id}`, patch),
};
