// Настройки агента: частота, окно анализа, модель. Меняются сразу по выбору.

const INTERVALS = Array.from({ length: 15 }, (_, i) => i + 1);
const WINDOWS = [15, 30, 60, 120, 240];

export default function Controls({ settings, models, saving, running, onChange, onForecast }) {
  return (
    <div className="controls">
      <label className="field">
        <span>Частота</span>
        <select
          value={settings.intervalMinutes}
          disabled={saving}
          onChange={(e) => onChange({ intervalMinutes: Number(e.target.value) })}
        >
          {INTERVALS.map((minutes) => (
            <option key={minutes} value={minutes}>
              {minutes} мин
            </option>
          ))}
        </select>
      </label>

      <label className="field">
        <span>Окно анализа</span>
        <select
          value={settings.windowMinutes}
          disabled={saving}
          onChange={(e) => onChange({ windowMinutes: Number(e.target.value) })}
        >
          {WINDOWS.map((minutes) => (
            <option key={minutes} value={minutes}>
              {minutes < 60 ? `${minutes} мин` : `${minutes / 60} ч`}
            </option>
          ))}
        </select>
      </label>

      <label className="field field-wide">
        <span>Модель</span>
        <select value={settings.model} disabled={saving} onChange={(e) => onChange({ model: e.target.value })}>
          {models.map((model) => (
            <option key={model.id} value={model.id} disabled={!model.available}>
              {model.title}
              {model.available ? '' : ' — нет ключа'}
            </option>
          ))}
        </select>
      </label>

      <button className="btn" disabled={running} onClick={onForecast}>
        {running ? 'Думает…' : 'Прогноз сейчас'}
      </button>
    </div>
  );
}
