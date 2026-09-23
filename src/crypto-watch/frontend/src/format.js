// Форматирование чисел и времени. Отдельно от компонентов, чтобы проверять тестами.

// price -- цена с разумным числом знаков: у биткоина центы, у мелких монет шесть знаков.
export function price(value) {
  if (value == null || Number.isNaN(value)) return '—';
  const digits = value >= 1000 ? 2 : value >= 1 ? 4 : 6;
  return value.toLocaleString('ru-RU', { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

// pct -- процент со знаком: «+0,123 %», «−0,050 %».
export function pct(value, digits = 3) {
  if (value == null || Number.isNaN(value)) return '—';
  const sign = value > 0 ? '+' : value < 0 ? '−' : '';
  return `${sign}${Math.abs(value).toFixed(digits).replace('.', ',')} %`;
}

// direction -- класс цвета по знаку.
export function direction(value, epsilon = 0) {
  if (value > epsilon) return 'up';
  if (value < -epsilon) return 'down';
  return 'flat';
}

// clock -- время по-местному: «18:40:24».
export function clock(iso) {
  if (!iso) return '—';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

// ago -- «12 с назад», «3 мин назад».
export function ago(iso, now = Date.now()) {
  if (!iso) return '';
  const seconds = Math.round((now - new Date(iso).getTime()) / 1000);
  if (seconds < 0) return `через ${until(iso, now)}`;
  if (seconds < 60) return `${seconds} с назад`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} мин назад`;
  return `${Math.round(minutes / 60)} ч назад`;
}

// until -- «через 40 с».
export function until(iso, now = Date.now()) {
  if (!iso) return '';
  const seconds = Math.max(0, Math.round((new Date(iso).getTime() - now) / 1000));
  if (seconds < 60) return `${seconds} с`;
  return `${Math.floor(seconds / 60)} мин ${seconds % 60} с`;
}

// rangeLayout -- положение цены и диапазона прогноза на одной шкале, в процентах ширины.
// Шкала охватывает и цену, и диапазон с запасом, чтобы маркер не прилипал к краю.
export function rangeLayout(current, low, high) {
  const lo = Math.min(current, low);
  const hi = Math.max(current, high);
  const pad = (hi - lo) * 0.15 || current * 0.001 || 1;
  const min = lo - pad;
  const span = hi + pad - min;
  const at = (value) => ((value - min) / span) * 100;
  return { low: at(low), high: at(high), current: at(current) };
}

// modelTitle -- человеческое имя модели по id.
export function modelTitle(models, id) {
  return models.find((model) => model.id === id)?.title ?? id;
}
