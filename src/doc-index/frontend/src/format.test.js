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
  assert.deepEqual(parseRoute(''), { view: 'search', params: {} });
  assert.deepEqual(parseRoute('#/compare'), { view: 'compare', params: {} });
  const h = readHash('tom', 3, 'structure-tom-0012', 'structure');
  assert.equal(h, '#/read/tom/3?c=structure-tom-0012&v=structure');
  assert.deepEqual(parseRoute(h), { view: 'read', book: 'tom', section: 3, params: { c: 'structure-tom-0012', v: 'structure' } });
  assert.equal(routeHash('search', { q: 'забор', qid: '' }), '#/search?q=%D0%B7%D0%B0%D0%B1%D0%BE%D1%80');
  assert.equal(parseRoute(routeHash('search', { q: 'забор' })).params.q, 'забор');
  assert.equal(parseRoute('#/nonsense').view, 'search');
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
