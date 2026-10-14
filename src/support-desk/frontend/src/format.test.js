import { test } from 'node:test';
import assert from 'node:assert/strict';
import { blockedNote, indexLine, statusLabel } from './format.js';

test('statusLabel', () => {
  assert.equal(statusLabel('open'), 'открыт');
  assert.equal(statusLabel('in_progress'), 'в работе');
  assert.equal(statusLabel('resolved'), 'решён');
  assert.equal(statusLabel('other'), 'other');
});

test('blockedNote', () => {
  assert.equal(blockedNote('injection'), 'Запрос отклонён защитой');
  assert.equal(blockedNote('offtopic'), 'Вне темы поддержки');
  assert.equal(blockedNote(''), '');
});

test('indexLine', () => {
  assert.equal(indexLine({ state: 'indexing', done: 64, total: 2400 }), 'Индексация: 64 из 2400 фрагментов');
  assert.equal(indexLine({ state: 'ready', files: 812, chunks: 2400 }), 'Индекс: 812 файлов, 2400 фрагментов');
  assert.equal(indexLine({ state: 'error', error: 'нет файлов' }), 'Ошибка индекса: нет файлов');
  assert.equal(indexLine(null), '');
});
