import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  parseRoute,
  routeHash,
  readHash,
  plural,
  pct,
  fmtSeconds,
  sectionsLabel,
  chapterHue,
  segments,
  hitMarks,
  cutMarks,
  best,
  placeTone,
  toggleVariant,
  pickExamples,
} from './format.js';

test('маршруты туда и обратно', () => {
  assert.deepEqual(parseRoute(''), { view: 'ask', params: {} });
  assert.deepEqual(parseRoute('#/index'), { view: 'index', params: {} });
  assert.equal(parseRoute('#/quality').view, 'ask');
  const h = readHash('tom', 3, 'structure-tom-0012', 'structure');
  assert.equal(h, '#/read/tom/3?c=structure-tom-0012&v=structure');
  assert.deepEqual(parseRoute(h), { view: 'read', book: 'tom', section: 3, params: { c: 'structure-tom-0012', v: 'structure' } });
  assert.equal(routeHash('search', { q: 'забор', qid: '' }), '#/search?q=%D0%B7%D0%B0%D0%B1%D0%BE%D1%80');
  assert.equal(parseRoute(routeHash('search', { q: 'забор' })).params.q, 'забор');
  assert.equal(parseRoute('#/nonsense').view, 'ask');
});

test('склонения и числа', () => {
  assert.equal(plural(1, 'чанк', 'чанка', 'чанков'), 'чанк');
  assert.equal(plural(3, 'чанк', 'чанка', 'чанков'), 'чанка');
  assert.equal(plural(11, 'чанк', 'чанка', 'чанков'), 'чанков');
  assert.equal(plural(22, 'чанк', 'чанка', 'чанков'), 'чанка');
  assert.equal(pct(0.456), '46%');
  assert.equal(pct(undefined), '—');
  assert.equal(fmtSeconds(0.2), '200 мс');
  assert.equal(fmtSeconds(75), '1 мин 15 с');
});

test('подписи глав', () => {
  const ch = (label) => ({ label });
  assert.equal(sectionsLabel([ch('Chapter II')]), 'гл. II');
  assert.equal(sectionsLabel([ch('Chapter I'), ch('Chapter II')]), 'гл. I → II');
  assert.equal(sectionsLabel([ch('Preface'), ch('Chapter I')]), 'предисловие → I');
  assert.equal(sectionsLabel([ch('Chapter the Last')]), 'гл. последняя');
  assert.equal(chapterHue('tom', 2), chapterHue('tom', 2));
  assert.notEqual(chapterHue('tom', 2), chapterHue('huck', 2));
});

test('абзац режется по границам отметок', () => {
  const para = { start: 100, end: 120, text: 'abcdefghijklmnopqrst' };
  const marks = [...hitMarks({ start: 90, bodyStart: 105, end: 110 })];
  const segs = segments(para, marks);
  assert.deepEqual(
    segs.map((s) => [s.text, s.marks.map((m) => m.kind).join('+')]),
    [
      ['abcde', 'overlap'],
      ['fghij', 'hit'],
      ['klmnopqrst', ''],
    ]
  );
  // склейка кусков -- исходный текст
  assert.equal(segs.map((s) => s.text).join(''), para.text);
});

test('нарезка и найденный чанк вместе', () => {
  const para = { start: 0, end: 10, text: '0123456789' };
  const cuts = cutMarks([
    { bodyStart: 0, end: 6, rank: 4 },
    { bodyStart: 6, end: 10, rank: 5 },
  ]);
  const segs = segments(para, [...cuts, ...hitMarks({ start: 4, bodyStart: 4, end: 8 })]);
  assert.deepEqual(
    segs.map((s) => s.text),
    ['0123', '45', '67', '89']
  );
  assert.equal(segs[1].marks.length, 2); // и нарезка, и найденное
  assert.equal(segs[2].marks.find((m) => m.kind === 'cut').idx, 5);
});

test('лучшее значение и тон места', () => {
  assert.equal(best([0.5, 0.7, undefined]), 0.7);
  assert.equal(best([3, 1, 2], false), 1);
  assert.equal(best([0.5]), undefined);
  assert.equal(placeTone(1), 'good');
  assert.equal(placeTone(4), 'fair');
  assert.equal(placeTone(0), 'bad');
});

test('выбор вариантов: не больше трёх, не меньше одного', () => {
  assert.deepEqual(toggleVariant(['a', 'b', 'c'], 'd'), ['b', 'c', 'd']);
  assert.deepEqual(toggleVariant(['a', 'b'], 'a'), ['b']);
  assert.deepEqual(toggleVariant(['a'], 'a'), ['a']);
});

test('примеры вопросов поровну из книг', () => {
  const qs = [
    { id: 't1', book: 'tom', valid: true },
    { id: 't2', book: 'tom', valid: true },
    { id: 't3', book: 'tom', valid: false },
    { id: 'h1', book: 'huck', valid: true },
  ];
  assert.deepEqual(
    pickExamples(qs, 3).map((q) => q.id),
    ['t1', 'h1', 't2']
  );
});

import { splitCitations, queryModeTitle } from './format.js';

test('сноски в ответе', () => {
  assert.deepEqual(splitCitations('Яблоко [1]. И змей [2, 3][4].'), [
    { text: 'Яблоко ' },
    { cite: 1 },
    { text: '. И змей ' },
    { cite: 2 },
    { cite: 3 },
    { cite: 4 },
    { text: '.' },
  ]);
  assert.deepEqual(splitCitations('без сносок'), [{ text: 'без сносок' }]);
  assert.deepEqual(splitCitations(''), []);
});

test('подпись режима поиска', () => {
  assert.equal(queryModeTitle({ query: 'en', hybrid: true }), 'перевод на английский · + BM25');
  assert.equal(queryModeTitle({ query: 'raw', parent: 'structure' }), 'small-to-big · вопрос как есть');
});

import { funnelLine, sameSettings, clampSettings } from './format.js';

test('строка воронки', () => {
  assert.equal(
    funnelLine({ total: 20, passedSim: 14, passedRel: 5, kept: 3 }),
    '20 кандидатов → 14 прошли порог косинуса → 5 одобрил реранкер → 3 в ответе'
  );
  assert.equal(funnelLine({ total: 20, passedSim: 20, passedRel: 0, kept: 0 }), '20 кандидатов → 0 одобрил реранкер → 0 в ответе');
});

test('настройки: сравнение и границы', () => {
  const a = { query: 'hyde', kBefore: 20, simMin: 0.3, relMin: 0.5, kAfter: 5 };
  assert.ok(sameSettings(a, { ...a, simMin: '0.3' }));
  assert.ok(!sameSettings(a, { ...a, relMin: 0.6 }));
  assert.deepEqual(clampSettings({ query: 'x', kBefore: 99, simMin: -1, relMin: 'abc', kAfter: 3.6 }), {
    query: 'hyde', kBefore: 50, simMin: 0, relMin: 0.5, kAfter: 4, order: 'cosine',
  });
});
