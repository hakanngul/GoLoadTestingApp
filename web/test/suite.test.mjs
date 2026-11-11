// Tests for the test-suite helpers (src/lib/suite.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { describeMode, formToRequest, itemDurationS, itemTone, moveItem, newFormItem, progress, suiteToForm, suiteTrend } from '../src/lib/suite.ts';

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

const sum = (p95, err = 0) => ({ requests: 10, errors: 0, dropped: 0, error_rate: err, rps: 1, mean_ms: 1, p50_ms: 1, p90_ms: 1, p95_ms: p95, p99_ms: 1, max_ms: 1 });
const srun = (id, status, items, started = '2025-01-01T00:00:00Z', label = '') => ({ id, suite_id: 's', suite_name: 'n', status, started_at: started, label, items });

test('suiteTrend orders oldest first, skips active runs and computes pass rate', () => {
  const runs = [
    srun('r3', 'running', [{ test_id: 'a', test_name: 'A', status: 'running' }], '2025-01-03T00:00:00Z'),
    // a passed, b skipped after a failure elsewhere: skipped counts as not passed.
    srun('r2', 'failed', [{ test_id: 'a', test_name: 'A', status: 'passed', summary: sum(200, 0.1) }, { test_id: 'b', test_name: 'B', status: 'skipped' }],
      '2025-01-02T00:00:00Z', 'v2'),
    srun('r1', 'passed', [{ test_id: 'a', test_name: 'A', status: 'passed', summary: sum(100) }, { test_id: 'b', test_name: 'B', status: 'passed', summary: sum(50) }]),
  ];
  const t = suiteTrend(runs);
  assert.deepEqual(t.points.map((p) => p.id), ['r1', 'r2']);
  assert.deepEqual(t.points.map((p) => p.passRate), [1, 0.5]);
  assert.equal(t.points[1].label, 'v2');
  assert.deepEqual(t.points[1].cases, { '0:a': { p95_ms: 200, error_rate: 0.1 } });
  assert.deepEqual(t.cases.map((c) => c.key), ['0:a', '1:b']);
  assert.deepEqual(suiteTrend([]), { points: [], cases: [] });
});

test('suiteTrend keeps duplicate tests apart, handles missing summaries and deleted tests', () => {
  const runs = [
    srun('r2', 'passed', [{ test_id: 'a', test_name: 'A', status: 'passed', summary: sum(20) }, { test_id: 'a', test_name: 'A', status: 'passed', summary: sum(90) }], '2025-01-02T00:00:00Z'),
    srun('r1', 'error', [{ test_id: 'a', test_name: '', status: 'error' }, { test_id: 'a', test_name: '', status: 'error' }, { test_id: 'x', test_name: '', status: 'error' }]),
    srun('r0', 'stopped', []),
  ];
  const t = suiteTrend(runs);
  assert.deepEqual(t.points.map((p) => p.passRate), [null, 0, 1]);
  assert.deepEqual(t.points[2].cases, { '0:a': { p95_ms: 20, error_rate: 0 }, '1:a': { p95_ms: 90, error_rate: 0 } });
  assert.deepEqual(t.points[1].cases, {});
  assert.deepEqual(t.cases.map((c) => [c.key, c.name]), [['0:a', 'A'], ['1:a', 'A'], ['2:x', '']]);
});
