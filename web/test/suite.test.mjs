// Tests for the test-suite helpers (src/lib/suite.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { describeMode, formToRequest, itemDurationS, itemTone, moveItem, progress, suiteToForm } from '../src/lib/suite.ts';

test('formToRequest trims and drops sequential-only options in parallel mode', () => {
  const items = [{ key: 'a', test_id: 't1', workers: 2.4 }, { key: 'b', test_id: 't2', workers: -1 }];
  assert.deepEqual(
    formToRequest({ name: ' Smoke ', description: ' d ', mode: 'sequential', stop_on_failure: true, gap_s: 5, items }),
    { name: 'Smoke', description: 'd', mode: 'sequential', stop_on_failure: true, gap_ms: 5000,
      items: [{ test_id: 't1', workers: 2 }, { test_id: 't2', workers: 0 }] },
  );
  const par = formToRequest({ name: 'x', description: '', mode: 'parallel', stop_on_failure: true, gap_s: 5, items: [] });
  assert.equal(par.stop_on_failure, false);
  assert.equal(par.gap_ms, 0);
  assert.equal(formToRequest({ name: 'x', description: '', mode: 'sequential', stop_on_failure: false, gap_s: NaN, items: [] }).gap_ms, 0);
});

test('suiteToForm round-trips', () => {
  const s = { id: 's', name: 'n', description: '', mode: 'sequential', stop_on_failure: true, gap_ms: 3000,
    items: [{ test_id: 't1', test_name: 'a', workers: 0 }], created_at: '', updated_at: '' };
  const f = suiteToForm(s);
  assert.equal(f.gap_s, 3);
  assert.equal(f.items[0].test_id, 't1');
  assert.ok(f.items[0].key);
  assert.deepEqual(formToRequest(f).items, [{ test_id: 't1', workers: 0 }]);
});

test('moveItem swaps neighbours and ignores out-of-range moves', () => {
  assert.deepEqual(moveItem(['a', 'b', 'c'], 0, 1), ['b', 'a', 'c']);
  assert.deepEqual(moveItem(['a', 'b', 'c'], 2, -1), ['a', 'c', 'b']);
  assert.deepEqual(moveItem(['a', 'b', 'c'], 0, -1), ['a', 'b', 'c']);
  assert.deepEqual(moveItem(['a', 'b', 'c'], 2, 1), ['a', 'b', 'c']);
});

test('progress counts outcomes', () => {
  const r = { items: [{ status: 'passed' }, { status: 'failed' }, { status: 'error' }, { status: 'skipped' }, { status: 'running' }, { status: 'pending' }] };
  assert.deepEqual(progress(r), { total: 6, done: 4, passed: 1, failed: 2, skipped: 1 });
});

test('itemDurationS and tones', () => {
  assert.equal(itemDurationS({ started_at: '2026-01-01T00:00:00Z', finished_at: '2026-01-01T00:01:30Z' }), 90);
  assert.equal(itemDurationS({ started_at: '2026-01-01T00:00:00Z' }, Date.parse('2026-01-01T00:00:10Z')), 10);
  assert.equal(itemDurationS({}), undefined);
  assert.equal(itemTone('passed'), 'good');
  assert.equal(itemTone('error'), 'bad');
  assert.equal(itemTone('pending'), '');
});

test('describeMode', () => {
  assert.equal(describeMode({ mode: 'parallel', stop_on_failure: true, gap_ms: 1000 }), 'All tests at once');
  assert.equal(describeMode({ mode: 'sequential', stop_on_failure: true, gap_ms: 30000 }), 'One after another · 30 s pause · stops on first failure');
  assert.equal(describeMode({ mode: 'sequential', stop_on_failure: false, gap_ms: 0 }), 'One after another');
});
