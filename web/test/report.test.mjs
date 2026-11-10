// Tests for the pure reporting helpers (windows, deltas, verdicts, compare
// URL parsing, CSV). Run with `pnpm test`.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  compareCsv,
  compareSummaries,
  customWindow,
  deltaFor,
  fmtDelta,
  fmtOffset,
  moveItem,
  overallVerdict,
  parseRunIds,
  parseTolerance,
  parseWindow,
  regressionText,
  windowLabel,
  windowParams,
  worstDelta,
  csvCell,
} from '../src/lib/report.ts';

const sum = (o = {}) => ({
  requests: 1000,
  errors: 2,
  dropped: 0,
  error_rate: 0.002,
  rps: 100,
  mean_ms: 50,
  p50_ms: 40,
  p90_ms: 80,
  p95_ms: 100,
  p99_ms: 200,
  max_ms: 500,
  ...o,
});

test('window params and parsing round-trip', () => {
  assert.equal(windowParams({ kind: 'steady' }).toString(), 'window=steady');
  assert.equal(windowParams({ kind: 'custom', from: 20.4, to: 70 }).toString(), 'window=custom&from=20&to=70');
  assert.deepEqual(parseWindow(new URLSearchParams('window=full')), { kind: 'full' });
  assert.deepEqual(parseWindow(new URLSearchParams('window=custom&from=20&to=70')), { kind: 'custom', from: 20, to: 70 });
  // invalid custom ranges fall back to steady
  assert.deepEqual(parseWindow(new URLSearchParams('window=custom&from=70&to=20')), { kind: 'steady' });
  assert.deepEqual(parseWindow(new URLSearchParams('')), { kind: 'steady' });
});

test('customWindow normalises drag order and rejects tiny ranges', () => {
  assert.deepEqual(customWindow(70.2, 19.6), { kind: 'custom', from: 19, to: 71 });
  assert.equal(customWindow(10, 11), null);
  assert.equal(customWindow(NaN, 5), null);
});

test('window labels', () => {
  assert.equal(fmtOffset(20), '0:20');
  assert.equal(fmtOffset(70), '1:10');
  assert.equal(fmtOffset(3723), '1:02:03');
  assert.equal(windowLabel({ kind: 'custom', from: 20, to: 70 }), 'Window 0:20–1:10');
  assert.equal(windowLabel({ kind: 'steady' }), 'Steady state');
  assert.equal(windowLabel({ kind: 'steady', from: 30, to: 150 }), 'Steady state · 0:30–2:30');
  assert.equal(windowLabel({ kind: 'full', from: 0, to: 180 }), 'Full run · 0:00–3:00');
});

test('fmtDelta: relative percent and absolute percentage points', () => {
  assert.equal(fmtDelta({ metric: 'p95_ms', delta: 0.234 }), '+23%');
  assert.equal(fmtDelta({ metric: 'p95_ms', delta: -0.042 }), '−4.2%');
  assert.equal(fmtDelta({ metric: 'rps', delta: 0 }), '±0%');
  assert.equal(fmtDelta({ metric: 'error_rate', delta: 0.0012 }), '+0.12 pp');
  assert.equal(fmtDelta({ metric: 'error_rate', delta: -0.0005 }), '−0.05 pp');
  assert.equal(fmtDelta({ metric: 'error_rate', delta: -0.00001 }), '±0.00 pp');
  assert.equal(fmtDelta({ metric: 'p95_ms', delta: NaN }), '—');
});

test('deltaFor verdicts follow direction and tolerance', () => {
  assert.equal(deltaFor('p95_ms', 100, 125).verdict, 'worse');
  assert.equal(deltaFor('p95_ms', 100, 105).verdict, 'same');
  assert.equal(deltaFor('p95_ms', 100, 80).verdict, 'better');
  assert.equal(deltaFor('rps', 100, 80).verdict, 'worse'); // higher is better
  assert.equal(deltaFor('rps', 100, 120).verdict, 'better');
  assert.equal(deltaFor('p95_ms', 0, 10).verdict, 'n/a');
  // error_rate: absolute, tolerance/10 = 1 pp
  const e = deltaFor('error_rate', 0.001, 0.02);
  assert.equal(e.verdict, 'worse');
  assert.ok(Math.abs(e.delta - 0.019) < 1e-12);
  assert.equal(deltaFor('error_rate', 0.001, 0.005).verdict, 'same');
  assert.equal(deltaFor('error_rate', 0, 0).verdict, 'same');
  // custom tolerance
  assert.equal(deltaFor('p95_ms', 100, 125, 0.3).verdict, 'same');
});

test('worst regression and summary text', () => {
  const c = compareSummaries(sum(), sum({ p95_ms: 125, p99_ms: 215, rps: 70 }));
  assert.equal(c.regression, true);
  // rps -30% (3x tol) beats p95 +25% (2.5x tol)
  assert.equal(worstDelta(c).metric, 'rps');
  assert.equal(regressionText(c), 'RPS −30%');
  assert.equal(overallVerdict(c), 'regressed');
  // max is never the headline (and never a regression on its own)
  assert.equal(worstDelta(compareSummaries(sum(), sum({ p95_ms: 130, max_ms: 2000 }))).metric, 'p95_ms');
  assert.equal(worstDelta(compareSummaries(sum(), sum({ max_ms: 2000 }))), undefined);
  const ok = compareSummaries(sum(), sum({ p95_ms: 80 }));
  assert.equal(regressionText(ok), '');
  assert.equal(overallVerdict(ok), 'improved');
  assert.equal(overallVerdict(compareSummaries(sum(), sum())), 'unchanged');
  assert.equal(overallVerdict(undefined), 'none');
});

test('parseRunIds supports runs= and legacy a=&b=', () => {
  assert.deepEqual(parseRunIds(new URLSearchParams('runs=a,b,,c,a')), ['a', 'b', 'c']);
  assert.deepEqual(parseRunIds(new URLSearchParams('a=x&b=y')), ['x', 'y']);
  assert.deepEqual(parseRunIds(new URLSearchParams('b=y')), ['y']);
  assert.deepEqual(parseRunIds(new URLSearchParams('runs=1,2,3,4,5,6,7')), ['1', '2', '3', '4', '5', '6']);
});

test('moveItem and parseTolerance', () => {
  assert.deepEqual(moveItem(['a', 'b', 'c'], 2, 0), ['c', 'a', 'b']);
  assert.deepEqual(moveItem(['a', 'b'], 5, 0), ['a', 'b']);
  assert.equal(parseTolerance('10'), 0.1);
  assert.equal(parseTolerance('7.5%'), 0.075);
  assert.equal(parseTolerance('-1'), null);
  assert.equal(parseTolerance('x'), null);
});

test('compareCsv escapes and lays out reference + candidates', () => {
  assert.equal(csvCell('a,"b"'), '"a,""b"""');
  const r = (id, label, s) => ({
    run: { id, label, started_at: '2026-11-01T10:00:00Z' },
    window: { kind: 'steady', from: 30, to: 150 },
    summary: s,
    thresholds: [],
  });
  const ref = sum({ by_step: { list: sum() } });
  const cand = sum({ p95_ms: 125, by_step: { list: sum({ p95_ms: 130 }) } });
  const cmp = compareSummaries(ref, cand);
  cmp.by_step = { list: compareSummaries(ref.by_step.list, cand.by_step.list).metrics };
  const csv = compareCsv({ runs: [r('r1', 'v1.0', ref), r('r2', 'v1,1', cand)], comparisons: [cmp] });
  const lines = csv.trim().split('\n');
  assert.equal(lines[0], 'step,metric,v1.0 (reference),"v1,1","v1,1 delta","v1,1 verdict"');
  assert.ok(lines.includes(',p95_ms,100,125,0.25,worse'));
  assert.ok(lines.includes('list,p95_ms,100,130,0.3,worse'));
  assert.ok(lines.some((l) => l.startsWith(',window,Steady state · 0:30–2:30')));
});

test('max_ms alone never makes a regression (matches the server rule)', async () => {
  const { overallVerdict, regressionText } = await import('../src/lib/report.ts');
  const base = { p50_ms: 20, p90_ms: 40, p95_ms: 50, p99_ms: 55, max_ms: 100, mean_ms: 25, rps: 200, error_rate: 0.01 };
  const onlyMax = compareSummaries(base, { ...base, max_ms: 112.5 }, 0.1);
  assert.equal(onlyMax.regression, false);
  assert.equal(overallVerdict(onlyMax), 'unchanged');
  assert.equal(regressionText(onlyMax), '');
  assert.equal(onlyMax.metrics.find((m) => m.metric === 'max_ms').verdict, 'worse'); // still shown per metric

  const slower = compareSummaries(base, { ...base, max_ms: 112.5, p95_ms: 92 }, 0.1);
  assert.equal(slower.regression, true);
  assert.equal(overallVerdict(slower), 'regressed');
  assert.match(regressionText(slower), /^p95 \+/);
});
