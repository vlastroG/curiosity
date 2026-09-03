/**
 * Мини-рендерер Markdown в React-элементы. Библиотеку не берём по двум причинам:
 * нужен небольшой поднабор разметки, и вывод модели нельзя вставлять как HTML.
 * Здесь текст никогда не превращается в разметку -- только в React-узлы.
 */

const INLINE = /(\*\*[^*\n]+\*\*|`[^`\n]+`|\*[^*\n]+\*)/g;

function inline(text) {
  return text
    .split(INLINE)
    .filter((part) => part !== '')
    .map((part, index) => {
      if (part.startsWith('**') && part.endsWith('**')) {
        return <strong key={index}>{part.slice(2, -2)}</strong>;
      }
      if (part.startsWith('`') && part.endsWith('`')) {
        return <code key={index}>{part.slice(1, -1)}</code>;
      }
      if (part.startsWith('*') && part.endsWith('*')) {
        return <em key={index}>{part.slice(1, -1)}</em>;
      }
      return part;
    });
}

const HEADING = /^(#{1,4})\s+(.*)$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const ORDERED = /^\s*(\d+)[.)]\s+(.*)$/;
const RULE = /^\s*([-*_])\s*\1\s*\1[\s\-*_]*$/;

export function Markdown({ text }) {
  if (!text) return null;

  const lines = text.replace(/\r\n/g, '\n').split('\n');
  const blocks = [];
  let paragraph = [];
  let list = null;

  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    blocks.push(
      <p key={blocks.length} className="md-p">
        {inline(paragraph.join(' '))}
      </p>
    );
    paragraph = [];
  };

  const flushList = () => {
    if (list === null) return;
    const items = list.items.map((item, index) => <li key={index}>{inline(item)}</li>);
    blocks.push(
      list.ordered ? (
        <ol key={blocks.length} className="md-list" start={list.start}>
          {items}
        </ol>
      ) : (
        <ul key={blocks.length} className="md-list">
          {items}
        </ul>
      )
    );
    list = null;
  };

  const flushAll = () => {
    flushParagraph();
    flushList();
  };

  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];

    if (line.trimStart().startsWith('```')) {
      flushAll();
      const code = [];
      i += 1;
      while (i < lines.length && !lines[i].trimStart().startsWith('```')) {
        code.push(lines[i]);
        i += 1;
      }
      blocks.push(
        <pre key={blocks.length} className="md-code">
          {code.join('\n')}
        </pre>
      );
      continue;
    }

    if (line.trim() === '') {
      flushAll();
      continue;
    }

    if (RULE.test(line)) {
      flushAll();
      blocks.push(<hr key={blocks.length} className="md-rule" />);
      continue;
    }

    const heading = line.match(HEADING);
    if (heading) {
      flushAll();
      const level = Math.min(heading[1].length + 2, 6);
      const Tag = `h${level}`;
      blocks.push(
        <Tag key={blocks.length} className="md-h">
          {inline(heading[2])}
        </Tag>
      );
      continue;
    }

    const ordered = line.match(ORDERED);
    if (ordered) {
      flushParagraph();
      if (list === null || !list.ordered) {
        flushList();
        list = { ordered: true, start: Number(ordered[1]), items: [] };
      }
      list.items.push(ordered[2]);
      continue;
    }

    const bullet = line.match(BULLET);
    if (bullet) {
      flushParagraph();
      if (list === null || list.ordered) {
        flushList();
        list = { ordered: false, items: [] };
      }
      list.items.push(bullet[1]);
      continue;
    }

    // продолжение пункта списка: строка с отступом сразу после элемента
    if (list !== null && /^\s{2,}\S/.test(line)) {
      list.items[list.items.length - 1] += ` ${line.trim()}`;
      continue;
    }

    flushList();
    paragraph.push(line.trim());
  }

  flushAll();
  return <div className="md">{blocks}</div>;
}
