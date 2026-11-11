import type { HistBucket } from '../api/types';

/** A histogram bucket prepared for charting. */
export interface HistRow extends HistBucket {
  /** Bucket index (category key of the x axis). */
  idx: number;
  /** Share of all requests in the histogram, 0..100. */
  pct: number;
}

/** Compact millisecond label for an axis edge: 0.25, 3.4, 120, 2.5s. */
export function fmtEdgeMs(v: number): string {
  if (!Number.isFinite(v)) return '—';
  if (v >= 10_000) return `${+(v / 1000).toFixed(1)}s`;
  if (v >= 1000) return `${+(v / 1000).toFixed(2)}s`;
  if (v >= 100) return `${Math.round(v)}`;
  if (v >= 10) return `${+v.toFixed(1)}`;
  return `${+v.toFixed(2)}`;
}

/** "12–18 ms" style range for a bucket (units shown once). */
export function bucketRange(b: Pick<HistBucket, 'from_ms' | 'to_ms'>): string {
  const unit = b.to_ms >= 1000 ? '' : ' ms';
  return `${fmtEdgeMs(b.from_ms)}–${fmtEdgeMs(b.to_ms)}${unit}`;
}

/** Share of `count` in `total` as a percentage string ("12.3%", "<0.1%"). */
export function fmtShare(count: number, total: number): string {
  if (!(total > 0) || !(count > 0)) return '0%';
  const p = (count / total) * 100;
  if (p < 0.1) return '<0.1%';
  return `${+p.toFixed(p >= 10 ? 1 : 2)}%`;
}

/** Adds the bucket index and percentage share to each bucket. */
export function histRows(buckets: HistBucket[] | null | undefined): HistRow[] {
  const list = buckets ?? [];
  const total = list.reduce((a, b) => a + b.count, 0);
  return list.map((b, idx) => ({ ...b, idx, pct: total > 0 ? (b.count / total) * 100 : 0 }));
}

/**
 * Index of the bucket containing `ms` (values below the first / above the last
 * bucket clamp to the ends), or -1 when there are no buckets or `ms` is not a
 * usable number.
 */
export function bucketIndexFor(buckets: HistBucket[] | null | undefined, ms: number): number {
  const list = buckets ?? [];
  if (list.length === 0 || !Number.isFinite(ms) || ms <= 0) return -1;
  if (ms < list[0].from_ms) return 0;
  for (let i = 0; i < list.length; i++) {
    if (ms < list[i].to_ms) return i;
  }
  return list.length - 1;
}
