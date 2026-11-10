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
