/**
 * Разбор Markdown в описания блоков. Отрисовкой занимается markdown.jsx.
 *
 * Разделено надвое сознательно: разбор -- чистая функция без React и без DOM,
 * поэтому его можно гонять тестами за долю секунды (`npm test`), не поднимая
 * ни браузера, ни бэкенда, ни тем более модели. Таблицы дважды уезжали сломанными
 * именно потому, что проверить их было нечем, кроме как заказать модели новый план.
 *
 * Библиотеку не берём по двум причинам: нужен небольшой поднабор разметки,
 * и вывод модели нельзя вставлять как HTML.
 */

// Таблицы в стиле GFM: модель охотно сводит материалы и расход в таблицу.
const TABLE_DELIM_CELL = /^:?-+:?$/;

const HEADING = /^(#{1,4})\s+(.*)$/;
const BULLET = /^\s*[-*+]\s+(.*)$/;
const ORDERED = /^\s*(\d+)[.)]\s+(.*)$/;
const RULE = /^\s*([-*_])\s*\1\s*\1[\s\-*_]*$/;

/**
 * splitRow -- ячейки строки таблицы.
 *
 * Крайние палки необязательны (GFM допускает обе формы), экранированная палка
 * остаётся текстом ячейки и её не делит.
 */
export function splitRow(line) {
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

/**
 * alignments -- выравнивание колонок из строки-разделителя.
 *
 * null означает «это не разделитель»: по нему и опознаётся таблица, поэтому
 * абзац со случайной палкой таблицей не станет.
 */
export function alignments(line) {
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

/** Ячейки строки, подогнанные под ширину шапки: сетка не должна разъезжаться. */
function cellsOf(row, width) {
  const cells = splitRow(row);
  while (cells.length < width) cells.push('');
  return cells.slice(0, width);
}

/**
 * parseBlocks разбирает текст в плоский список описаний блоков.
 *
 * Виды блоков: code, rule, table, heading, list, paragraph. Внутри блоков текст
 * остаётся текстом -- разбор строчной разметки (жирное, курсив, код) живёт
 * в отрисовке, потому что там он сразу превращается в React-узлы.
 */
export function parseBlocks(text) {
  if (!text) return [];

  const lines = text.replace(/\r\n/g, '\n').split('\n');
  const blocks = [];
  let paragraph = [];
  let list = null;

  const flushParagraph = () => {
    if (paragraph.length === 0) return;
    blocks.push({ type: 'paragraph', text: paragraph.join(' ') });
    paragraph = [];
  };

  const flushList = () => {
    if (list === null) return;
    blocks.push(list);
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
      blocks.push({ type: 'code', text: code.join('\n') });
      continue;
    }

    if (line.trim() === '') {
      flushAll();
      continue;
    }

    if (RULE.test(line)) {
      flushAll();
      blocks.push({ type: 'rule' });
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

        blocks.push({ type: 'table', header, align, rows });
        continue;
      }
    }

    const heading = line.match(HEADING);
    if (heading) {
      flushAll();
      blocks.push({
        type: 'heading',
        level: Math.min(heading[1].length + 2, 6),
        text: heading[2],
      });
      continue;
    }

    const ordered = line.match(ORDERED);
    if (ordered) {
      flushParagraph();
      if (list === null || !list.ordered) {
        flushList();
        list = { type: 'list', ordered: true, start: Number(ordered[1]), items: [] };
      }
      list.items.push(ordered[2]);
      continue;
    }

    const bullet = line.match(BULLET);
    if (bullet) {
      flushParagraph();
      if (list === null || list.ordered) {
        flushList();
        list = { type: 'list', ordered: false, items: [] };
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
  return blocks;
}
