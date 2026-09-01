import jsYaml from 'js-yaml';

const VOID_TAGS = new Set([
  'area', 'base', 'br', 'col', 'embed', 'hr', 'img',
  'input', 'link', 'meta', 'source', 'track', 'wbr',
]);

const ok = (message) => ({ ok: true, message });
const fail = (message) => ({ ok: false, message });

function validateJson(text) {
  try {
    JSON.parse(text);
    return ok('валидный JSON');
  } catch (err) {
    return fail(err.message);
  }
}

function validateYaml(text) {
  try {
    const value = jsYaml.load(text);
    if (value === undefined) return fail('пустой YAML-документ');
    if (typeof value === 'string') return fail('YAML разобран как обычная строка, а не структура');
    return ok('валидный YAML');
  } catch (err) {
    return fail(err.message);
  }
}

function validateHtml(text) {
  const stripped = text.replace(/<!--[\s\S]*?-->/g, '');
  const tagRe = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)((?:"[^"]*"|'[^']*'|[^'">])*)>/g;
  const stack = [];
  let tagCount = 0;
  let match;

  while ((match = tagRe.exec(stripped)) !== null) {
    const isClosing = match[1] === '/';
    const tag = match[2].toLowerCase();
    const selfClosing = /\/\s*$/.test(match[3]);
    tagCount += 1;

    if (VOID_TAGS.has(tag) || (!isClosing && selfClosing)) continue;

    if (isClosing) {
      if (stack.length === 0) return fail(`закрывающий </${tag}> без открывающего тега`);
      const expected = stack.pop();
      if (expected !== tag) return fail(`ожидался </${expected}>, а встретился </${tag}>`);
    } else {
      stack.push(tag);
    }
  }

  if (tagCount === 0) return fail('в ответе нет HTML-тегов');
  if (stack.length > 0) return fail(`не закрыт тег <${stack[stack.length - 1]}>`);

  const doc = new DOMParser().parseFromString(text, 'text/html');
  if (doc.body.children.length === 0) return fail('парсер не нашёл ни одного элемента');

  return ok('валидный HTML-фрагмент');
}

function validateMarkdown(text) {
  const trimmed = text.trim();
  if (trimmed.startsWith('```') && trimmed.endsWith('```')) {
    return fail('весь ответ обёрнут в код-фенс');
  }
  const hasStructure =
    /^#{1,6}\s/m.test(trimmed) ||
    /^\s*([-*+]|\d+\.)\s/m.test(trimmed) ||
    /^\s*\|.*\|/m.test(trimmed) ||
    /\*\*[^*]+\*\*/.test(trimmed) ||
    /^```/m.test(trimmed);
  if (!hasStructure) return fail('нет ни одной Markdown-конструкции');
  return ok('похоже на Markdown (эвристика)');
}

const VALIDATORS = {
  json: validateJson,
  yaml: validateYaml,
  html: validateHtml,
  markdown: validateMarkdown,
};

/**
 * Проверяет сырой текст ответа как есть -- без снятия код-фенсов.
 * Возвращает null, если формат не задан и проверять нечего.
 */
export function validateOutput(format, text) {
  const validator = VALIDATORS[format];
  if (!validator) return null;
  if (!text || !text.trim()) return fail('пустой ответ');
  return validator(text);
}
