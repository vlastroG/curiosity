import { direction, pct, price } from '../format.js';

// Карточка монеты: цена сейчас, изменение за окно, график и границы.
export default function CoinCard({ coin, points, forecast }) {
  const dir = direction(coin.changePct, 0.0005);
  return (
    <article className="coin">
      <header className="coin-head">
        <span className="coin-symbol">{coin.symbol}</span>
        {forecast && <TrendBadge trend={forecast.trend} />}
      </header>
      <div className="coin-price">{price(coin.last)} $</div>
      <div className={`coin-change ${dir}`}>{pct(coin.changePct)}</div>
      <Sparkline points={points} dir={dir} />
      <dl className="coin-stats">
        <div>
          <dt>мин</dt>
          <dd>{price(coin.min)}</dd>
        </div>
        <div>
          <dt>макс</dt>
          <dd>{price(coin.max)}</dd>
        </div>
        <div>
          <dt>волат.</dt>
          <dd>{pct(coin.volatility).replace('+', '')}</dd>
        </div>
        <div>
          <dt>точек</dt>
          <dd>{coin.points}</dd>
        </div>
      </dl>
    </article>
  );
}

export function TrendBadge({ trend }) {
  const label = { up: '↗ рост', down: '↘ падение', flat: '→ боковик' }[trend] ?? trend;
  return <span className={`badge ${trend}`}>{label}</span>;
}

// Sparkline -- линия цены за окно. Масштаб по своей монете: сравниваем форму
// движения, а не абсолютные цены.
function Sparkline({ points, dir }) {
  const width = 240;
  const height = 56;
  if (points.length < 2) {
    return (
      <svg className="spark" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
        <text x={width / 2} y={height / 2 + 4} textAnchor="middle" className="spark-empty">
          мало точек
        </text>
      </svg>
    );
  }
  const t0 = points[0].t;
  const t1 = points[points.length - 1].t;
  const values = points.map((point) => point.p);
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const x = (t) => ((t - t0) / (t1 - t0 || 1)) * width;
  const y = (p) => (hi === lo ? height / 2 : height - 4 - ((p - lo) / (hi - lo)) * (height - 8));
  const line = points.map((point, i) => `${i ? 'L' : 'M'}${x(point.t).toFixed(1)},${y(point.p).toFixed(1)}`).join(' ');
  const area = `${line} L${width},${height} L0,${height} Z`;
  const last = points[points.length - 1];
  return (
    <svg className={`spark ${dir}`} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
      <path d={area} className="spark-area" />
      <path d={line} className="spark-line" vectorEffect="non-scaling-stroke" />
      <circle cx={x(last.t)} cy={y(last.p)} r="2.5" className="spark-dot" />
    </svg>
  );
}
