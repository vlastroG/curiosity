const STATUS = { open: 'открыт', in_progress: 'в работе', resolved: 'решён' };

export const STATUSES = Object.keys(STATUS);

export function statusLabel(s) {
  return STATUS[s] || s;
}

const BLOCKED = { injection: 'Запрос отклонён защитой', offtopic: 'Вне темы поддержки' };

export function blockedNote(b) {
  return BLOCKED[b] || '';
}

// indexLine -- состояние индекса одной строкой для шапки.
export function indexLine(ix) {
  if (!ix) return '';
  switch (ix.state) {
    case 'indexing':
      return `Индексация: ${ix.done} из ${ix.total} фрагментов`;
    case 'ready':
      return `Индекс: ${ix.files} файлов, ${ix.chunks} фрагментов`;
    case 'error':
      return `Ошибка индекса: ${ix.error}`;
    default:
      return '';
  }
}
