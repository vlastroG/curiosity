export const FORMATS = {
  none: {
    label: 'Без формата',
    instruction: null,
  },
  json: {
    label: 'JSON',
    instruction:
      'Reply with a single valid JSON object. Output raw json only: no markdown code fences, ' +
      'no explanations, no prose before or after the object. The whole response must parse ' +
      'with JSON.parse.',
  },
  markdown: {
    label: 'Markdown',
    instruction:
      'Reply in well-formed Markdown. Use headings, lists or a table to structure the answer. ' +
      'Do not wrap the whole response in a code fence and do not add prose outside the Markdown.',
  },
  html: {
    label: 'HTML',
    instruction:
      'Reply with a valid HTML fragment. Every element must be properly nested and closed. ' +
      'Output raw HTML only: no markdown code fences, no explanations, no <html>/<head>/<body> ' +
      'wrapper, no prose before or after the fragment.',
  },
  yaml: {
    label: 'YAML',
    instruction:
      'Reply with a single valid YAML document. Output raw YAML only: no markdown code fences, ' +
      'no explanations, no prose before or after the document.',
  },
};

/**
 * Собирает system prompt правой панели. Возвращает null, если ограничений нет --
 * тогда system-сообщение в запрос не уходит вовсе.
 */
export function buildSystemPrompt({ format, maxWords }) {
  const parts = [];
  const instruction = FORMATS[format]?.instruction;
  if (instruction) parts.push(instruction);
  if (typeof maxWords === 'number' && maxWords > 0) {
    parts.push(`Answer in no more than ${maxWords} words.`);
  }
  return parts.length > 0 ? parts.join('\n') : null;
}

/** DeepSeek требует, чтобы слово "json" встречалось в промпте при response_format=json_object. */
export function needsJsonMode(format) {
  return format === 'json';
}
