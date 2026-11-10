// Tests for the test-suite helpers (src/lib/suite.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { describeMode, formToRequest, itemDurationS, itemTone, moveItem, newFormItem, progress, suiteToForm } from '../src/lib/suite.ts';

const keep = { mode: 'keep', unit: 'users', target: '10', rampUp: '30s', hold: '5m', rampDown: '0s' };

test('formToRequest trims and drops sequential-only options in parallel mode', () => {
  const items = [{ key: 'a', test_id: 't1', workers: 2.4, load: keep }, { key: 'b', test_id: 't2', workers: -1, load: keep }];
  assert.deepEqual(
    formToRequest({ name: ' Smoke ', description: ' d ', mode: 'sequential', stop_on_failure: true, gap_s: 5, items }).req,
    { name: 'Smoke', description: 'd', mode: 'sequential', stop_on_failure: true, gap_ms: 5000,
      items: [{ test_id: 't1', workers: 2 }, { test_id: 't2', workers: 0 }] },
  );
  const par = formToRequest({ name: 'x', description: '', mode: 'parallel', stop_on_failure: true, gap_s: 5, items: [] }).req;
  assert.equal(par.stop_on_failure, false);
  assert.equal(par.gap_ms, 0);
  assert.equal(formToRequest({ name: 'x', description: '', mode: 'sequential', stop_on_failure: false, gap_s: NaN, items: [] }).req.gap_ms, 0);
});

test('suiteToForm round-trips', () => {
  const s = { id: 's', name: 'n', description: '', mode: 'sequential', stop_on_failure: true, gap_ms: 3000,
    items: [{ test_id: 't1', test_name: 'a', workers: 0 }], created_at: '', updated_at: '' };
  const f = suiteToForm(s);
  assert.equal(f.gap_s, 3);
  assert.equal(f.items[0].test_id, 't1');
  assert.ok(f.items[0].key);
  assert.deepEqual(formToRequest(f).req.items, [{ test_id: 't1', workers: 0 }]);
});

test('item load overrides round-trip and report errors by item', () => {
  const info = () => ({ model: 'closed', peak: 5 });
  const s = { id: 's', name: 'n', description: '', mode: 'sequential', stop_on_failure: false, gap_ms: 0, created_at: '', updated_at: '',
    items: [
      { test_id: 't1', test_name: 'a', workers: 0 },
      { test_id: 't1', test_name: 'a', workers: 0, load: { mode: 'profile', users: 50, ramp_up_ms: 30000, hold_ms: 300000 } },
    ] };
  const f = suiteToForm(s, info);
  assert.equal(f.items[0].load.mode, 'keep');
  assert.equal(f.items[0].load.target, '5');
  assert.equal(f.items[1].load.target, '50');
  const { req, errors } = formToRequest(f, info);
  assert.deepEqual(errors, {});
  assert.equal(req.items[0].load, undefined);
  assert.deepEqual(req.items[1].load, { mode: 'profile', users: 50, ramp_up_ms: 30000, hold_ms: 300000, ramp_down_ms: 0 });

  f.items[1].load = { ...f.items[1].load, target: '0' };
  f.items.push({ ...newFormItem('t1', info()), load: { ...keep, mode: 'scale', unit: 'rate', target: '20' } });
  const bad = formToRequest(f, info).errors;
  assert.ok(bad['items[1].load.users']);
  assert.ok(bad['items[2].load.rate']);
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
