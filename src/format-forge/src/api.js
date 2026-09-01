const ENDPOINT = 'https://api.deepseek.com/chat/completions';
const MODEL = 'deepseek-chat';

/**
 * Один запрос к DeepSeek. Все ограничения опциональны: если параметр не передан,
 * поле вообще не попадает в тело запроса. Левая панель зовёт эту функцию только
 * с prompt, правая -- со всем набором.
 */
export async function complete({ prompt, system, temperature, maxTokens, stop, jsonMode }) {
  const apiKey = window._env_?.DEEPSEEK_API_KEY;
  if (!apiKey) {
    throw new Error(
      'DEEPSEEK_API_KEY is not set. Start the container with -e DEEPSEEK_API_KEY=<your key>.'
    );
  }

  const messages = [];
  if (system) messages.push({ role: 'system', content: system });
  messages.push({ role: 'user', content: prompt });

  const body = { model: MODEL, messages, stream: false };
  if (typeof temperature === 'number') body.temperature = temperature;
  if (typeof maxTokens === 'number') body.max_tokens = maxTokens;
  if (stop && stop.length > 0) body.stop = stop.slice(0, 16);
  if (jsonMode) body.response_format = { type: 'json_object' };

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
  const latencyMs = Math.round(performance.now() - startedAt);

  return {
    text: data.choices[0].message.content,
    finishReason: data.choices[0].finish_reason,
    usage: data.usage,
    latencyMs,
    requestBody: body,
  };
}
