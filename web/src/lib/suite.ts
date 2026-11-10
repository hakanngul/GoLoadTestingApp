// Helpers for test suites (pure; unit-tested in test/suite.test.mjs).
import type {
  Suite,
  SuiteItemStatus,
  SuiteRequest,
  SuiteRun,
  SuiteRunItem,
} from "../api/types";

/** Editable suite item (key keeps React rows stable while reordering). */
export interface SuiteFormItem {
  key: string;
  test_id: string;
  workers: number;
}

export interface SuiteForm {
  name: string;
  description: string;
  mode: Suite["mode"];
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
    name: "",
    description: "",
    mode: "sequential",
    stop_on_failure: false,
    gap_s: 0,
    items: [],
  };
}

export function suiteToForm(s: Suite): SuiteForm {
  return {
    name: s.name,
    description: s.description,
    mode: s.mode,
    stop_on_failure: s.stop_on_failure,
    gap_s: Math.round(s.gap_ms / 1000),
    items: s.items.map((it) => ({
      key: itemKey(),
      test_id: it.test_id,
      workers: it.workers,
    })),
  };
}

export function formToRequest(f: SuiteForm): SuiteRequest {
  const seqMode = f.mode === "sequential";
  const gap = Number.isFinite(f.gap_s) ? Math.max(0, Math.round(f.gap_s)) : 0;
  return {
    name: f.name.trim(),
    description: f.description.trim(),
    mode: f.mode,
    stop_on_failure: seqMode && f.stop_on_failure,
    gap_ms: seqMode ? gap * 1000 : 0,
    items: f.items.map((it) => ({
      test_id: it.test_id,
      workers: Math.max(0, Math.round(it.workers || 0)),
    })),
  };
}

/** Moves the item at index i by delta (-1 up, +1 down); out of range = unchanged. */
export function moveItem<T>(
  items: readonly T[],
  i: number,
  delta: number,
): T[] {
  const j = i + delta;
  if (i < 0 || i >= items.length || j < 0 || j >= items.length)
    return [...items];
  const out = [...items];
  [out[i], out[j]] = [out[j], out[i]];
  return out;
}

export function isActive(r: SuiteRun | undefined): boolean {
  return !!r && (r.status === "running" || r.status === "queued");
}

/** Badge tone (CSS class) of a suite item status. */
export function itemTone(s: SuiteItemStatus): string {
  switch (s) {
    case "passed":
      return "good";
    case "failed":
    case "error":
      return "bad";
    case "stopped":
      return "warn";
    case "running":
    case "queued":
      return "info pulse";
    default:
      return "";
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
    if (
      it.status === "pending" ||
      it.status === "running" ||
      it.status === "queued"
    )
      continue;
    done += 1;
    if (it.status === "passed") passed += 1;
    else if (it.status === "skipped") skipped += 1;
    else failed += 1;
  }
  return { total: r.items.length, done, passed, failed, skipped };
}

/** Seconds an item ran (to now while running); undefined if never started. */
export function itemDurationS(
  it: SuiteRunItem,
  now = Date.now(),
): number | undefined {
  if (!it.started_at) return undefined;
  const start = Date.parse(it.started_at);
  const end = it.finished_at ? Date.parse(it.finished_at) : now;
  return Math.max(0, (end - start) / 1000);
}

/** One-line description of how a suite runs. */
export function describeMode(
  s: Pick<Suite, "mode" | "stop_on_failure" | "gap_ms">,
): string {
  if (s.mode === "parallel") return "All tests at once";
  const parts = ["One after another"];
  if (s.gap_ms > 0) parts.push(`${Math.round(s.gap_ms / 1000)} s pause`);
  if (s.stop_on_failure) parts.push("stops on first failure");
  return parts.join(" · ");
}
