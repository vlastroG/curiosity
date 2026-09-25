import { useState } from 'react';
import hero from '../assets/hero.jpg';
import { submitRun } from '../api.js';

const INTERESTS = [
  ['museums', 'Музеи'],
  ['architecture', 'Архитектура'],
  ['nature', 'Природа и парки'],
  ['food', 'Еда'],
  ['nightlife', 'Ночная жизнь'],
  ['kids', 'С детьми'],
];

// Форма нового плана. Обязателен только город; всё остальное агент додумает сам
// и честно запишет допущения в план.
export default function NewPlan({ meta, onCreated }) {
  const [form, setForm] = useState({
    city: '',
    startDate: '',
    endDate: '',
    travelers: '',
    budget: '',
    currency: 'RUB',
    interests: [],
    pace: 'calm',
  });
  const [error, setError] = useState('');
  const [sending, setSending] = useState(false);
  const today = meta?.today || new Date().toISOString().slice(0, 10);

  const set = (patch) => {
    setError('');
    setForm((f) => ({ ...f, ...patch }));
  };
  const toggle = (interest) =>
    set({
      interests: form.interests.includes(interest)
        ? form.interests.filter((i) => i !== interest)
        : [...form.interests, interest],
    });

  async function submit(e) {
    e.preventDefault();
    if (!form.city.trim()) {
      setError('Укажите город');
      return;
    }
    if (form.endDate && !form.startDate) {
      setError('Укажите и дату начала');
      return;
    }
    setSending(true);
    try {
      const run = await submitRun({
        city: form.city.trim(),
        startDate: form.startDate || undefined,
        endDate: form.endDate || undefined,
        travelers: form.travelers ? Number(form.travelers) : undefined,
        budget: form.budget ? Number(form.budget) : undefined,
        currency: form.budget ? form.currency : undefined,
        interests: form.interests,
        pace: form.pace,
      });
      onCreated(run);
    } catch (err) {
      setError(err.message);
    } finally {
      setSending(false);
    }
  }

  return (
    <div className="new-plan">
      <section className="hero" style={{ backgroundImage: `url(${hero})` }}>
        <div className="hero-shade">
          <h1>Куда поедем?</h1>
          <p>
            Заполните то, что знаете, — агент сам соберёт план по дням: погоду, что посмотреть и бюджет.
            Вы увидите, как он обращается к четырём сервисам.
          </p>
        </div>
      </section>

      <form className="card form" onSubmit={submit} noValidate>
        <label className="field field-wide">
          <span>Город *</span>
          <input
            value={form.city}
            onChange={(e) => set({ city: e.target.value })}
            placeholder="Стамбул"
            maxLength={80}
            autoFocus
          />
        </label>

        <div className="row">
          <label className="field">
            <span>Начало поездки</span>
            <input type="date" min={today} value={form.startDate} onChange={(e) => set({ startDate: e.target.value })} />
          </label>
          <label className="field">
            <span>Конец поездки</span>
            <input
              type="date"
              min={form.startDate || today}
              value={form.endDate}
              onChange={(e) => set({ endDate: e.target.value })}
            />
          </label>
          <label className="field">
            <span>Сколько человек</span>
            <input
              type="number"
              min="1"
              max={meta?.maxTravelers || 10}
              value={form.travelers}
              onChange={(e) => set({ travelers: e.target.value })}
              placeholder="1"
            />
          </label>
        </div>

        <div className="row">
          <label className="field">
            <span>Бюджет на всех</span>
            <input
              type="number"
              min="1"
              value={form.budget}
              onChange={(e) => set({ budget: e.target.value })}
              placeholder="80000"
            />
          </label>
          <label className="field">
            <span>Валюта</span>
            <select value={form.currency} onChange={(e) => set({ currency: e.target.value })}>
              <option value="RUB">₽ рубли</option>
              <option value="USD">$ доллары</option>
              <option value="EUR">€ евро</option>
            </select>
          </label>
          <label className="field">
            <span>Темп</span>
            <select value={form.pace} onChange={(e) => set({ pace: e.target.value })}>
              <option value="calm">Спокойный</option>
              <option value="busy">Насыщенный</option>
            </select>
          </label>
        </div>

        <fieldset className="field">
          <span>Интересы</span>
          <div className="chips">
            {INTERESTS.map(([id, title]) => (
              <button
                type="button"
                key={id}
                className={`chip ${form.interests.includes(id) ? 'on' : ''}`}
                aria-pressed={form.interests.includes(id)}
                onClick={() => toggle(id)}
              >
                {title}
              </button>
            ))}
          </div>
        </fieldset>

        <p className="hint">
          Без дат — поездка на 3 дня через неделю. Без бюджета — план без расчёта денег. Всё, что агент
          додумал, он запишет в раздел «Допущения».
        </p>
        {error && <div className="form-error">{error}</div>}
        <button className="btn btn-primary btn-big" disabled={sending}>
          {sending ? 'Отправляю…' : 'Составить план'}
        </button>
        {meta?.model && <div className="model-note">Модель: {meta.model.title}</div>}
      </form>
    </div>
  );
}
