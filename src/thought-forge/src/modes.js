import { complete, sumUsage } from './api.js';

const STEPWISE_SYSTEM = [
  'Решай задачу строго пошагово.',
  'Сначала перечисли, что дано и что требуется найти.',
  'Затем разбей решение на пронумерованные шаги и после каждого шага выписывай',
  'промежуточный результат, а не только рассуждение.',
  'Перед финалом проверь решение подстановкой или встречной прикидкой.',
  'Последней строкой ответа напиши "Ответ: ..." -- одну строку с итогом и без пояснений.',
].join(' ');

const PROMPT_ENGINEER_SYSTEM = [
  'Ты -- инженер промптов. Тебе дают задачу пользователя.',
  'НЕ решай её. Твоя работа -- написать промпт, по которому языковая модель решит эту задачу',
  'максимально точно. В промпте укажи: подходящую роль исполнителя, метод решения,',
  'порядок шагов, типичные ошибки этого класса задач, способ самопроверки и формат ответа.',
  'Верни только текст промпта, без вступлений, комментариев и кавычек вокруг него.',
].join(' ');

const EXPERTS = [
  {
    id: 'analyst',
    title: 'Аналитик',
    system: [
      'Ты -- аналитик. Начни с разбора условия: выпиши все данные, явные и неявные допущения,',
      'что именно требуется найти и чего в условии не хватает.',
      'Только после разбора дай своё решение и итоговый ответ.',
      'Пиши сжато, без воды. Последней строкой -- "Ответ: ...".',
    ].join(' '),
  },
  {
    id: 'engineer',
    title: 'Инженер',
    system: [
      'Ты -- инженер. Тебя интересует работающая процедура, а не рассуждения вокруг задачи.',
      'Дай конкретный алгоритм или расчёт и доведи его до числа, формулы или готового ответа.',
      'Если задача вычислительная -- покажи вычисления. Если алгоритмическая -- покажи алгоритм',
      'и его сложность. Пиши сжато. Последней строкой -- "Ответ: ...".',
    ].join(' '),
  },
  {
    id: 'critic',
    title: 'Критик',
    system: [
      'Ты -- критик и скептик. Сначала перечисли ловушки этой задачи и типичные ошибки,',
      'на которых обычно спотыкаются при её решении: неверная трактовка условия,',
      'подмена вопроса, ошибки в арифметике, забытые граничные случаи.',
      'Затем реши задачу сам, обходя перечисленные ловушки.',
      'Пиши сжато. Последней строкой -- "Ответ: ...".',
    ].join(' '),
  },
];

const MODERATOR_SYSTEM = [
  'Ты -- модератор экспертного совета. Ниже даны независимые мнения трёх экспертов',
  'по одной и той же задаче. Сопоставь их: явно назови, в чём они сходятся и в чём расходятся,',
  'и если расходятся -- реши, кто прав, и объясни почему.',
  'Заверши разбор итоговым ответом. Последней строкой -- "Ответ: ..." без пояснений.',
].join(' ');

/** Последний вопрос пользователя -- нужен режимам, которые работают с одной задачей. */
function lastUserMessage(messages) {
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    if (messages[i].role === 'user') return messages[i].content;
  }
  return '';
}

function done(text, results, stages = []) {
  return {
    text,
    stages,
    usage: sumUsage(results),
    calls: results.length,
  };
}

async function runDirect({ messages }) {
  const result = await complete({ messages });
  return done(result.text, [result]);
}

async function runStepwise({ messages }) {
  const result = await complete({ messages, system: STEPWISE_SYSTEM });
  return done(result.text, [result]);
}

async function runMetaprompt({ messages, onStage }) {
  onStage('составляю промпт для решения');
  const generator = await complete({
    messages: [{ role: 'user', content: lastUserMessage(messages) }],
    system: PROMPT_ENGINEER_SYSTEM,
  });

  onStage('решаю по сгенерированному промпту');
  const solver = await complete({ messages, system: generator.text });

  return done(solver.text, [generator, solver], [
    { title: 'Промпт, который составила модель', text: generator.text },
  ]);
}

async function runCouncil({ messages, onStage }) {
  onStage('опрашиваю трёх экспертов');
  const settled = await Promise.allSettled(
    EXPERTS.map((expert) => complete({ messages, system: expert.system }))
  );

  const stages = [];
  const results = [];
  const opinions = [];

  settled.forEach((outcome, index) => {
    const expert = EXPERTS[index];
    if (outcome.status === 'fulfilled') {
      stages.push({ title: `Мнение: ${expert.title}`, text: outcome.value.text });
      results.push(outcome.value);
      opinions.push(`### ${expert.title}\n${outcome.value.text}`);
    } else {
      const message = outcome.reason?.message ?? String(outcome.reason);
      stages.push({ title: `Мнение: ${expert.title} -- ошибка`, text: message, failed: true });
    }
  });

  if (opinions.length === 0) {
    throw new Error(`Все эксперты вернули ошибку. ${settled[0].reason?.message ?? ''}`);
  }

  onStage('свожу мнения в итоговый ответ');
  const moderator = await complete({
    messages: [
      {
        role: 'user',
        content: `Задача:\n${lastUserMessage(messages)}\n\nМнения экспертов:\n\n${opinions.join('\n\n')}`,
      },
    ],
    system: MODERATOR_SYSTEM,
  });
  results.push(moderator);

  return done(moderator.text, results, stages);
}

/**
 * Реестр режимов. App.jsx рендерит переключалку через Object.entries(MODES),
 * а сам вызов делает единым контрактом: run({ messages, onStage }).
 */
export const MODES = {
  direct: {
    label: 'Прямой ответ',
    hint: 'без system prompt',
    calls: 1,
    run: runDirect,
  },
  stepwise: {
    label: 'Пошагово',
    hint: 'system prompt требует разбить решение на шаги',
    calls: 1,
    run: runStepwise,
  },
  metaprompt: {
    label: 'Предпромпт',
    hint: 'модель пишет промпт, потом решает по нему',
    calls: 2,
    run: runMetaprompt,
  },
  council: {
    label: 'Совет экспертов',
    hint: 'аналитик, инженер и критик параллельно, затем синтез',
    calls: 4,
    run: runCouncil,
  },
};

export const MODE_KEYS = Object.keys(MODES);

/** Обёртка с замером времени: латентность режима -- это время всей цепочки вызовов. */
export async function runMode(modeKey, { messages, onStage = () => {} }) {
  const startedAt = performance.now();
  const result = await MODES[modeKey].run({ messages, onStage });
  return { ...result, latencyMs: Math.round(performance.now() - startedAt) };
}
