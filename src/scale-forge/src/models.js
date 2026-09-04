/**
 * Три модели разного класса под один и тот же запрос.
 *
 * Слабая идёт через OpenRouter (бесплатный тариф), средняя и сильная -- через прямой
 * API DeepSeek. Поэтому у каждой модели свой провайдер: адрес и ключ отличаются,
 * а формат запроса и ответа у обоих OpenAI-совместимый.
 */

export const PROVIDERS = {
  deepseek: {
    title: 'DeepSeek API',
    endpoint: 'https://api.deepseek.com/chat/completions',
    envKey: 'DEEPSEEK_API_KEY',
  },
  openrouter: {
    title: 'OpenRouter',
    endpoint: 'https://openrouter.ai/api/v1/chat/completions',
    envKey: 'OPENROUTER_API_KEY',
  },
};

/**
 * Цены за 1 млн токенов в долларах. Источник -- каталог OpenRouter
 * (GET https://openrouter.ai/api/v1/models), снято 2026-09-04.
 *
 * Важная оговорка: средняя и сильная модели вызываются напрямую через API DeepSeek,
 * а цены взяты из каталога OpenRouter, который перепродаёт те же модели. Прямой прайс
 * DeepSeek может отличаться, так что абсолютные суммы здесь -- оценка. Для задания это
 * не критично: сравниваются порядки величин между классами моделей, а они различаются
 * в десятки раз.
 */
export const MODELS = [
  {
    id: 'weak',
    tier: 'слабая',
    title: 'LFM2.5-2.6B',
    subtitle: 'liquid/lfm-2.5-2.6b:free · 2.6B параметров',
    provider: 'openrouter',
    model: 'liquid/lfm-2.5-2.6b:free',
    // бесплатный тариф OpenRouter: и вход, и выход стоят 0
    priceIn: 0,
    priceOut: 0,
    // у модели контекст 65k, просить 100k токенов ответа нельзя
    maxTokens: 4096,
  },
  {
    id: 'medium',
    tier: 'средняя',
    title: 'DeepSeek V4 Flash',
    subtitle: 'deepseek-v4-flash · быстрая рассуждающая',
    provider: 'deepseek',
    model: 'deepseek-v4-flash',
    priceIn: 0.08708,
    priceOut: 0.17416,
    maxTokens: 100000,
  },
  {
    id: 'strong',
    tier: 'сильная',
    title: 'DeepSeek V4 Pro',
    subtitle: 'deepseek-v4-pro · флагманская рассуждающая',
    provider: 'deepseek',
    model: 'deepseek-v4-pro',
    priceIn: 1.039302,
    priceOut: 2.078604,
    maxTokens: 100000,
  },
];

/** Стоимость одного ответа в долларах по фактическому usage. */
export function costOf(model, usage) {
  if (!usage) return 0;
  const input = ((usage.prompt_tokens ?? 0) / 1e6) * model.priceIn;
  const output = ((usage.completion_tokens ?? 0) / 1e6) * model.priceOut;
  return input + output;
}
