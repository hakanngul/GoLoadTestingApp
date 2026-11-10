// Mirrors internal/model/model.go (CONTRACT). Durations are milliseconds,
// timestamps are RFC 3339 strings.

export interface FileInfo {
  name: string;
  size: number;
}

export interface Test {
  id: string;
  name: string;
  yaml: string;
  files: FileInfo[] | null;
  created_at: string;
  updated_at: string;
  /** Filled by GET /api/tests only (summary may be absent). */
  last_run?: Run;
  /** Reference run new runs are compared against (absent/empty = none). */
  baseline_run_id?: string;
}

export type RunStatus = 'queued' | 'running' | 'passed' | 'failed' | 'stopped' | 'error';

export const FINISHED_STATUSES: readonly RunStatus[] = ['passed', 'failed', 'stopped', 'error'];

export function isFinished(s: RunStatus): boolean {
  return FINISHED_STATUSES.includes(s);
}

export interface Run {
  id: string;
  test_id: string;
  test_name: string;
  status: RunStatus;
  /** Scenario frozen at start. */
  yaml: string;
  workers: number;
  started_at: string;
  finished_at?: string;
  summary?: Summary;
  thresholds: ThresholdResult[] | null;
  /** 0..1, highest load-generator CPU seen. */
  peak_cpu: number;
  warnings: string[] | null;
  error?: string;
  /** Short tag, e.g. "v1.4.2" (<= 100 chars). */
  label: string;
  /** Free text (<= 10k chars). */
  notes: string;
  /** Schedule that started the run (absent = started by hand). */
  schedule_id?: string;
}

export interface Summary {
  requests: number;
  errors: number;
  dropped: number;
  /** Fraction 0..1. */
  error_rate: number;
  rps: number;
  mean_ms: number;
  p50_ms: number;
  p90_ms: number;
  p95_ms: number;
  p99_ms: number;
  max_ms: number;
  by_step?: Record<string, Summary>;
}

export interface ThresholdResult {
  metric: string;
  op: string;
  value: number;
  actual: number;
  passed: boolean;
}

/** One second of a run (live stream and stored time series). */
export interface MetricPoint {
  t: string;
  /** Seconds since run start. */
  elapsed: number;
  /** Target rps (open) or VUs (closed). */
  target: number;
  active: number;
  requests: number;
  errors: number;
  dropped: number;
  rps: number;
  error_rate: number;
  mean_ms: number;
  p50_ms: number;
  p95_ms: number;
  p99_ms: number;
  max_ms: number;
  /** 0..1, max across workers. */
  cpu: number;
  workers: number;
}

export interface ErrorSample {
  t: string;
  step: string;
  status: number;
  message: string;
}

/** Failed requests with the same step, status and message (GET /api/runs/{id}/error-groups). */
export interface ErrorGroup {
  step: string;
  /** 0 = transport error / timeout. */
  status: number;
  /**
   * Normalized message: request-specific parts replaced by placeholders
   * ({n}, {uuid}, {id}, {v}, {ts}, {dur}, {port}).
   */
  message: string;
  /** First raw message seen for the group (may equal `message`; absent in older runs). */
  sample?: string;
  count: number;
  /** RFC 3339. */
  first: string;
  last: string;
}

/** Max distinct error groups tracked per run (model.MaxErrorGroups). */
export const MAX_ERROR_GROUPS = 100;
/** Message of the overflow group (model.ErrorGroupOther). */
export const ERROR_GROUP_OTHER = '(other errors)';

/** SSE `event: stats`, once per second while a run is active (model.LiveStats). */
export interface LiveStats {
  /** Seconds since run start. */
  elapsed: number;
  /** Cumulative since start, by_step filled. */
  total: Summary;
  /** Last one-second interval, by_step filled. */
  current: Summary;
  /** Most frequent first, at most MAX_ERROR_GROUPS (+ overflow group). */
  errors: ErrorGroup[] | null;
}

export type WorkerState = 'idle' | 'busy' | 'lost';

export interface Worker {
  id: string;
  addr: string;
  state: WorkerState | string;
  run_id?: string;
  cpu: number;
  last_seen: string;
  version: string;
}

export interface ValidateResult {
  valid: boolean;
  /** "path: message" */
  errors: string[] | null;
  model?: string;
  duration_s?: number;
  steps?: number;
  /** Non-blocking problems, e.g. {{var}} references no source defines ("path: message"). */
  warnings?: string[] | null;
}

// ---------- test request (POST /api/tests/try) ----------

export type TryState = 'passed' | 'failed' | 'not_run';

export interface TryHeader {
  name: string;
  value: string;
}

export interface TryExtract {
  var: string;
  source: string;
  expr: string;
  /** Truncated / masked. */
  value?: string;
  ok: boolean;
  error?: string;
}

export interface TryAssertion {
  assertion: string;
  passed: boolean;
  message?: string;
}

export interface TryMessage {
  name: string;
  duration_ms: number;
  error?: string;
}

/** One followed redirect of a trial step (cookie names only, never values). */
export interface TryRedirect {
  status: number;
  method: string;
  url: string;
  /** Resolved target: the URL of the next request. */
  location: string;
  cookies_sent?: string[];
  cookies_set?: string[];
}

export interface TryStep {
  name: string;
  kind: 'http' | 'ws' | 'login';
  /** Position in scenario steps; -1 = auth:login. */
  index: number;
  state: TryState;
  method?: string;
  url?: string;
  request_headers: TryHeader[];
  request_body?: string;
  status: number;
  duration_ms: number;
  bytes: number;
  response_headers: TryHeader[];
  body?: string;
  body_truncated?: boolean;
  body_binary?: boolean;
  /** Resolved target of a final 3xx (a redirect that was not followed). */
  location?: string;
  /** Redirects followed before the final response; url is the first request. */
  redirects?: TryRedirect[];
  /** URL that produced the final response, when redirects were followed. */
  final_url?: string;
  /** Names of the cookies sent with the (final) request / stored from its response. */
  cookies_sent?: string[];
  cookies_set?: string[];
  extracted: TryExtract[];
  assertions: TryAssertion[];
  messages?: TryMessage[];
  error?: string;
}

export interface TryFix {
  /** base_url: set target.base_url; step_path: set steps[step].path (auth.login for -1). */
  kind: 'base_url' | 'step_path';
  step: number;
  value: string;
  label: string;
}

export interface TryHint {
  level: 'error' | 'warning' | 'info';
  step?: string;
  message: string;
  fix?: TryFix;
}

export interface TryResult {
  ok: boolean;
  duration_ms: number;
  data: Record<string, string>;
  data_note?: string;
  steps: TryStep[];
  hints: TryHint[];
}

export interface StartRunRequest {
  /** 0 => 1 (local). */
  workers: number;
  label?: string;
  notes?: string;
}

/** Body of PATCH /api/runs/{id}; omitted = unchanged. */
export interface UpdateRunRequest {
  label?: string;
  notes?: string;
}

// ---------- Reporting ----------

/** full = whole run, steady = without ramp-up/down, custom = [from, to) seconds. */
export type WindowKind = 'full' | 'steady' | 'custom';

/** A resolved time range of a run, in seconds since start. */
export interface Window {
  kind: WindowKind;
  from: number;
  to: number;
}

/** A run aggregated over a window. GET /api/runs/{id}/report. */
export interface RunReport {
  run: Run;
  window: Window;
  /** Over `window` (incl. by_step). */
  summary: Summary;
  /** Re-evaluated over `window`. */
  thresholds: ThresholdResult[] | null;
  /** vs the test's baseline run, same window kind. */
  baseline?: Comparison;
}

export type Verdict = 'better' | 'same' | 'worse' | 'n/a';

export type DeltaMetric = 'p50_ms' | 'p90_ms' | 'p95_ms' | 'p99_ms' | 'max_ms' | 'mean_ms' | 'rps' | 'error_rate';

/**
 * One metric of a comparison. `delta` is relative ((cand-ref)/ref) except for
 * error_rate where it is absolute (percentage points as a fraction).
 */
export interface MetricDelta {
  metric: DeltaMetric | string;
  reference: number;
  candidate: number;
  delta: number;
  verdict: Verdict;
}

export interface Comparison {
  reference_run_id: string;
  candidate_run_id: string;
  /** Relative, default 0.10; error_rate uses tolerance/10 absolute. */
  tolerance: number;
  metrics: MetricDelta[] | null;
  /** Any metric worse. */
  regression: boolean;
  /** Steps present in both runs. */
  by_step?: Record<string, MetricDelta[]>;
}

/** GET /api/compare. runs[0] is the reference; comparisons[i] compares runs[i+1] to it. */
export interface CompareResult {
  runs: RunReport[];
  comparisons: Comparison[];
}

/** One finished run of a test. GET /api/tests/{id}/trend (oldest first). */
export interface TrendPoint {
  run_id: string;
  started_at: string;
  label: string;
  status: RunStatus;
  workers: number;
  /** Without by_step. */
  summary: Summary;
  is_baseline: boolean;
}

/** GET /api/reports/overview: the reporting dashboard. */
export interface Overview {
  /** ?days= window start. */
  since: string;
  runs: number;
  passed: number;
  failed: number;
  /** error + stopped */
  errored: number;
  requests: number;
  tests: TestHealth[] | null;
  /** Newest first, max 10, summaries without by_step. */
  recent_runs: Run[] | null;
  runs_per_day: DayCount[] | null;
}

/** One row of the overview: a test's latest run vs baseline. */
export interface TestHealth {
  test_id: string;
  test_name: string;
  latest_run?: Run;
  /** Latest vs baseline (steady window). */
  baseline?: Comparison;
  /** passed / finished runs in the period. */
  pass_rate: number;
  run_count: number;
}

/** Runs started on one UTC day, by outcome. */
export interface DayCount {
  /** YYYY-MM-DD */
  day: string;
  passed: number;
  failed: number;
  other: number;
}

export interface APIErrorBody {
  error: string;
  details?: string[];
}

export interface Health {
  status: string;
  version: string;
  /** serve -retention in seconds; 0 (or absent on older servers) = runs are kept forever. */
  retention_seconds?: number;
}

// ---------- schedules ----------

export type ScheduleKind = 'once' | 'recurring';
export type OverlapPolicy = 'skip' | 'queue';
export type ScheduleStatus = 'started' | 'queued' | 'skipped' | 'missed' | 'error';

/** Load override of a scheduled run (exactly one of users / rate). */
export interface LoadOverride {
  /** scale: keep the stage shape, scale the peak; profile: ramp-up, hold, ramp-down. */
  mode: 'scale' | 'profile';
  users?: number;
  rate?: number;
  ramp_up_ms?: number;
  hold_ms?: number;
  ramp_down_ms?: number;
}

export interface ScheduleSpec {
  test_id: string;
  name: string;
  kind: ScheduleKind;
  /** once: local wall time "YYYY-MM-DDTHH:MM" in timezone. */
  at?: string;
  /** recurring: 5-field cron expression evaluated in timezone. */
  cron?: string;
  /** IANA zone, e.g. Europe/Istanbul. */
  timezone: string;
  load?: LoadOverride;
  workers: number;
  label: string;
  notes: string;
  overlap: OverlapPolicy;
  queue_timeout_ms?: number;
}

export interface Schedule extends ScheduleSpec {
  id: string;
  test_name: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
  next_run_at?: string;
  last_run_at?: string;
  last_run_id?: string;
  last_status?: ScheduleStatus;
  last_message?: string;
}

export interface ScheduleRequest extends ScheduleSpec {
  enabled?: boolean;
}

export interface SchedulePreviewRequest {
  kind: ScheduleKind;
  at?: string;
  cron?: string;
  timezone: string;
  count?: number;
}

export interface SchedulePreview {
  valid: boolean;
  errors: string[];
  times: string[];
}

// ---------- test suites ----------

export type SuiteMode = 'sequential' | 'parallel';

/** Item status: pending, skipped or the run's status. */
export type SuiteItemStatus = 'pending' | 'skipped' | RunStatus;

export interface SuiteItem {
  test_id: string;
  /** Empty when the test has been deleted (the item is skipped). */
  test_name: string;
  /** 0 = 1 (local). */
  workers: number;
}

export interface Suite {
  id: string;
  name: string;
  description: string;
  mode: SuiteMode;
  /** Sequential only: a test that does not pass skips the rest. */
  stop_on_failure: boolean;
  /** Sequential only: pause between two tests. */
  gap_ms: number;
  items: SuiteItem[];
  created_at: string;
  updated_at: string;
  /** Filled by GET /api/suites. */
  last_run?: SuiteRun;
}

export interface SuiteRequest {
  name: string;
  description: string;
  mode: SuiteMode;
  stop_on_failure: boolean;
  gap_ms: number;
  items: { test_id: string; workers: number }[];
}

export interface StartSuiteRequest {
  label?: string;
}

export interface SuiteRunItem {
  test_id: string;
  test_name: string;
  workers: number;
  run_id?: string;
  status: SuiteItemStatus;
  error?: string;
  started_at?: string;
  finished_at?: string;
  /** Copied from the run when it finishes (no by_step). */
  summary?: Summary;
}

export interface SuiteRun {
  id: string;
  suite_id: string;
  suite_name: string;
  status: RunStatus;
  mode: SuiteMode;
  stop_on_failure: boolean;
  gap_ms: number;
  label: string;
  started_at: string;
  finished_at?: string;
  items: SuiteRunItem[];
  error?: string;
}
