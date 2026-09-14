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
          <select value={draft.model} onChange={(event) => set({ model: event.target.value })}>
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

        <Slider
          label="top_p"
          hint="доля вероятностной массы, из которой выбираются токены"
          min={0.05}
          max={1}
          step={0.05}
          value={draft.topP}
          onChange={(topP) => set({ topP })}
        />

        <Slider
          label="frequency_penalty"
          hint="штраф за повторы одних и тех же слов"
          min={-2}
          max={2}
          step={0.1}
          value={draft.frequencyPenalty}
          onChange={(frequencyPenalty) => set({ frequencyPenalty })}
        />

        <Slider
          label="presence_penalty"
          hint="штраф за возврат к уже затронутым темам"
          min={-2}
          max={2}
          step={0.1}
          value={draft.presencePenalty}
          onChange={(presencePenalty) => set({ presencePenalty })}
        />

        <NumberField
          label="max_tokens"
          hint="потолок длины ответа; у рассуждающих моделей сюда же входит внутреннее рассуждение"
          value={draft.maxTokens}
          onChange={(maxTokens) => set({ maxTokens })}
        />

        <label className="field">
          <span className="field__label">формат ответа</span>
          <select
            value={draft.responseFormat}
            onChange={(event) => set({ responseFormat: event.target.value })}
          >
            <option value="text">обычный текст</option>
            <option value="json_object">json-объект</option>
          </select>
          <span className="field__hint">
            в режиме json выходная политика проверяет, что ответ действительно разбирается
          </span>
        </label>

        <NumberField
          label="лимит слов"
          hint="0 — без лимита; попадает в промпт и проверяется выходной политикой"
          value={draft.maxWords}
          onChange={(maxWords) => set({ maxWords })}
        />

        <div className="settings__group">
          <span className="settings__group-title">стратегии контекста</span>
          <span className="field__hint">
            Краткосрочная память: окно последних сообщений и пересказ того, что из него
            выпало. Рабочая память задачи и долговременный справочник знаний живут
            отдельно и настроек не требуют. Ветки диалога — кнопка «чекпоинт» в шапке чата.
          </span>
        </div>

        <NumberField
          label="окно истории"
          hint={
            'сколько сообщений уходит в модель как есть. Когда окно заполняется, оно ' +
            'закрывается — сворачивается в пересказ или отбрасывается, — и отсчёт ' +
            'начинается заново. 0 — памяти нет вовсе, каждый запрос без контекста'
          }
          value={draft.historyDepth}
          onChange={(historyDepth) => set({ historyDepth })}
        />

        <label className="field field--check">
          <input
            type="checkbox"
            checked={draft.summarizeHistory}
            onChange={(event) => set({ summarizeHistory: event.target.checked })}
          />
          <span>
            <span className="field__label">сжимать историю</span>
            <span className="field__hint">
              на закрытии окна агент отдельным вызовом модели сворачивает его в короткий
              пересказ и дальше подставляет пересказ вместо самих сообщений. Каждое
              следующее сжатие складывает прошлый пересказ с новым окном, так что память
              копится, а запрос не растёт. Выключено — окно на переходе просто теряется.
              Стоит одного дополнительного вызова модели на каждые {draft.historyDepth}{' '}
              сообщений
            </span>
          </span>
        </label>

        <NumberField
          label="лимит длины запроса"
          hint="символов; проверяет входная политика до вызова модели"
          value={draft.maxInputChars}
          onChange={(maxInputChars) => set({ maxInputChars })}
        />

        <label className="field field--check">
          <input
            type="checkbox"
            checked={draft.judgeEnabled}
            onChange={(event) => set({ judgeEnabled: event.target.checked })}
          />
          <span>
            <span className="field__label">судья</span>
            <span className="field__hint">
              второй вызов модели оценивает ответ по шкале 1–5. Удваивает расход токенов
            </span>
          </span>
        </label>
      </div>

      <div className="settings__foot">
        {error && <div className="notice notice--failed">{error}</div>}
        <button
          className="btn"
          disabled={!dirty || saving}
          onClick={() => onSave({ title, config: draft })}
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
