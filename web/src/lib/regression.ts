import type { ThresholdResult } from '../api/types';
import { fmtMs, fmtPct, fmtRps } from './format.ts';

const SUFFIX = ' vs baseline';

/** True for a regression-vs-baseline result (metric "<name> vs baseline"). */
export function isRegression(t: ThresholdResult): boolean {
  return t.metric.endsWith(SUFFIX);
}

/** The underlying metric name of a threshold result ("p95_ms vs baseline" -> "p95_ms"). */
export function regressionMetric(t: ThresholdResult): string {
  return isRegression(t) ? t.metric.slice(0, -SUFFIX.length) : t.metric;
}

/** Formats a value of a regression metric (p95_ms, error_rate, rps, ...). */
export function fmtRegressionValue(metric: string, v: number): string {
  if (metric === 'error_rate') return fmtPct(v);
  if (metric === 'rps') return `${fmtRps(v)} req/s`;
  return fmtMs(v);
}

/** The rule cell of a baseline row, e.g. "≤ 54.0 ms (baseline 45.0 ms, +20%)". */
export function regressionRule(t: ThresholdResult): string {
  const m = regressionMetric(t);
  const sym = t.op === 'gte' ? '≥' : '≤';
  let s = `${sym} ${fmtRegressionValue(m, t.value)}`;
  if (t.baseline !== undefined && t.baseline !== null) {
    let delta = '';
    if (t.baseline !== 0) {
      const pct = (t.value / t.baseline - 1) * 100;
      delta = `, ${pct >= 0 ? '+' : ''}${Math.abs(pct) < 10 ? Number(pct.toFixed(1)) : pct.toFixed(0)}%`;
    }
    s += ` (baseline ${fmtRegressionValue(m, t.baseline)}${delta})`;
  }
  return s;
}
