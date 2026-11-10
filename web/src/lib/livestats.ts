// Pure helpers for the live Statistics / Failures tables (Locust-style) and
// the grouped failures of a finished run. No React here so `pnpm test` can
// import them directly.
import { ERROR_GROUP_OTHER, type ErrorGroup, type LiveStats, type Summary } from '../api/types.ts';

export const AGGREGATED = 'Aggregated';

/** Which summary the statistics table shows. */
export type StatsMode = 'total' | 'current';

export interface StatRow {
  name: string;
  /** True for the "Aggregated" row (always rendered last). */
  aggregate: boolean;
  requests: number;
  fails: number;
  /** Fraction 0..1. */
  failRate: number;
  /** RPS over the last second (from `current`), whatever the mode. */
  currentRps: number;
  p50: number;
  p90: number;
  p95: number;
  p99: number;
  max: number;
  avg: number;
}

export type StatSortKey = 'name' | 'requests' | 'fails' | 'failRate' | 'currentRps' | 'p50' | 'p90' | 'p95' | 'p99' | 'max' | 'avg';

export interface StatSort {
  key: StatSortKey;
  dir: 'asc' | 'desc';
}

const num = (v: number | undefined | null): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);

function rowFrom(name: string, s: Summary | undefined, currentRps: number, aggregate: boolean): StatRow {
  const requests = num(s?.requests);
  const fails = num(s?.errors);
  return {
    name,
    aggregate,
    requests,
    fails,
    failRate: s && Number.isFinite(s.error_rate) ? s.error_rate : requests > 0 ? fails / requests : 0,
    currentRps: num(currentRps),
    p50: num(s?.p50_ms),
    p90: num(s?.p90_ms),
    p95: num(s?.p95_ms),
    p99: num(s?.p99_ms),
    max: num(s?.max_ms),
    avg: num(s?.mean_ms),
  };
}

/**
 * Builds the per-endpoint rows (step order of `total.by_step`, then steps only
 * seen in `current`) plus the "Aggregated" row (last). In `current` mode the
 * numbers come from the last-second summary; Current RPS always does.
 */
export function buildStatRows(stats: LiveStats | null | undefined, mode: StatsMode = 'total'): StatRow[] {
  if (!stats) return [];
  const total = stats.total?.by_step ?? {};
  const current = stats.current?.by_step ?? {};
  const names = [...Object.keys(total)];
  for (const n of Object.keys(current)) if (!(n in total)) names.push(n);
  const src = mode === 'current' ? current : total;
  const rows = names.map((n) => rowFrom(n, src[n], num(current[n]?.rps), false));
  const agg = mode === 'current' ? stats.current : stats.total;
  rows.push(rowFrom(AGGREGATED, agg, num(stats.current?.rps), true));
  return rows;
}

/** Sorts the endpoint rows; the Aggregated row stays last. Stable for ties (by name). */
export function sortStatRows(rows: StatRow[], sort: StatSort | null): StatRow[] {
  const body = rows.filter((r) => !r.aggregate);
  const agg = rows.filter((r) => r.aggregate);
  if (sort) {
    const m = sort.dir === 'asc' ? 1 : -1;
    body.sort((a, b) => {
      let c: number;
      if (sort.key === 'name') c = a.name.localeCompare(b.name);
      else c = a[sort.key] - b[sort.key];
      return c !== 0 ? c * m : a.name.localeCompare(b.name);
    });
  }
  return [...body, ...agg];
}

/** Next sort state when a header is clicked: numbers start descending, names ascending; a third click clears. */
export function nextSort(cur: StatSort | null, key: StatSortKey): StatSort | null {
  const first: StatSort['dir'] = key === 'name' ? 'asc' : 'desc';
  if (!cur || cur.key !== key) return { key, dir: first };
  if (cur.dir === first) return { key, dir: first === 'asc' ? 'desc' : 'asc' };
  return null;
}

/** Names of rows whose fail count went up since `prev` (name -> fails). */
export function increasedFails(prev: ReadonlyMap<string, number>, rows: StatRow[]): Set<string> {
  const out = new Set<string>();
  for (const r of rows) {
    const p = prev.get(r.name);
    if (p !== undefined && r.fails > p) out.add(r.name);
  }
  return out;
}

// ---------------------------------------------------------------- failures

/** Human status of an error group / sample: 0 is a transport error or timeout. */
export function errorStatusLabel(status: number): string {
  return status === 0 ? 'network/timeout' : String(status);
}

/** Tone class of a status (5xx / transport = bad, rest = warn). */
export function errorStatusTone(status: number): 'bad' | 'warn' {
  return status === 0 || status >= 500 ? 'bad' : 'warn';
}

export function isOverflowGroup(g: Pick<ErrorGroup, 'message'>): boolean {
  return g.message === ERROR_GROUP_OTHER;
}

export function groupKey(g: Pick<ErrorGroup, 'step' | 'status' | 'message'>): string {
  return `${g.step}\u0000${g.status}\u0000${g.message}`;
}

/** Count desc (ties: step, status, message); the overflow group always last. */
export function sortErrorGroups(groups: readonly ErrorGroup[] | null | undefined): ErrorGroup[] {
  return [...(groups ?? [])].sort((a, b) => {
    const oa = isOverflowGroup(a) ? 1 : 0;
    const ob = isOverflowGroup(b) ? 1 : 0;
    if (oa !== ob) return oa - ob;
    if (b.count !== a.count) return b.count - a.count;
    return a.step.localeCompare(b.step) || a.status - b.status || a.message.localeCompare(b.message);
  });
}

/** Keeps groups of one step ('' = all). The overflow group spans steps and is always kept. */
export function filterErrorGroups(groups: ErrorGroup[], step: string): ErrorGroup[] {
  if (!step) return groups;
  return groups.filter((g) => g.step === step || isOverflowGroup(g));
}

/** Distinct steps that have failures, sorted (overflow group excluded). */
export function errorSteps(groups: readonly ErrorGroup[] | null | undefined): string[] {
  const s = new Set<string>();
  for (const g of groups ?? []) if (!isOverflowGroup(g) && g.step) s.add(g.step);
  return [...s].sort((a, b) => a.localeCompare(b));
}

export function totalFailures(groups: readonly ErrorGroup[] | null | undefined): number {
  let n = 0;
  for (const g of groups ?? []) n += num(g.count);
  return n;
}

/** Keys of groups not in `prev`. */
export function newGroupKeys(prev: ReadonlySet<string>, groups: readonly ErrorGroup[]): Set<string> {
  const out = new Set<string>();
  for (const g of groups) {
    const k = groupKey(g);
    if (!prev.has(k)) out.add(k);
  }
  return out;
}

/** "4s ago", "2m 05s ago", "1h 3m ago" — precise enough for a live table. */
export function fmtAgo(iso: string | undefined | null, now: number): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '—';
  const d = Math.max(0, Math.floor((now - t) / 1000));
  if (d < 1) return 'now';
  if (d < 60) return `${d}s ago`;
  if (d < 3600) return `${Math.floor(d / 60)}m ${String(d % 60).padStart(2, '0')}s ago`;
  if (d < 86400) return `${Math.floor(d / 3600)}h ${Math.floor((d % 3600) / 60)}m ago`;
  return `${Math.floor(d / 86400)}d ago`;
}

/** Offset of `iso` from the run start as "+m:ss" (for finished runs). */
export function fmtRunOffset(iso: string | undefined | null, startedAt: string): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  const s = Date.parse(startedAt);
  if (!Number.isFinite(t) || !Number.isFinite(s)) return '—';
  const d = Math.max(0, Math.floor((t - s) / 1000));
  const h = Math.floor(d / 3600);
  const m = Math.floor((d % 3600) / 60);
  const sec = String(d % 60).padStart(2, '0');
  return h > 0 ? `+${h}:${String(m).padStart(2, '0')}:${sec}` : `+${m}:${sec}`;
}
