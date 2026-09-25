import { pct, price, rangeLayout } from '../format.js';
import { TrendBadge } from './CoinCard.jsx';

// Прогноз модели: общий комментарий и по каждой монете -- куда пойдёт цена,
// в каком диапазоне и насколько модель уверена.
export default function ForecastPanel({ forecast, running, error }) {
  if (!forecast) {
    return (
      <div className="empty">
        {running ? 'Агент делает первый прогноз…' : 'Прогнозов пока нет — первый появится после первого прогона агента.'}
        {error && <div className="bad">Последняя попытка: {error}</div>}
      </div>
    );
  }

  return (
    <div className="forecast">
      <p className="overview">{forecast.overview}</p>
      {error && <div className="banner banner-warn">Последний прогон не удался, показан предыдущий прогноз: {error}</div>}
      <div className="forecast-rows">
        {forecast.coins.map((coin) => (
          <ForecastRow key={coin.symbol} coin={coin} horizon={forecast.horizonMinutes} />
        ))}
      </div>
      <Trace forecast={forecast} />
    </div>
  );
}

function ForecastRow({ coin, horizon }) {
  const layout = rangeLayout(coin.price, coin.low, coin.high);
  const mid = (coin.low + coin.high) / 2;
  return (
    <div className="frow">
      <div className="frow-head">
        <span className="coin-symbol">{coin.symbol}</span>
        <TrendBadge trend={coin.trend} />
        <Confidence value={coin.confidence} />
      </div>
      <div className="range" title={`сейчас ${price(coin.price)}, прогноз ${price(coin.low)}–${price(coin.high)}`}>
        <div className={`range-band ${coin.trend}`} style={{ left: `${layout.low}%`, width: `${Math.max(layout.high - layout.low, 0.8)}%` }} />
        <div className="range-now" style={{ left: `${layout.current}%` }} />
      </div>
      <div className="frow-nums">
        <span>
          сейчас <b>{price(coin.price)}</b>
        </span>
        <span>
          через {horizon} мин <b>{price(coin.low)} – {price(coin.high)}</b>
        </span>
        <span className="muted">середина {pct(((mid - coin.price) / coin.price) * 100)}</span>
      </div>
      <p className="reason">{coin.reason}</p>
    </div>
  );
}

function Confidence({ value }) {
  const percent = Math.round(value * 100);
  const level = value >= 0.66 ? 'high' : value >= 0.33 ? 'mid' : 'low';
  return (
    <span className={`conf ${level}`} title="уверенность модели">
      <span className="conf-bar">
        <span style={{ width: `${percent}%` }} />
      </span>
      {percent}%
    </span>
  );
}

// Trace -- как модель пришла к ответу: какие инструменты MCP-сервера звала.
function Trace({ forecast }) {
  const steps = forecast.steps ?? [];
  return (
    <div className="trace">
      {forecast.mode === 'prompt' ? (
        <span>данные переданы в промпт (модель без инструментов)</span>
      ) : (
        <span>
          вызовы MCP:{' '}
          {steps.length === 0
            ? 'нет'
            : steps.map((step, i) => (
                <code key={i}>
                  {step.tool}({step.arguments || ''})
                </code>
              ))}
        </span>
      )}
      <span>окно {forecast.windowMinutes} мин</span>
      {forecast.durationMs > 0 && <span>{(forecast.durationMs / 1000).toFixed(1)} с</span>}
      {forecast.tokens > 0 && <span>{forecast.tokens.toLocaleString('ru-RU')} токенов</span>}
    </div>
  );
}
