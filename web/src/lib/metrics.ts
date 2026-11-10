import type { MetricPoint } from '../api/types';

/**
 * Display-only clean-up for a finished run: the last metric point can cover a
 * partial second, which makes throughput dip at the very end. Drop it when its
 * `requests` is below 50% of the median of the previous 5 points.
 */
export function trimPartialTail(points: MetricPoint[]): MetricPoint[] {
  if (points.length < 2) return points;
  const last = points[points.length - 1];
  const prev = points
    .slice(-6, -1)
    .map((p) => p.requests)
    .sort((a, b) => a - b);
  const mid = prev.length >> 1;
  const median = prev.length % 2 ? prev[mid] : (prev[mid - 1] + prev[mid]) / 2;
  return median > 0 && last.requests < median * 0.5 ? points.slice(0, -1) : points;
}
