// Helpers for test suites (pure; unit-tested in test/suite.test.mjs).
import type { Suite, SuiteItemStatus, SuiteRequest, SuiteRun, SuiteRunItem } from '../api/types';
import { buildLoadOverride, defaultLoadForm, loadFormFromOverride, type LoadForm } from './schedule.ts';
import type { LoadModel } from './scenario.ts';

/** Editable suite item (key keeps React rows stable while reordering). */
export interface SuiteFormItem {
  key: string;
  test_id: string;
  workers: number;
  /** mode 'keep' = the test's own load. */
  load: LoadForm;
}

/** Load model and peak of a test's saved scenario (for the load form). */
export interface TestLoadInfo {
  model: LoadModel;
  peak: number;
}

export function newFormItem(testId: string, info: TestLoadInfo | undefined): SuiteFormItem {
  return { key: itemKey(), test_id: testId, workers: 0, load: defaultLoadForm(info?.model ?? 'closed', info?.peak ?? 10) };
}

export interface SuiteForm {
  name: string;
  description: string;
  mode: Suite['mode'];
  stop_on_failure: boolean;
  /** Gap between tests, seconds (sequential only). */
  gap_s: number;
  items: SuiteFormItem[];
}

let seq = 0;
export function itemKey(): string {
  seq += 1;
  return `i${seq}`;
}

export function emptySuiteForm(): SuiteForm {
  return {
    name: '',
    description: '',
    mode: 'sequential',
    stop_on_failure: false,
    gap_s: 0,
    items: [],
  };
}

export function suiteToForm(s: Suite, infoOf: (testId: string) => TestLoadInfo | undefined = () => undefined): SuiteForm {
  return {
    name: s.name,
    description: s.description,
    mode: s.mode,
    stop_on_failure: s.stop_on_failure,
    gap_s: Math.round(s.gap_ms / 1000),
    items: s.items.map((it) => {
      const info = infoOf(it.test_id);
      return {
        key: itemKey(),
        test_id: it.test_id,
        workers: it.workers,
        load: loadFormFromOverride(it.load, info?.model ?? 'closed', info?.peak ?? 10),
      };
    }),
  };
}

/**
 * Converts the form into the API request. errors holds client-side load
 * problems keyed like the server's details ("items[1].load.users").
 */
export function formToRequest(
  f: SuiteForm,
  infoOf: (testId: string) => TestLoadInfo | undefined = () => undefined,
): { req: SuiteRequest; errors: Record<string, string> } {
  const seqMode = f.mode === 'sequential';
  const gap = Number.isFinite(f.gap_s) ? Math.max(0, Math.round(f.gap_s)) : 0;
  const errors: Record<string, string> = {};
  const items = f.items.map((it, i) => {
    const out: SuiteRequest['items'][number] = { test_id: it.test_id, workers: Math.max(0, Math.round(it.workers || 0)) };
    const built = buildLoadOverride(it.load, infoOf(it.test_id)?.model ?? 'closed');
    for (const [k, v] of Object.entries(built.errors)) errors[`items[${i}].${k}`] = v;
    if (built.load) out.load = built.load;
    return out;
  });
  return {
    req: {
      name: f.name.trim(),
      description: f.description.trim(),
      mode: f.mode,
      stop_on_failure: seqMode && f.stop_on_failure,
      gap_ms: seqMode ? gap * 1000 : 0,
      items,
    },
    errors,
  };
}

/** Moves the item at index i by delta (-1 up, +1 down); out of range = unchanged. */
export function moveItem<T>(items: readonly T[], i: number, delta: number): T[] {
  const j = i + delta;
  if (i < 0 || i >= items.length || j < 0 || j >= items.length) return [...items];
  const out = [...items];
  [out[i], out[j]] = [out[j], out[i]];
  return out;
}

export function isActive(r: SuiteRun | undefined): boolean {
  return !!r && (r.status === 'running' || r.status === 'queued');
}

/** Badge tone (CSS class) of a suite item status. */
export function itemTone(s: SuiteItemStatus): string {
  switch (s) {
    case 'passed':
      return 'good';
    case 'failed':
    case 'error':
      return 'bad';
    case 'stopped':
      return 'warn';
    case 'running':
    case 'queued':
      return 'info pulse';
    default:
      return '';
  }
}

/** Counts of items by outcome: done = finished or skipped. */
export function progress(r: SuiteRun): {
  total: number;
  done: number;
  passed: number;
  failed: number;
  skipped: number;
} {
  let done = 0;
  let passed = 0;
  let failed = 0;
  let skipped = 0;
  for (const it of r.items) {
    if (it.status === 'pending' || it.status === 'running' || it.status === 'queued') continue;
    done += 1;
    if (it.status === 'passed') passed += 1;
    else if (it.status === 'skipped') skipped += 1;
    else failed += 1;
  }
  return { total: r.items.length, done, passed, failed, skipped };
}

/** Seconds an item ran (to now while running); undefined if never started. */
export function itemDurationS(it: SuiteRunItem, now = Date.now()): number | undefined {
  if (!it.started_at) return undefined;
  const start = Date.parse(it.started_at);
  const end = it.finished_at ? Date.parse(it.finished_at) : now;
  return Math.max(0, (end - start) / 1000);
}

/** One-line description of how a suite runs. */
export function describeMode(s: Pick<Suite, 'mode' | 'stop_on_failure' | 'gap_ms'>): string {
  if (s.mode === 'parallel') return 'All tests at once';
  const parts = ['One after another'];
  if (s.gap_ms > 0) parts.push(`${Math.round(s.gap_ms / 1000)} s pause`);
  if (s.stop_on_failure) parts.push('stops on first failure');
  return parts.join(' · ');
}

/** Per-test-case numbers of one suite run in the trend. */
export interface SuiteTrendCase {
  p95_ms: number;
  error_rate: number;
}

export interface SuiteTrendPoint {
  id: string;
  started_at: string;
  label: string;
  status: SuiteRun['status'];
  /**
   * passed / total items, in 0..1; null if the run has no items. Skipped items
   * count as not passed (they are only skipped after a failure, so excluding
   * them would hide it).
   */
  passRate: number | null;
  /** By case key; only items that finished with a summary. */
  cases: Record<string, SuiteTrendCase>;
}

export interface SuiteTrendCaseInfo {
  key: string;
  test_id: string;
  /** Latest known test name; '' if the test was deleted. */
  name: string;
}

export interface SuiteTrend {
  /** Finished runs, oldest first. */
  points: SuiteTrendPoint[];
  /** Every test case seen in the points, ordered by position in the suite. */
  cases: SuiteTrendCaseInfo[];
}

/** Key of a test case within a suite: position + test id (a test may appear twice). */
export function caseKey(index: number, testId: string): string {
  return `${index}:${testId}`;
}

/**
 * Trend over suite runs (given newest first, as the API returns them): active
 * runs are skipped and the rest are returned oldest first.
 */
export function suiteTrend(runs: readonly SuiteRun[]): SuiteTrend {
  const points: SuiteTrendPoint[] = [];
  const info = new Map<string, SuiteTrendCaseInfo>();
  for (const r of runs) {
    if (isActive(r)) continue;
    const cases: Record<string, SuiteTrendCase> = {};
    r.items.forEach((it, i) => {
      const key = caseKey(i, it.test_id);
      // Runs come newest first, so the first name seen is the latest.
      const known = info.get(key);
      if (!known) info.set(key, { key, test_id: it.test_id, name: it.test_name });
      else if (!known.name && it.test_name) known.name = it.test_name;
      if (it.summary) cases[key] = { p95_ms: it.summary.p95_ms, error_rate: it.summary.error_rate };
    });
    const total = r.items.length;
    const passed = r.items.filter((it) => it.status === 'passed').length;
    points.push({
      id: r.id,
      started_at: r.started_at,
      label: r.label,
      status: r.status,
      passRate: total > 0 ? passed / total : null,
      cases,
    });
  }
  points.reverse();
  const order = (k: string) => Number(k.slice(0, k.indexOf(':')));
  const cases = [...info.values()].sort((a, b) => order(a.key) - order(b.key));
  return { points, cases };
}
