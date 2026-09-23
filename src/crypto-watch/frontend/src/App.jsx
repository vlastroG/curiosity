import { useCallback, useEffect, useRef, useState } from 'react';
import { getForecasts, getModels, getState, runForecast, saveSettings } from './api.js';
import Controls from './components/Controls.jsx';
import CoinCard from './components/CoinCard.jsx';
import ForecastPanel from './components/ForecastPanel.jsx';
import History from './components/History.jsx';
import { ago, clock, modelTitle, until } from './format.js';

// Страница опрашивает бэкенд сама: пока идёт прогноз -- часто, чтобы результат
// появился сразу, в остальное время -- раз в 10 секунд. Сбор идёт не чаще раза
// в минуту, опрашивать чаще незачем.
const POLL_IDLE = 10_000;
const POLL_BUSY = 2_000;

export default function App() {
  const [state, setState] = useState(null);
  const [models, setModels] = useState([]);
  const [history, setHistory] = useState([]);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [now, setNow] = useState(Date.now());
  const lastForecastId = useRef(null);

  const refresh = useCallback(async () => {
    try {
      const next = await getState();
      setState(next);
      setError('');
      const id = next.forecast?.id ?? null;
      if (id !== lastForecastId.current) {
        lastForecastId.current = id;
        getForecasts(10).then(setHistory).catch(() => {});
      }
    } catch (err) {
      setError(`Бэкенд недоступен: ${err.message}`);
    }
  }, []);

  useEffect(() => {
    getModels().then(setModels).catch(() => {});
    refresh();
  }, [refresh]);

  const running = state?.status?.running;
  useEffect(() => {
    const timer = setInterval(refresh, running ? POLL_BUSY : POLL_IDLE);
    return () => clearInterval(timer);
  }, [refresh, running]);

  // секундные часы -- для обратного отсчёта до следующего прогноза
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  async function change(patch) {
    setSaving(true);
    try {
      await saveSettings({ ...state.settings, ...patch });
      await refresh();
    } catch (err) {
      setError(err.message);
    } finally {
      setSaving(false);
    }
  }

  async function forecastNow() {
    try {
      await runForecast();
      setState((prev) => ({ ...prev, status: { ...prev.status, running: true } }));
      setTimeout(refresh, 500);
    } catch (err) {
      setError(err.message);
    }
  }

  const coins = state?.summary?.coins ?? [];
  const lines = Object.fromEntries((state?.series?.lines ?? []).map((line) => [line.symbol, line.points]));
  const forecast = state?.forecast?.body ?? null;
  const forecastBySymbol = Object.fromEntries((forecast?.coins ?? []).map((coin) => [coin.symbol, coin]));

  return (
    <div className="page">
      <header className="top">
        <div className="brand">
          <span className="brand-mark">◆</span>
          <span>Crypto Watch</span>
          <span className="brand-sub">MCP-планировщик + LLM-прогноз, 24/7</span>
        </div>
        {state && (
          <Controls
            settings={state.settings}
            models={models}
            saving={saving}
            running={running}
            onChange={change}
            onForecast={forecastNow}
          />
        )}
      </header>

      {error && <div className="banner banner-error">{error}</div>}
      {state?.mcpError && <div className="banner banner-warn">MCP-сервер: {state.mcpError}</div>}

      {state && <StatusLine state={state} now={now} />}

      <section>
        <h2>
          Сейчас <span className="h-note">за последние {state?.settings.windowMinutes ?? '…'} мин</span>
        </h2>
        {coins.length === 0 ? (
          <div className="empty">
            {state ? 'Планировщик ещё не собрал данных — первая точка появится в течение минуты.' : 'Загрузка…'}
          </div>
        ) : (
          <div className="coins">
            {coins.map((coin) => (
              <CoinCard key={coin.symbol} coin={coin} points={lines[coin.symbol] ?? []} forecast={forecastBySymbol[coin.symbol]} />
            ))}
          </div>
        )}
      </section>

      <section>
        <h2>
          Прогноз на {state?.horizonMinutes ?? 30} мин{' '}
          {state?.forecast && (
            <span className="h-note">
              сделан {ago(state.forecast.createdAt, now)} · {modelTitle(models, state.forecast.model)}
            </span>
          )}
        </h2>
        <ForecastPanel forecast={forecast} running={running} error={state?.status?.lastError} />
      </section>

      <section>
        <h2>История прогнозов</h2>
        <History items={history} models={models} />
      </section>

      <footer className="foot">
        Учебный прогноз языковой модели по минутным ценам Binance. Не инвестиционный совет.
      </footer>
    </div>
  );
}

function StatusLine({ state, now }) {
  const { schedule, status, settings } = state;
  return (
    <div className="status">
      <span>
        <b>Сбор</b> каждые {schedule?.intervalMinutes ?? settings.intervalMinutes} мин · последний{' '}
        {clock(schedule?.lastSuccessAt)} · следующий через {untilOf(schedule?.nextRunAt, now)}
        {schedule?.lastError && <span className="bad"> · ошибка: {schedule.lastError}</span>}
      </span>
      <span>
        <b>Агент</b>{' '}
        {status.running ? (
          <span className="pulse">делает прогноз…</span>
        ) : (
          <>следующий прогноз через {untilOf(status.nextRunAt, now)}</>
        )}
        {!status.scheduleSynced && <span className="bad"> · частота не передана MCP-серверу</span>}
      </span>
    </div>
  );
}

function untilOf(iso, now) {
  return iso ? until(iso, now) : '—';
}
