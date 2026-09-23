import { clock, modelTitle, price } from '../format.js';

const ARROW = { up: '↗', down: '↘', flat: '→' };

// История прогнозов: последние десять, каждый раскрывается целиком.
export default function History({ items, models }) {
  if (items.length === 0) {
    return <div className="empty">Здесь появятся прошлые прогнозы.</div>;
  }
  return (
    <div className="history">
      {items.map((item) => {
        const body = item.body;
        return (
          <details key={item.id} className="hist">
            <summary>
              <span className="hist-time">{clock(item.createdAt)}</span>
              <span className="hist-model">{modelTitle(models, item.model)}</span>
              <span className="hist-coins">
                {(body?.coins ?? []).map((coin) => (
                  <span key={coin.symbol} className={coin.trend}>
                    {coin.symbol} {ARROW[coin.trend]}
                  </span>
                ))}
              </span>
            </summary>
            {body && (
              <div className="hist-body">
                <p>{body.overview}</p>
                <ul>
                  {body.coins.map((coin) => (
                    <li key={coin.symbol}>
                      <b>{coin.symbol}</b> {price(coin.price)} → {price(coin.low)}–{price(coin.high)},{' '}
                      уверенность {Math.round(coin.confidence * 100)}% — {coin.reason}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </details>
        );
      })}
    </div>
  );
}
