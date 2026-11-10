// Pure helpers for the reporting UI: report windows, metric deltas and
// verdicts, compare URL parsing and CSV export. No React, no DOM — unit
// tested in test/report.test.mjs.
import type {
  CompareResult,
  Comparison,
  MetricDelta,
  Summary,
  Verdict,
  Window,
  WindowKind,
} from '../api/types';
import { fmtMs, fmtPct, fmtRps } from './format.ts';

// =====================================================================
// Windows
// =====================================================================

/** A window as requested by the UI (custom ranges in whole seconds). */
export type WindowSel = { kind: 'steady' | 'full' } | { kind: 'custom'; from: number; to: number };

export const DEFAULT_WINDOW: WindowSel = { kind: 'steady' };

/** Query parameters for `?window=` (+ `from`/`to` for custom). */
export function windowParams(w: WindowSel): URLSearchParams {
  const q = new URLSearchParams();
  q.set('window', w.kind);
  if (w.kind === 'custom') {
    q.set('from', String(Math.max(0, Math.round(w.from))));
    q.set('to', String(Math.max(0, Math.round(w.to))));
  }
  return q;
}

/** Reads a window from URL params; anything invalid falls back to steady. */
export function parseWindow(params: URLSearchParams): WindowSel {
  const k = params.get('window');
  if (k === 'full') return { kind: 'full' };
  if (k === 'custom') {
    const from = Number(params.get('from'));
    const to = Number(params.get('to'));
    if (Number.isFinite(from) && Number.isFinite(to) && to > from && from >= 0) return { kind: 'custom', from, to };
  }
  return { kind: 'steady' };
}

/** Normalises a dragged range (any order) to a custom window, or null if too short. */
export function customWindow(a: number, b: number, minSpan = 2): WindowSel | null {
  if (!Number.isFinite(a) || !Number.isFinite(b)) return null;
  const from = Math.max(0, Math.floor(Math.min(a, b)));
  const to = Math.ceil(Math.max(a, b));
  return to - from >= minSpan ? { kind: 'custom', from, to } : null;
}

/** Seconds -> "0:20", "12:05", "1:02:03" (compact clock for ranges). */
export function fmtOffset(s: number): string {
  if (!Number.isFinite(s)) return '—';
  const total = Math.max(0, Math.round(s));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const sec = String(total % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

export const WINDOW_KIND_LABEL: Record<WindowKind, string> = {
  steady: 'Steady state',
  full: 'Full run',
  custom: 'Custom',
};

function hasRange(w: WindowSel | Window): w is Window | Extract<WindowSel, { kind: 'custom' }> {
  return 'from' in w && 'to' in w && Number.isFinite(w.from) && Number.isFinite(w.to) && w.to > w.from;
}

/** "0:20–1:10" for a window with a range, '' otherwise. */
export function windowRange(w: WindowSel | Window): string {
  return hasRange(w) ? `${fmtOffset(w.from)}–${fmtOffset(w.to)}` : '';
}

/**
 * Human label of a window: "Steady state · 0:30–2:30", "Full run · 0:00–3:00",
 * "Window 0:20–1:10". Range parts appear when known (resolved Window).
 */
export function windowLabel(w: WindowSel | Window): string {
  const r = windowRange(w);
  if (w.kind === 'custom') return r ? `Window ${r}` : 'Custom window';
  const base = WINDOW_KIND_LABEL[w.kind] ?? w.kind;
  return r ? `${base} · ${r}` : base;
}

export function sameWindow(a: WindowSel, b: WindowSel): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === 'custom' && b.kind === 'custom') return a.from === b.from && a.to === b.to;
  return true;
}

// =====================================================================
// Metrics, deltas and verdicts
// =====================================================================

export interface MetricInfo {
  key: string;
  label: string;
  lowerIsBetter: boolean;
  /** error_rate: delta is absolute (percentage points as a fraction). */
  absolute: boolean;
  fmt: (v: number) => string;
}

const INFO: Record<string, MetricInfo> = {
  p50_ms: { key: 'p50_ms', label: 'p50', lowerIsBetter: true, absolute: false, fmt: fmtMs },
  p90_ms: { key: 'p90_ms', label: 'p90', lowerIsBetter: true, absolute: false, fmt: fmtMs },
  p95_ms: { key: 'p95_ms', label: 'p95', lowerIsBetter: true, absolute: false, fmt: fmtMs },
  p99_ms: { key: 'p99_ms', label: 'p99', lowerIsBetter: true, absolute: false, fmt: fmtMs },
  max_ms: { key: 'max_ms', label: 'Max', lowerIsBetter: true, absolute: false, fmt: fmtMs },
  mean_ms: { key: 'mean_ms', label: 'Mean', lowerIsBetter: true, absolute: false, fmt: fmtMs },
  rps: { key: 'rps', label: 'RPS', lowerIsBetter: false, absolute: false, fmt: fmtRps },
  error_rate: { key: 'error_rate', label: 'Error rate', lowerIsBetter: true, absolute: true, fmt: (v) => fmtPct(v) },
};

/** Display order of the compared metrics. */
export const METRIC_ORDER = ['p50_ms', 'p90_ms', 'p95_ms', 'p99_ms', 'max_ms', 'mean_ms', 'rps', 'error_rate'] as const;

export function metricInfo(metric: string): MetricInfo {
  return INFO[metric] ?? { key: metric, label: metric, lowerIsBetter: true, absolute: false, fmt: (v) => String(v) };
}

export function fmtMetric(metric: string, v: number | undefined | null): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return '—';
  return metricInfo(metric).fmt(v);
}

const MINUS = '−';

/** "+23%", "−4.2%", "+0.12 pp" (error_rate), "—" when not finite. */
export function fmtDelta(d: Pick<MetricDelta, 'metric' | 'delta'>): string {
  const v = d.delta;
  if (typeof v !== 'number' || !Number.isFinite(v)) return '—';
  const sign = v > 0 ? '+' : v < 0 ? MINUS : '±';
  if (metricInfo(d.metric).absolute) {
    const txt = Math.abs(v * 100).toFixed(2);
    return `${txt === '0.00' ? '±' : sign}${txt} pp`;
  }
  const p = Math.abs(v * 100);
  const txt = p >= 10 ? p.toFixed(0) : p >= 0.05 ? p.toFixed(1) : '0';
  return `${txt === '0' ? '±' : sign}${txt}%`;
}

/**
 * Verdict of one metric (mirrors the backend rule): relative delta for
 * latencies/RPS against `tolerance`; error_rate uses an absolute delta against
 * tolerance/10. A zero reference makes a relative metric not comparable.
 */
export function deltaFor(metric: string, reference: number, candidate: number, tolerance = 0.1): MetricDelta {
  const info = metricInfo(metric);
  if (!Number.isFinite(reference) || !Number.isFinite(candidate)) {
    return { metric, reference, candidate, delta: NaN, verdict: 'n/a' };
  }
  let delta: number;
  let tol: number;
  if (info.absolute) {
    delta = candidate - reference;
    tol = tolerance / 10;
  } else {
    if (reference === 0) return { metric, reference, candidate, delta: NaN, verdict: 'n/a' };
    delta = (candidate - reference) / reference;
    tol = tolerance;
  }
  const worse = info.lowerIsBetter ? delta > tol : delta < -tol;
  const better = info.lowerIsBetter ? delta < -tol : delta > tol;
  const verdict: Verdict = worse ? 'worse' : better ? 'better' : 'same';
  return { metric, reference, candidate, delta, verdict };
}

/** Client-side comparison of two summaries (used where the API has no Comparison). */
export function compareSummaries(
  ref: Summary,
  cand: Summary,
  tolerance = 0.1,
  ids: { ref?: string; cand?: string } = {},
): Comparison {
  const metrics = METRIC_ORDER.map((m) => deltaFor(m, ref[m], cand[m], tolerance));
  return {
    reference_run_id: ids.ref ?? '',
    candidate_run_id: ids.cand ?? '',
    tolerance,
    metrics,
    // Same rule as the server (internal/report): max_ms is a single noisy
    // sample, so it is reported but never makes a run a regression.
    regression: metrics.some((x) => x.verdict === 'worse' && x.metric !== 'max_ms'),
  };
}

/** How far past its tolerance a delta is (bigger = worse); used to rank regressions. */
function severity(d: MetricDelta, tolerance: number): number {
  const info = metricInfo(d.metric);
  const tol = info.absolute ? tolerance / 10 : tolerance;
  const signed = info.lowerIsBetter ? d.delta : -d.delta;
  return tol > 0 ? signed / tol : signed;
}

/** The worst regressed metric of a comparison, if any. */
export function worstDelta(c: Comparison | undefined | null): MetricDelta | undefined {
  if (!c) return undefined;
  // max_ms is a single noisy sample and never counts as a regression
  // (server rule), so it is never the headline either.
  const worse = (c.metrics ?? []).filter(
    (m) => m.verdict === 'worse' && m.metric !== 'max_ms' && Number.isFinite(m.delta),
  );
  if (worse.length === 0) return undefined;
  const tol = c.tolerance > 0 ? c.tolerance : 0.1;
  return worse.reduce((a, b) => (severity(b, tol) > severity(a, tol) ? b : a));
}

/** The most improved metric (for a "Better" badge), if any. */
export function bestDelta(c: Comparison | undefined | null): MetricDelta | undefined {
  if (!c) return undefined;
  const better = (c.metrics ?? []).filter((m) => m.verdict === 'better' && Number.isFinite(m.delta));
  if (better.length === 0) return undefined;
  const tol = c.tolerance > 0 ? c.tolerance : 0.1;
  return better.reduce((a, b) => (severity(b, tol) < severity(a, tol) ? b : a));
}

/** "p95 +23%" for the worst regression, '' when there is none. */
export function regressionText(c: Comparison | undefined | null): string {
  const w = worstDelta(c);
  return w ? `${metricInfo(w.metric).label} ${fmtDelta(w)}` : '';
}

export type OverallVerdict = 'regressed' | 'improved' | 'unchanged' | 'none';

/**
 * Overall verdict of a comparison. `regression` (computed by the server, or
 * by compareSummaries with the same rule) is authoritative; otherwise any
 * better metric other than max_ms = improved.
 */
export function overallVerdict(c: Comparison | undefined | null): OverallVerdict {
  if (!c) return 'none';
  if (c.regression) return 'regressed';
  if ((c.metrics ?? []).some((m) => m.verdict === 'better' && m.metric !== 'max_ms')) return 'improved';
  return 'unchanged';
}

export function verdictLabel(v: Verdict | string): string {
  switch (v) {
    case 'better':
      return 'Better';
    case 'worse':
      return 'Worse';
    case 'same':
      return 'Same';
    default:
      return 'n/a';
  }
}

/** CSS tone class for a verdict (better = good, worse = bad, else neutral). */
export function verdictTone(v: Verdict | string): 'good' | 'bad' | '' {
  return v === 'better' ? 'good' : v === 'worse' ? 'bad' : '';
}

/** Reads a delta of `metric` out of a comparison. */
export function findDelta(c: Comparison | undefined | null, metric: string): MetricDelta | undefined {
  return (c?.metrics ?? []).find((m) => m.metric === metric);
}

// =====================================================================
// Compare page URL
// =====================================================================

export const MAX_COMPARE = 6;

/** Run ids from `?runs=a,b,c` or the legacy `?a=&b=` (deduplicated, max 6). */
export function parseRunIds(params: URLSearchParams): string[] {
  const raw = params.get('runs');
  const list = raw !== null ? raw.split(',') : [params.get('a') ?? '', params.get('b') ?? ''];
  const out: string[] = [];
  for (const x of list) {
    const id = x.trim();
    if (id && !out.includes(id)) out.push(id);
    if (out.length >= MAX_COMPARE) break;
  }
  return out;
}

/** Moves item `from` to index `to` (returns a new array). */
export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  const out = list.slice();
  if (from < 0 || from >= out.length || to < 0 || to >= out.length || from === to) return out;
  const [x] = out.splice(from, 1);
  out.splice(to, 0, x);
  return out;
}

/** Parses a tolerance in percent ("10", "7.5%") to a fraction; null if invalid. */
export function parseTolerance(s: string): number | null {
  const v = Number(String(s).trim().replace(/%$/, ''));
  if (!Number.isFinite(v) || v < 0 || v > 100) return null;
  return v / 100;
}

// =====================================================================
// Run naming
// =====================================================================

/** "Nov 3" / "Nov 3, 14:05" in UTC-agnostic local time. */
export function fmtShortDate(iso: string, withTime = false): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return '—';
  const d = t.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
  if (!withTime) return d;
  return `${d}, ${t.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}`;
}

/** A run's display name: its label, else its start date and time. */
export function runName(r: { label?: string; started_at: string }): string {
  const l = (r.label ?? '').trim();
  return l || fmtShortDate(r.started_at, true);
}

// =====================================================================
// CSV
// =====================================================================

export function csvCell(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'number' && !Number.isFinite(v)) return '';
  const s = String(v);
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

export function toCsv(rows: unknown[][]): string {
  return rows.map((r) => r.map(csvCell).join(',')).join('\n') + '\n';
}

/**
 * Compare result as CSV: one row per metric (and per step metric), with the
 * value of each run and, for candidates, the delta and verdict vs the reference.
 * Values are raw numbers (ms, req/s, error rate as a fraction).
 */
export function compareCsv(result: CompareResult): string {
  const runs = result.runs;
  const header: string[] = ['step', 'metric'];
  runs.forEach((r, i) => {
    const name = runName(r.run);
    header.push(i === 0 ? `${name} (reference)` : name);
    if (i > 0) header.push(`${name} delta`, `${name} verdict`);
  });
  const rows: unknown[][] = [
    ['', 'run_id', ...runs.flatMap((r, i) => (i === 0 ? [r.run.id] : [r.run.id, '', '']))],
    ['', 'window', ...runs.flatMap((r, i) => (i === 0 ? [windowLabel(r.window)] : [windowLabel(r.window), '', '']))],
    ['', 'requests', ...runs.flatMap((r, i) => (i === 0 ? [r.summary.requests] : [r.summary.requests, '', '']))],
  ];
  for (const m of METRIC_ORDER) {
    const row: unknown[] = ['', m];
    runs.forEach((r, i) => {
      row.push(r.summary[m]);
      if (i > 0) {
        const d = findDelta(result.comparisons[i - 1], m);
        row.push(d && Number.isFinite(d.delta) ? Number(d.delta.toFixed(6)) : '', d?.verdict ?? '');
      }
    });
    rows.push(row);
  }
  const steps = new Set<string>();
  runs.forEach((r) => Object.keys(r.summary.by_step ?? {}).forEach((s) => steps.add(s)));
  for (const step of steps) {
    for (const m of METRIC_ORDER) {
      const row: unknown[] = [step, m];
      runs.forEach((r, i) => {
        row.push(r.summary.by_step?.[step]?.[m]);
        if (i > 0) {
          const d = (result.comparisons[i - 1]?.by_step?.[step] ?? []).find((x) => x.metric === m);
          row.push(d && Number.isFinite(d.delta) ? Number(d.delta.toFixed(6)) : '', d?.verdict ?? '');
        }
      });
      rows.push(row);
    }
  }
  return toCsv([header, ...rows]);
}
