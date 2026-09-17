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

// Таблицы в стиле GFM. Модель охотно сводит материалы и расход в таблицу -- и без
// поддержки здесь строки с палками склеивались в один абзац, то есть ровно та часть
// плана, ради которой таблицу и просили, читалась хуже всего.
const TABLE_DELIM_CELL = /^:?-+:?$/;

// splitRow -- ячейки строки таблицы. Крайние палки необязательны, экранированная
// палка остаётся текстом ячейки
function splitRow(line) {
  let row = line.trim();
  if (row.startsWith('|')) row = row.slice(1);
  if (row.endsWith('|') && !row.endsWith('\\|')) row = row.slice(0, -1);

  const cells = [];
  let cell = '';
  for (let i = 0; i < row.length; i += 1) {
    if (row[i] === '\\' && row[i + 1] === '|') {
      cell += '|';
      i += 1;
      continue;
    }
    if (row[i] === '|') {
      cells.push(cell.trim());
      cell = '';
      continue;
    }
    cell += row[i];
  }
  cells.push(cell.trim());
  return cells;
}

// alignments -- выравнивание колонок из строки-разделителя. null, если строка
// разделителем не является: по ней и опознаётся таблица
function alignments(line) {
  if (!line || !line.includes('-')) return null;

  const align = [];
  for (const cell of splitRow(line)) {
    if (!TABLE_DELIM_CELL.test(cell)) return null;
    if (cell.startsWith(':') && cell.endsWith(':')) align.push('center');
    else if (cell.endsWith(':')) align.push('right');
    else align.push(undefined);
  }
  return align;
}

/** Ячейки одной строки, подогнанные под ширину шапки: сетка не должна разъезжаться. */
function cellsOf(row, width) {
  const cells = splitRow(row);
  while (cells.length < width) cells.push('');
  return cells.slice(0, width);
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

    // таблица: строка с палками, под которой лежит разделитель той же ширины
    if (line.includes('|')) {
      const align = alignments(lines[i + 1]);
      const header = splitRow(line);

      if (align && align.length === header.length && header.length > 1) {
        flushAll();

        const rows = [];
        i += 2;
        while (i < lines.length && lines[i].trim() !== '' && lines[i].includes('|')) {
          rows.push(cellsOf(lines[i], header.length));
          i += 1;
        }
        i -= 1; // цикл сам прибавит единицу, а эта строка таблице уже не принадлежит

        blocks.push(
          <div key={blocks.length} className="md-table-wrap">
            <table className="md-table">
              <thead>
                <tr>
                  {header.map((cell, index) => (
                    <th key={index} style={{ textAlign: align[index] }}>
                      {inline(cell)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row, rowIndex) => (
                  <tr key={rowIndex}>
                    {row.map((cell, index) => (
                      <td key={index} style={{ textAlign: align[index] }}>
                        {inline(cell)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
        continue;
      }
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
