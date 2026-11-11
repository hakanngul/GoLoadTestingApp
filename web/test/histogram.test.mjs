// Tests for the latency histogram helpers. Run with `pnpm test`.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { bucketIndexFor, bucketRange, fmtEdgeMs, fmtShare, histRows } from '../src/lib/histogram.ts';

const buckets = [
  { from_ms: 1, to_ms: 2, count: 10 },
  { from_ms: 2, to_ms: 4, count: 0 },
  { from_ms: 4, to_ms: 8, count: 30 },
  { from_ms: 8, to_ms: 1600, count: 60 },
];

test('fmtEdgeMs', () => {
  assert.equal(fmtEdgeMs(0.254), '0.25');
  assert.equal(fmtEdgeMs(12.34), '12.3');
  assert.equal(fmtEdgeMs(123.6), '124');
  assert.equal(fmtEdgeMs(2500), '2.5s');
  assert.equal(fmtEdgeMs(NaN), '—');
});

test('bucketRange', () => {
  assert.equal(bucketRange({ from_ms: 4, to_ms: 8 }), '4–8 ms');
  assert.equal(bucketRange({ from_ms: 800, to_ms: 1600 }), '800–1.6s');
});

test('fmtShare', () => {
  assert.equal(fmtShare(0, 100), '0%');
  assert.equal(fmtShare(5, 0), '0%');
  assert.equal(fmtShare(1, 10000), '<0.1%');
  assert.equal(fmtShare(1, 3), '33.3%');
  assert.equal(fmtShare(1, 20), '5%');
});

test('histRows adds index and percentage', () => {
  const rows = histRows(buckets);
  assert.deepEqual(
    rows.map((r) => [r.idx, r.pct]),
    [
      [0, 10],
      [1, 0],
      [2, 30],
      [3, 60],
    ],
  );
  assert.deepEqual(histRows(undefined), []);
  assert.equal(histRows([{ from_ms: 1, to_ms: 2, count: 0 }])[0].pct, 0);
});

test('bucketIndexFor', () => {
  assert.equal(bucketIndexFor(buckets, 1.5), 0);
  assert.equal(bucketIndexFor(buckets, 2), 1);
  assert.equal(bucketIndexFor(buckets, 5), 2);
  assert.equal(bucketIndexFor(buckets, 0.5), 0);
  assert.equal(bucketIndexFor(buckets, 99999), 3);
  assert.equal(bucketIndexFor(buckets, NaN), -1);
  assert.equal(bucketIndexFor([], 5), -1);
  assert.equal(bucketIndexFor(buckets, 0), -1);
});
