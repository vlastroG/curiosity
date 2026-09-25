import { useEffect, useState } from 'react';

/**
 * Правая панель: настройки одного чата.
 *
 * Изменения копятся в черновике и уходят на бэкенд по кнопке. Автосохранение здесь
 * было бы хуже: ползунок температуры отправил бы полтора десятка запросов на одно
 * движение мыши, а валидация настроек живёт на сервере.
 */
export function SettingsPanel({ chat, catalog, saving, error, onSave, onClose }) {
  const [draft, setDraft] = useState(chat.config);
  const [title, setTitle] = useState(chat.title);

  // при переключении чата черновик берётся заново -- иначе настройки одного чата
  // утекут в другой
  useEffect(() => {
    setDraft(chat.config);
    setTitle(chat.title);
  }, [chat.id, chat.config, chat.title]);

  const dirty =
    title !== chat.title || JSON.stringify(draft) !== JSON.stringify(chat.config);

  const set = (patch) => setDraft((prev) => ({ ...prev, ...patch }));

  // при смене модели бюджет вывода ведёт себя так же, как на сервере: нетронутый
  // дефолт идёт за моделью, введённое руками сохраняется и лишь обрезается по потолку.
  // Повторяем правило здесь, чтобы число поменялось сразу, а не после «применить»;
  // последнее слово всё равно за сервером -- он возвращает итоговый конфиг
  const chooseModel = (model) =>
    set({ model, maxTokens: budgetAfterSwitch(catalog, draft.model, model, draft.maxTokens) });

  return (
    <aside className="settings">
      <div className="settings__head">
        <span className="sidebar__title">настройки агента</span>
        <button className="btn btn--ghost" onClick={onClose} title="свернуть панель">
          ×
        </button>
      </div>

      <div className="settings__body">
        <label className="field">
          <span className="field__label">название чата</span>
          <input value={title} onChange={(event) => setTitle(event.target.value)} />
        </label>

        <label className="field">
          <span className="field__label">модель</span>
          <select value={draft.model} onChange={(event) => chooseModel(event.target.value)}>
            {catalog.models.map((model) => (
              <option key={model.id} value={model.id} disabled={!model.available}>
                {model.title}
                {model.available ? '' : ' — нет ключа'}
              </option>
            ))}
          </select>
          <span className="field__hint">{modelHint(catalog, draft.model)}</span>
        </label>

        <Toggle
          label="погода по MCP"
          hint={weatherHint(catalog, draft.model)}
          checked={Boolean(draft.weather) && toolsCapable(catalog, draft.model)}
          disabled={!toolsCapable(catalog, draft.model)}
          onChange={(weather) => set({ weather })}
        />

        <Slider
          label="temperature"
          hint="разброс ответов: 0 — предсказуемо, 2 — свободно"
          min={0}
          max={2}
          step={0.1}
          value={draft.temperature}
          onChange={(temperature) => set({ temperature })}
        />

        <NumberField
          label="бюджет вывода"
          hint={budgetHint(catalog, draft.model)}
          min={1}
          max={ceilingOf(catalog, draft.model)}
          value={draft.maxTokens}
          onChange={(maxTokens) => set({ maxTokens })}
        />

        <div className="settings__group">
          <span className="settings__group-title">стратегии контекста</span>
          <span className="field__hint">
            Краткосрочная память: последние 20 сообщений уходят в модель как есть,
            дальше окно закрывается и сворачивается в пересказ. Настройки здесь нет
            намеренно — это предохранитель на случай, когда переписка по одной задаче
            разрастается, а не ручка для кручения. Рабочая память задачи и долговременный
            справочник знаний живут отдельно. Ветки диалога — кнопка «чекпоинт»
            в шапке чата.
          </span>
        </div>

        <NumberField
          label="лимит длины запроса"
          hint="символов; проверяет входная политика до вызова модели"
          value={draft.maxInputChars}
          onChange={(maxInputChars) => set({ maxInputChars })}
        />
      </div>

      <div className="settings__foot">
        {error && <div className="notice notice--failed">{error}</div>}
        <button
          className="btn"
          disabled={!dirty || saving}
          onClick={() => onSave({ title, config: editable(draft) })}
        >
          {saving ? 'сохраняю…' : dirty ? 'применить' : 'сохранено'}
        </button>
      </div>
    </aside>
  );
}

function modelHint(catalog, id) {
  const model = catalog.models.find((item) => item.id === id);
  if (!model) return '';
  if (model.priceOut === 0) return `${model.subtitle} · бесплатно`;
  return `${model.subtitle} · $${model.priceCacheMiss} / $${model.priceOut} за 1M (peak)`;
}

// editable -- что панель имеет право отправить.
//
// Черновик держит настройки чата целиком, включая выводимые: бюджет вывода лежит
// в нём, чтобы его показать. Но сервер принимает только редактируемые поля и на
// лишнее отвечает отказом -- пусть список того, что уходит на сервер, будет виден
// глазами, а не складывался сам собой из того, что оказалось в черновике.
function editable(draft) {
  return {
    model: draft.model,
    temperature: draft.temperature,
    maxTokens: draft.maxTokens,
    maxInputChars: draft.maxInputChars,
    weather: draft.weather,
  };
}

// toolsCapable -- умеет ли выбранная модель вызывать инструменты.
//
// Флаг приходит из каталога, с сервера: решать это в интерфейсе значило бы
// держать список моделей в двух местах.
function toolsCapable(catalog, id) {
  return Boolean(catalog.models.find((item) => item.id === id)?.tools);
}

function weatherHint(catalog, id) {
  if (!toolsCapable(catalog, id)) {
    return 'выбранная модель не умеет вызывать инструменты';
  }
  return 'агент сам узнаёт погоду в месте работ и учитывает её в плане. Город — в профиле';
}

// Toggle -- выключатель. Отдельным компонентом ради той же разметки полей,
// что у ползунка и числовых настроек.
function Toggle({ label, hint, checked, disabled, onChange }) {
  return (
    <label className={`field${disabled ? ' field--disabled' : ''}`}>
      <span className="field__label">
        <input
          type="checkbox"
          checked={checked}
          disabled={disabled}
          onChange={(event) => onChange(event.target.checked)}
        />{' '}
        {label}
      </span>
      <span className="field__hint">{hint}</span>
    </label>
  );
}

function modelOf(catalog, id) {
  return catalog.models.find((item) => item.id === id);
}

function ceilingOf(catalog, id) {
  return modelOf(catalog, id)?.maxOutputTokens ?? 0;
}

// budgetAfterSwitch повторяет agent.MaxTokensAfterSwitch: одно значение на все модели
// и было причиной, по которой чат на рассуждающей модели молчал -- весь бюджет уходил
// во внутреннее рассуждение. Но и выбросить введённое человеком число нельзя
function budgetAfterSwitch(catalog, from, to, current) {
  const untouched = current === (modelOf(catalog, from)?.defaultMaxTokens ?? 0);
  if (untouched) return modelOf(catalog, to)?.defaultMaxTokens ?? current;
  return Math.min(current, ceilingOf(catalog, to) || current);
}

function budgetHint(catalog, id) {
  const model = modelOf(catalog, id);
  if (!model) return '';
  const tail = model.reasoning
    ? 'у рассуждающих моделей сюда же входит внутреннее рассуждение, поэтому бюджет щедрее'
    : 'потолок длины ответа';
  return `токенов на ответ; потолок модели — ${model.maxOutputTokens}. ${tail}`;
}

function Slider({ label, hint, min, max, step, value, onChange }) {
  return (
    <label className="field">
      <span className="field__label">
        {label} <span className="field__value">{value}</span>
      </span>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
      />
      <span className="field__hint">{hint}</span>
    </label>
  );
}

function NumberField({ label, hint, min, max, value, onChange }) {
  // потолок держим сами: набрать число, на котором провайдер ответит отказом, нельзя.
  // Нижнюю границу на каждом нажатии не зажимаем -- пока человек стирает поле, чтобы
  // набрать новое число, подстановка минимума дралась бы с набором; её проверит сервер
  const clamp = (next) => (max && next > max ? max : next);

  return (
    <label className="field">
      <span className="field__label">{label}</span>
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        onChange={(event) => onChange(clamp(Number(event.target.value)))}
      />
      <span className="field__hint">{hint}</span>
    </label>
  );
}
