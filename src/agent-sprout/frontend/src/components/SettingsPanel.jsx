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

  // бюджет вывода выводится из модели, и пересчитывает его сервер. Но показать
  // новое число надо сразу при выборе модели, а не после «применить»: рядом написано
  // «подставляется под модель», и неподвижная цифра выглядит как обман
  const chooseModel = (model) => set({ model, maxTokens: budgetOf(catalog, model) });

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

        <Slider
          label="temperature"
          hint="разброс ответов: 0 — предсказуемо, 2 — свободно"
          min={0}
          max={2}
          step={0.1}
          value={draft.temperature}
          onChange={(temperature) => set({ temperature })}
        />

        <div className="field">
          <span className="field__label">
            бюджет вывода <span className="field__value">{draft.maxTokens}</span>
          </span>
          <span className="field__hint">{budgetHint(catalog, draft.model)}</span>
        </div>

        <div className="settings__group">
          <span className="settings__group-title">стратегии контекста</span>
          <span className="field__hint">
            Краткосрочная память: окно последних сообщений и пересказ того, что из него
            выпало. Пересказ собирается всегда — терять окно молча было бы хуже, чем
            заплатить за один служебный вызов. Рабочая память задачи и долговременный
            справочник знаний живут отдельно и настроек не требуют. Ветки диалога —
            кнопка «чекпоинт» в шапке чата.
          </span>
        </div>

        <NumberField
          label="окно истории"
          hint={
            'сколько сообщений уходит в модель как есть. Когда окно заполняется, оно ' +
            'закрывается — сворачивается в пересказ, — и отсчёт начинается заново. ' +
            '0 — памяти нет вовсе, каждый запрос без контекста'
          }
          value={draft.historyDepth}
          onChange={(historyDepth) => set({ historyDepth })}
        />

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
    historyDepth: draft.historyDepth,
    maxInputChars: draft.maxInputChars,
  };
}

// бюджет вывода не редактируется: он выводится из модели и пересчитывается при
// её смене. Одно значение на все модели и было причиной, по которой чат на
// рассуждающей модели молчал -- весь бюджет уходил во внутреннее рассуждение
function budgetOf(catalog, id) {
  return catalog.models.find((item) => item.id === id)?.defaultMaxTokens ?? 0;
}

function budgetHint(catalog, id) {
  const model = catalog.models.find((item) => item.id === id);
  if (!model) return '';
  const tail = model.reasoning
    ? 'сюда же входит внутреннее рассуждение, поэтому у рассуждающих моделей он щедрее'
    : 'потолок длины ответа';
  return `токенов, подставляется под модель (её потолок — ${model.maxOutputTokens}). ${tail}`;
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

function NumberField({ label, hint, value, onChange }) {
  return (
    <label className="field">
      <span className="field__label">{label}</span>
      <input
        type="number"
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
      />
      <span className="field__hint">{hint}</span>
    </label>
  );
}
