import { test } from 'node:test';
import assert from 'node:assert/strict';

import { parseBlocks, splitRow, alignments } from './markdown.js';

/**
 * Тесты разбора Markdown.
 *
 * Гоняются встроенным в Node раннером: `npm test`. Ни сети, ни ключей, ни браузера,
 * ни модели -- а до их появления единственным способом проверить рендер таблицы было
 * заказать модели новый план работ и посмотреть глазами. Поэтому таблицы и уехали
 * сломанными дважды.
 */

const tables = (text) => parseBlocks(text).filter((block) => block.type === 'table');

test('таблица из живого ответа модели разбирается целиком', () => {
  // ровно тот текст, который в интерфейсе показывался простынёй из палок
  const text = [
    '**Материалы (с запасом)**',
    '',
    '| Материал | Зачем | Расчёт на 1,35 м² | Брать |',
    '|---|---|---|---|',
    '| Краска акриловая для внутренних работ | 2 слоя | 0,12–0,15 л/м² на слой | Банка **0,9–1 л** |',
    '| Грунтовка акриловая | Сцепление краски | ~0,2–0,3 л на 2 прохода | 1 л |',
    '',
    'Важно про пену: проверьте срок годности.',
  ].join('\n');

  const blocks = parseBlocks(text);
  const table = blocks.find((block) => block.type === 'table');

  assert.ok(table, 'таблица должна опознаться');
  assert.deepEqual(table.header, ['Материал', 'Зачем', 'Расчёт на 1,35 м²', 'Брать']);
  assert.equal(table.rows.length, 2);
  assert.equal(table.rows[0][3], 'Банка **0,9–1 л**');

  // соседние блоки не съедены таблицей
  assert.equal(blocks[0].type, 'paragraph');
  assert.equal(blocks.at(-1).text, 'Важно про пену: проверьте срок годности.');
});

test('крайние палки необязательны', () => {
  const withPipes = tables('| а | б |\n|---|---|\n| 1 | 2 |')[0];
  const without = tables('а | б\n---|---\n1 | 2')[0];

  assert.deepEqual(withPipes.header, ['а', 'б']);
  assert.deepEqual(without.header, ['а', 'б']);
  assert.deepEqual(without.rows, [['1', '2']]);
});

test('выравнивание читается из строки-разделителя', () => {
  const table = tables('| л | ц | п |\n| :--- | :---: | ---: |\n| 1 | 2 | 3 |')[0];

  assert.deepEqual(table.align, [undefined, 'center', 'right']);
});

test('экранированная палка остаётся текстом ячейки', () => {
  assert.deepEqual(splitRow('| а \\| б | в |'), ['а | б', 'в']);
});

test('строка короче шапки добивается пустыми ячейками', () => {
  // иначе сетка разъезжается: в одной строке три колонки, в другой две
  const table = tables('| а | б | в |\n|---|---|---|\n| 1 | 2 |\n| 1 | 2 | 3 | 4 |')[0];

  assert.deepEqual(table.rows[0], ['1', '2', '']);
  assert.deepEqual(table.rows[1], ['1', '2', '3'], 'лишние ячейки отбрасываются');
});

test('абзац с палкой без разделителя таблицей не становится', () => {
  const text = 'Ставим маяки | шаг 1,2 м | по уровню.\nДальше обычный текст.';
  const blocks = parseBlocks(text);

  assert.equal(tables(text).length, 0);
  assert.equal(blocks.length, 1);
  assert.equal(blocks[0].type, 'paragraph');
});

test('разделителем считается только строка из чёрточек и двоеточий', () => {
  assert.equal(alignments('| а | б |'), null);
  assert.equal(alignments('|--- | не разделитель |'), null);
  assert.equal(alignments(undefined), null, 'таблица в самом конце текста не должна падать');
  assert.deepEqual(alignments('|---|---|'), [undefined, undefined]);
});

test('таблица в конце текста без пустой строки после не теряется', () => {
  const table = tables('| а | б |\n|---|---|\n| 1 | 2 |')[0];

  assert.equal(table.rows.length, 1);
});

test('таблица закрывает предыдущий блок, а не прилипает к нему', () => {
  const text = [
    '## Материалы',
    '- первый пункт',
    '| а | б |',
    '|---|---|',
    '| 1 | 2 |',
    'абзац после таблицы',
  ].join('\n');

  const types = parseBlocks(text).map((block) => block.type);
  assert.deepEqual(types, ['heading', 'list', 'table', 'paragraph']);
});

test('остальная разметка не сломалась', () => {
  const text = [
    '# Заголовок',
    '',
    'Абзац с переносом',
    'на две строки.',
    '',
    '1. первый',
    '2. второй',
    '',
    '---',
    '',
    '```',
    'код как есть | с палкой',
    '```',
  ].join('\n');

  const blocks = parseBlocks(text);
  assert.deepEqual(
    blocks.map((block) => block.type),
    ['heading', 'paragraph', 'list', 'rule', 'code']
  );
  assert.equal(blocks[1].text, 'Абзац с переносом на две строки.');
  assert.equal(blocks[2].ordered, true);
  assert.equal(blocks[4].text, 'код как есть | с палкой', 'внутри кода таблиц не ищем');
});
