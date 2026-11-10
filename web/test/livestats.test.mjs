// Tests for the live Statistics / Failures table helpers. Run with `pnpm test`.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  AGGREGATED,
  buildStatRows,
  errorStatusLabel,
  errorStatusTone,
  errorSteps,
  filterErrorGroups,
  fmtAgo,
  fmtRunOffset,
  groupKey,
  increasedFails,
  isOverflowGroup,
  newGroupKeys,
  nextSort,
  sortErrorGroups,
  sortStatRows,
  totalFailures,
} from '../src/lib/livestats.ts';

const sum = (o = {}) => ({
  requests: 100,
  errors: 0,
  dropped: 0,
  error_rate: 0,
  rps: 10,
  mean_ms: 50,
  p50_ms: 40,
  p90_ms: 80,
  p95_ms: 100,
  p99_ms: 200,
  max_ms: 500,
  ...o,
});

const stats = () => ({
  elapsed: 30,
  total: sum({
    requests: 300,
    errors: 6,
    error_rate: 0.02,
    by_step: {
      list: sum({ requests: 200, errors: 2, error_rate: 0.01, p50_ms: 30 }),
      detail: sum({ requests: 100, errors: 4, error_rate: 0.04, p50_ms: 60 }),
    },
  }),
  current: sum({
    requests: 12,
    rps: 12,
    by_step: {
      list: sum({ requests: 8, rps: 8 }),
      detail: sum({ requests: 3, rps: 3 }),
      search: sum({ requests: 1, rps: 1 }),
    },
  }),
  errors: [],
});

test('buildStatRows: per-step rows in step order, steps only in current, aggregated last', () => {
  const rows = buildStatRows(stats(), 'total');
  assert.deepEqual(
    rows.map((r) => r.name),
    ['list', 'detail', 'search', AGGREGATED],
  );
  const [list, , search, agg] = rows;
  assert.equal(list.requests, 200);
  assert.equal(list.fails, 2);
  assert.equal(list.failRate, 0.01);
  assert.equal(list.currentRps, 8, 'current RPS comes from current.by_step');
  assert.equal(list.p50, 30);
  assert.equal(search.requests, 0, 'step not yet in total shows zeros');
  assert.equal(search.currentRps, 1);
  assert.equal(agg.aggregate, true);
  assert.equal(agg.requests, 300);
  assert.equal(agg.currentRps, 12);
});

test('buildStatRows: last-second mode uses current', () => {
  const rows = buildStatRows(stats(), 'current');
  assert.equal(rows[0].requests, 8);
  assert.equal(rows.at(-1).requests, 12);
  assert.deepEqual(buildStatRows(null), []);
});

test('buildStatRows: tolerates missing by_step and derives the fail rate', () => {
  const s = { elapsed: 1, total: { ...sum({ requests: 10, errors: 5 }), error_rate: undefined }, current: sum(), errors: null };
  const rows = buildStatRows(s);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].failRate, 0.5);
});

test('sortStatRows keeps Aggregated last and sorts by key', () => {
  const rows = buildStatRows(stats(), 'total');
  assert.deepEqual(
    sortStatRows(rows, { key: 'fails', dir: 'desc' }).map((r) => r.name),
    ['detail', 'list', 'search', AGGREGATED],
  );
  assert.deepEqual(
    sortStatRows(rows, { key: 'name', dir: 'asc' }).map((r) => r.name),
    ['detail', 'list', 'search', AGGREGATED],
  );
  assert.deepEqual(
    sortStatRows(rows, null).map((r) => r.name),
    ['list', 'detail', 'search', AGGREGATED],
  );
});

test('nextSort cycles desc -> asc -> off for numbers, asc -> desc -> off for names', () => {
  let s = nextSort(null, 'p95');
  assert.deepEqual(s, { key: 'p95', dir: 'desc' });
  s = nextSort(s, 'p95');
  assert.deepEqual(s, { key: 'p95', dir: 'asc' });
  assert.equal(nextSort(s, 'p95'), null);
  assert.deepEqual(nextSort(s, 'name'), { key: 'name', dir: 'asc' });
  assert.deepEqual(nextSort({ key: 'name', dir: 'asc' }, 'name'), { key: 'name', dir: 'desc' });
});

test('increasedFails flags rows whose fail count went up', () => {
  const rows = buildStatRows(stats(), 'total');
  const prev = new Map([
    ['list', 2],
    ['detail', 1],
    [AGGREGATED, 3],
  ]);
  assert.deepEqual([...increasedFails(prev, rows)].sort(), [AGGREGATED, 'detail']);
  assert.equal(increasedFails(new Map(), rows).size, 0, 'nothing flashes on the first event');
});

const g = (step, status, message, count, first = '2026-01-01T00:00:00Z', last = '2026-01-01T00:00:10Z') => ({
  step,
  status,
  message,
  count,
  first,
  last,
});

test('error status label and tone', () => {
  assert.equal(errorStatusLabel(0), 'network/timeout');
  assert.equal(errorStatusLabel(503), '503');
  assert.equal(errorStatusTone(0), 'bad');
  assert.equal(errorStatusTone(500), 'bad');
  assert.equal(errorStatusTone(429), 'warn');
});

test('sortErrorGroups: count desc, overflow group last', () => {
  const groups = [
    g('a', 500, 'x', 5),
    g('', 0, '(other errors)', 50),
    g('b', 503, 'y', 9),
    g('a', 0, 'timeout', 9),
  ];
  const sorted = sortErrorGroups(groups);
  assert.deepEqual(
    sorted.map((x) => x.count),
    [9, 9, 5, 50],
  );
  assert.equal(sorted[0].step, 'a', 'ties by step');
  assert.ok(isOverflowGroup(sorted.at(-1)));
  assert.deepEqual(sortErrorGroups(null), []);
  assert.equal(groups[0].count, 5, 'input not mutated');
});

test('filter, steps, totals and new-group detection', () => {
  const groups = [g('a', 500, 'x', 5), g('b', 503, 'y', 9), g('', 0, '(other errors)', 2)];
  assert.deepEqual(errorSteps(groups), ['a', 'b']);
  assert.deepEqual(
    filterErrorGroups(groups, 'a').map((x) => x.count),
    [5, 2],
    'overflow group is kept by a step filter',
  );
  assert.equal(filterErrorGroups(groups, '').length, 3);
  assert.equal(totalFailures(groups), 16);
  assert.equal(totalFailures(null), 0);
  const prev = new Set([groupKey(groups[0])]);
  assert.deepEqual([...newGroupKeys(prev, groups)], [groupKey(groups[1]), groupKey(groups[2])]);
  assert.notEqual(groupKey(g('a', 500, 'x', 1)), groupKey(g('a', 50, '0x', 1)));
});

test('relative times', () => {
  const now = Date.parse('2026-01-01T00:10:00Z');
  assert.equal(fmtAgo('2026-01-01T00:09:56Z', now), '4s ago');
  assert.equal(fmtAgo('2026-01-01T00:07:55Z', now), '2m 05s ago');
  assert.equal(fmtAgo('2026-01-01T00:10:00.400Z', now), 'now');
  assert.equal(fmtAgo('', now), '—');
  assert.equal(fmtRunOffset('2026-01-01T00:01:35Z', '2026-01-01T00:00:00Z'), '+1:35');
  assert.equal(fmtRunOffset('2026-01-01T01:02:03Z', '2026-01-01T00:00:00Z'), '+1:02:03');
  assert.equal(fmtRunOffset('bad', '2026-01-01T00:00:00Z'), '—');
});
