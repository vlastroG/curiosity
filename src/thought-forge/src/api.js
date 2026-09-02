const ENDPOINT = 'https://api.deepseek.com/chat/completions';
const MODEL = 'deepseek-v4-flash';

/**
 * Параметры сэмплинга одинаковы для всех режимов и не выведены в интерфейс:
 * сравнение должно показывать разницу от system prompt, а не от температуры.
 */
export const TEMPERATURE = 0.2;
/**
 * Модель рассуждающая: max_tokens -- общий бюджет на внутреннее рассуждение
 * (reasoning_content) и на сам ответ. Если рассуждение выбирает весь лимит,
 * приходит finish_reason "length" и пустой content, поэтому запас нужен щедрый.
 */
export const MAX_TOKENS = 4000;

/**
 * Один запрос к DeepSeek. system опционален: если его нет, system-сообщение
 * в запрос не уходит вовсе -- так работает режим "прямой ответ".
 */
export async function complete({ messages, system }) {
  const apiKey = window._env_?.DEEPSEEK_API_KEY;
  if (!apiKey) {
    throw new Error(
      'DEEPSEEK_API_KEY is not set. Start the container with -e DEEPSEEK_API_KEY=<your key>.'
    );
  }

  const payload = [];
  if (system) payload.push({ role: 'system', content: system });
  for (const message of messages) {
    payload.push({ role: message.role, content: message.content });
  }

  const body = {
    model: MODEL,
    messages: payload,
    stream: false,
    temperature: TEMPERATURE,
    max_tokens: MAX_TOKENS,
  };

  const startedAt = performance.now();
  const response = await fetch(ENDPOINT, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${apiKey}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const errorBody = await response.text();
    throw new Error(`DeepSeek API error ${response.status}: ${errorBody}`);
  }

  const data = await response.json();

  return {
    text: data.choices[0].message.content ?? '',
    finishReason: data.choices[0].finish_reason,
    usage: data.usage,
    latencyMs: Math.round(performance.now() - startedAt),
  };
}

/** Складывает usage нескольких вызовов -- у многошаговых режимов их больше одного. */
export function sumUsage(results) {
  return results.reduce(
    (acc, result) => ({
      prompt_tokens: acc.prompt_tokens + (result.usage?.prompt_tokens ?? 0),
      completion_tokens: acc.completion_tokens + (result.usage?.completion_tokens ?? 0),
      total_tokens: acc.total_tokens + (result.usage?.total_tokens ?? 0),
    }),
    { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 }
  );
}
