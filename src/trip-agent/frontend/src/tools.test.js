import test from 'node:test';
import assert from 'node:assert/strict';
import { argsLine, formatNumber, reduceEvents, temp, toolTitle } from './tools.js';

test('argsLine: главное из аргументов', () => {
  assert.equal(argsLine('find_city', { name: 'Стамбул' }), '«Стамбул»');
  assert.equal(
    argsLine('trip_weather', { latitude: 41.01384, longitude: 28.94966, start_date: '2026-11-10', end_date: '2026-11-12' }),
    '41.0138, 28.9497 · 2026-11-10 — 2026-11-12'
  );
  assert.equal(argsLine('convert', { amount: 80000, from: 'RUB', to: 'TRY' }), '80 000 RUB → TRY');
  assert.equal(argsLine('unknown', { x: 1 }), '');
  assert.equal(argsLine('find_city', null), '');
});

test('форматирование', () => {
  assert.equal(formatNumber(1234567.4), '1 234 567');
  assert.equal(temp(12.6), '+13°');
  assert.equal(temp(-0.4), '0°');
  assert.equal(toolTitle('trip_publish'), 'Публикую план');
  assert.equal(toolTitle('mystery'), 'mystery');
});

test('reduceEvents: ход модели, вызовы, текущее действие, счётчики', () => {
  const events = [
    { seq: 1, type: 'started' },
    { seq: 2, type: 'thinking', turn: 1 },
    { seq: 3, type: 'decided', turn: 1, calls: [{ callId: 'a', server: 'places', tool: 'find_city', args: { name: 'X' } }] },
    { seq: 4, type: 'tool_started', callId: 'a', server: 'places', tool: 'find_city', args: { name: 'X' } },
  ];
  let state = reduceEvents(events);
  assert.equal(state.current.kind, 'tool');
  assert.equal(state.active, 'places');
  assert.equal(state.turns.length, 1);

  state = reduceEvents([
    ...events,
    { seq: 5, type: 'tool_finished', callId: 'a', server: 'places', tool: 'find_city', ok: true, summary: 'X, Y' },
    { seq: 6, type: 'injection', callId: 'a', server: 'places', tool: 'find_city', quote: 'ignore previous instructions' },
    { seq: 7, type: 'verify', checks: [{ name: 'c', ok: true }] },
    { seq: 8, type: 'finished', status: 'done' },
  ]);
  assert.equal(state.active, null);
  assert.equal(state.counts.places, 1);
  assert.equal(state.turns[0].calls[0].status, 'ok');
  assert.equal(state.turns[0].calls[0].injection, 'ignore previous instructions');
  assert.equal(state.notices[0].kind, 'injection');
  assert.equal(state.finished.status, 'done');
  assert.equal(state.checks.length, 1);
});
