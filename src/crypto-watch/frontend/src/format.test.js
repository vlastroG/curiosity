import test from 'node:test';
import assert from 'node:assert/strict';
import { pct, direction, rangeLayout, until } from './format.js';

test('pct: знак и запятая', () => {
  assert.equal(pct(0.1234), '+0,123 %');
  assert.equal(pct(-0.05), '−0,050 %');
  assert.equal(pct(0), '0,000 %');
  assert.equal(pct(null), '—');
});

test('direction', () => {
  assert.equal(direction(1), 'up');
  assert.equal(direction(-1), 'down');
  assert.equal(direction(0.001, 0.01), 'flat');
});

test('rangeLayout: цена и диапазон внутри шкалы', () => {
  const layout = rangeLayout(100, 98, 104);
  assert.ok(layout.low > 0 && layout.high < 100);
  assert.ok(layout.low < layout.current && layout.current < layout.high);

  // цена вне диапазона тоже на шкале
  const outside = rangeLayout(110, 98, 104);
  assert.ok(outside.current > outside.high && outside.current < 100);

  // вырожденный диапазон не делит на ноль
  const flat = rangeLayout(100, 100, 100);
  assert.ok(Number.isFinite(flat.current));
});

test('until', () => {
  const now = Date.parse('2026-09-23T12:00:00Z');
  assert.equal(until('2026-09-23T12:00:40Z', now), '40 с');
  assert.equal(until('2026-09-23T12:02:05Z', now), '2 мин 5 с');
  assert.equal(until('2026-09-23T11:59:00Z', now), '0 с');
});
