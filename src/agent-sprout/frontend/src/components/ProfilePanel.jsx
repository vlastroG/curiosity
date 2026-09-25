import { useEffect, useState } from 'react';

/**
 * Персонализация — профиль пользователя.
 *
 * Профиль один на всё приложение и уезжает в каждый запрос: «объясняй простыми
 * словами» и «у меня нет болгарки» не принадлежат какому-то одному разговору.
 * Поэтому панель общая, как и справочник знаний, а не настройка чата.
 */

const HINT =
  'Профиль действует во всех чатах сразу и подставляется в каждый запрос. ' +
  'Он меняет, как агент разговаривает, и избавляет от повторов: то, что здесь написано, ' +
  'агент больше не спросит.';

const FIELDS = [
  {
    key: 'about',
    label: 'о себе',
    hint: 'опыт, обстоятельства, объект. Отсюда агент узнаёт, что ему уже не надо спрашивать',
    placeholder: 'Впервые делаю ремонт своими руками, объект — своя квартира',
  },
  {
    key: 'city',
    label: 'город',
    hint:
      'где вы строите. Единственное поле, которое читает не только агент: ' +
      'отсюда берётся место для запроса погоды',
    placeholder: 'Екатеринбург',
  },
  {
    key: 'style',
    label: 'стиль',
    hint: 'как говорить: язык, тон, глубина объяснений',
    placeholder: 'Объясняй простыми словами, расшифровывай термины при первом упоминании',
  },
  {
    key: 'format',
    label: 'формат',
    hint: 'как оформлять ответ: списки, таблицы, длина',
    placeholder: 'Нумерованные шаги, материалы отдельной таблицей с запасом',
  },
  {
    key: 'limits',
    label: 'ограничения',
    hint:
      'чего нет или нельзя: инструмент, бюджет, время, здоровье. ' +
      'Отсюда тоже заполняется чеклист, поэтому пишите конкретно',
    placeholder: 'Из инструмента дрель и правило, спецтехники нет, работаю один по выходным',
  },
];

const EMPTY = { about: '', city: '', style: '', format: '', limits: '' };

// pick -- черновик собирается ТОЛЬКО из редактируемых полей.
//
// Сервер отдаёт профиль вместе со служебным updatedAt, и если завести черновик
// из ответа целиком, это поле уедет обратно в PUT -- а там разбор с запретом
// неизвестных полей, и сохранение отвалится с «unknown field updatedAt».
// Поэтому граница проходит здесь: что редактируется, то и хранится в черновике.
function pick(profile) {
  const draft = { ...EMPTY };
  for (const field of FIELDS) {
    draft[field.key] = profile?.[field.key] ?? '';
  }
  return draft;
}

/** Заготовка профиля: показать целиком до того, как подставлять. */
function Preset({ preset, busy, onApply }) {
  const [open, setOpen] = useState(false);

  return (
    <div className="preset">
      <div className="preset__line">
        <button className="btn btn--chip" disabled={busy} onClick={() => onApply(preset.profile)}>
          ← {preset.title}
        </button>
        <button className="trace__toggle" onClick={() => setOpen(!open)}>
          {open ? '▾ скрыть' : '▸ посмотреть'}
        </button>
      </div>
      {open && (
        <div className="knowledge__item-text">
          {FIELDS.filter((field) => preset.profile[field.key]).map((field) => (
            <div key={field.key} className="profile__preset-field">
              <strong>{field.label}: </strong>
              {preset.profile[field.key]}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function ProfilePanel({ profile, presets, busy, error, onSave, onClose }) {
  const [draft, setDraft] = useState(() => pick(profile));

  // профиль мог измениться на сервере (например, после сохранения) -- черновик
  // берётся заново, иначе форма будет спорить с тем, что реально применяется
  useEffect(() => {
    setDraft(pick(profile));
  }, [profile]);

  const dirty = FIELDS.some((field) => (draft[field.key] ?? '') !== (profile?.[field.key] ?? ''));
  const empty = FIELDS.every((field) => !draft[field.key]?.trim());

  const set = (key, value) => setDraft((prev) => ({ ...prev, [key]: value }));

  // заготовка подставляется в форму, а не сохраняется сама: что о себе рассказать,
  // решает пользователь, и перед сохранением он это увидит и поправит
  const apply = (preset) => setDraft(pick(preset));

  return (
    <aside className="knowledge" title={HINT}>
      <div className="knowledge__head">
        <span className="sidebar__title">профиль пользователя</span>
        <button className="btn btn--ghost" onClick={onClose} title="закрыть профиль">
          ×
        </button>
      </div>

      <div className="knowledge__body">
        <p className="field__hint">
          Действует во всех чатах и уезжает в каждый запрос. Меняет то, как агент
          разговаривает, и избавляет от лишних вопросов: написанное здесь он не спросит
          заново, а подставит в чеклист с пометкой «из профиля» — на сверке это видно
          и поправимо.
        </p>

        {presets.length > 0 && (
          <div className="knowledge__presets">
            <span className="field__hint">
              Заготовки: подставляются в форму, а не сохраняются сами. Возьмите близкую
              и поправьте под себя.
            </span>
            <div className="knowledge__preset-list">
              {presets.map((preset) => (
                <Preset key={preset.title} preset={preset} busy={busy} onApply={apply} />
              ))}
            </div>
          </div>
        )}

        {FIELDS.map((field) => (
          <label key={field.key} className="field">
            <span className="field__label">{field.label}</span>
            {/* восемь строк: в каждое поле должен помещаться абзац-другой,
                не заставляя листать внутри крошечного окошка */}
            <textarea
              rows={8}
              value={draft[field.key] ?? ''}
              placeholder={field.placeholder}
              onChange={(event) => set(field.key, event.target.value)}
            />
            <span className="field__hint">{field.hint}</span>
          </label>
        ))}

        {empty && (
          <p className="muted">
            Профиль пуст — агент разговаривает со всеми одинаково и спрашивает всё с нуля.
          </p>
        )}
      </div>

      <div className="settings__foot">
        {error && <div className="notice notice--failed">{error}</div>}
        <button className="btn" disabled={!dirty || busy} onClick={() => onSave(draft)}>
          {busy ? 'сохраняю…' : dirty ? 'применить' : 'сохранено'}
        </button>
      </div>
    </aside>
  );
}
