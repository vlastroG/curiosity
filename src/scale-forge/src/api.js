import { PROVIDERS } from './models.js';

/**
 * Один запрос к модели. Провайдера (адрес + ключ) и model id берём из реестра моделей:
 * DeepSeek и OpenRouter оба говорят на OpenAI-совместимом протоколе, поэтому тело запроса
 * и разбор ответа общие -- отличаются только эндпоинт, ключ и пара заголовков.
 *
 * temperature фиксирована для всех трёх моделей: день 5 сравнивает классы моделей,
 * а не настройки сэмплинга (это была тема дня 4).
 */
export const TEMPERATURE = 0.3;

/**
 * Требования к оформлению, одинаковые для всех трёх моделей. Про то, КАК отвечать
 * по существу, здесь ничего нет -- только формат, иначе рассуждающие модели сыплют
 * LaTeX, который в узкой колонке нечитаем. Промпт общий, поэтому сравнение честное:
 * различается только сама модель.
 */
export const SHARED_SYSTEM = [
  'Отвечай на русском языке в простом Markdown.',
  'Разрешены заголовки уровня ### и ниже, списки, **жирный**, *курсив*,',
  '`моноширинный` и блоки кода в тройных апострофах.',
  'Формулы записывай обычным текстом в одну строку (например: t = 1500 / 20 = 75 с).',
  'Не используй LaTeX и таблицы -- ответ показывается в узкой колонке.',
].join(' ');

export async function complete({ model, messages }) {
  const provider = PROVIDERS[model.provider];
  const apiKey = window._env_?.[provider.envKey];

  if (!apiKey) {
    throw new Error(
      `${provider.envKey} не задан. Добавьте ключ в .env в корне репозитория и пересоберите контейнер.`
    );
  }

  const headers = {
    Authorization: `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
  };

  // OpenRouter просит эти заголовки для атрибуции запроса; на работу они не влияют
  if (model.provider === 'openrouter') {
    headers['HTTP-Referer'] = window.location.origin;
    headers['X-Title'] = 'Scale Forge';
  }

  const body = {
    model: model.model,
    messages: [
      { role: 'system', content: SHARED_SYSTEM },
      ...messages.map(({ role, content }) => ({ role, content })),
    ],
    stream: false,
    temperature: TEMPERATURE,
    max_tokens: model.maxTokens,
  };

  const startedAt = performance.now();
  const response = await fetch(provider.endpoint, {
    method: 'POST',
    headers,
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const errorBody = await response.text();
    throw new Error(`${provider.title} error ${response.status}: ${errorBody}`);
  }

  const data = await response.json();
  const latencyMs = Math.round(performance.now() - startedAt);

  // OpenRouter отдаёт ошибку с кодом 200 в теле ответа, если модель недоступна
  if (data.error) {
    throw new Error(`${provider.title}: ${data.error.message ?? JSON.stringify(data.error)}`);
  }

  const choice = data.choices?.[0];
  if (!choice) {
    throw new Error(`${provider.title}: ответ без choices -- ${JSON.stringify(data).slice(0, 300)}`);
  }

  return {
    text: choice.message?.content ?? '',
    finishReason: choice.finish_reason,
    usage: data.usage,
    // у рассуждающих моделей часть completion_tokens уходит на внутреннее рассуждение
    reasoningTokens: data.usage?.completion_tokens_details?.reasoning_tokens ?? null,
    latencyMs,
  };
}
