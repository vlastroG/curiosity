import { formatDay, formatNumber, temp } from '../tools.js';

// Готовый план -- то, что вернул trip_get сервера планов.
export default function PlanView({ plan, answer }) {
  const days = plan.days || [];
  const budget = plan.budget;
  return (
    <section className="plan">
      <h2 className="section-title">План поездки</h2>
      {(plan.summary || answer) && <p className="plan-summary">{plan.summary || answer}</p>}

      <div className="days">
        {days.map((day) => (
          <article key={day.date} className="card day">
            <div className="day-head">
              <h3>{formatDay(day.date)}</h3>
              <div className="weather">
                <span className="weather-temp">
                  {temp(day.weather?.min)}…{temp(day.weather?.max)}
                </span>
                <span>{day.weather?.summary}</span>
                <span className="muted">осадки {day.weather?.precip_chance ?? 0}%</span>
                <span className={`pill ${day.weather?.source === 'forecast' ? 'tone-ok' : 'tone-muted'}`}>
                  {day.weather?.source === 'forecast' ? 'прогноз' : 'климатическая норма'}
                </span>
              </div>
            </div>
            <ul className="activities">
              {(day.activities || []).map((a, i) => (
                <li key={i}>
                  {a.time && <span className="time">{a.time}</span>}
                  <div>
                    {safe(a.url) ? (
                      <a href={a.url} target="_blank" rel="noopener noreferrer">
                        {a.title}
                      </a>
                    ) : (
                      <b>{a.title}</b>
                    )}
                    {a.description && <p>{a.description}</p>}
                  </div>
                </li>
              ))}
            </ul>
            {day.notes && <p className="day-notes">💡 {day.notes}</p>}
          </article>
        ))}
      </div>

      {budget && (
        <article className="card budget">
          <h3>Бюджет</h3>
          <p className="budget-main">
            {formatNumber(budget.total)} {budget.currency}
            {budget.local_total ? (
              <>
                {' '}
                ≈ <b>{formatNumber(budget.local_total)} {budget.local_currency}</b>
              </>
            ) : null}
          </p>
          {budget.rate_date && <p className="muted">Курс ЦБ РФ на {budget.rate_date}</p>}
          {budget.per_person_per_day ? (
            <p>
              На человека в день: <b>{formatNumber(budget.per_person_per_day)} {budget.local_currency || budget.currency}</b>
            </p>
          ) : null}
          {budget.categories?.length > 0 && (
            <table>
              <tbody>
                {budget.categories.map((c) => (
                  <tr key={c.name}>
                    <td>{c.name}</td>
                    <td>{formatNumber(c.amount)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </article>
      )}

      {plan.notes?.length > 0 && (
        <article className="card notes">
          <h3>Допущения</h3>
          <ul>
            {plan.notes.map((n, i) => (
              <li key={i}>{n}</li>
            ))}
          </ul>
        </article>
      )}
    </section>
  );
}

function safe(url) {
  return typeof url === 'string' && /^https?:\/\//.test(url);
}
