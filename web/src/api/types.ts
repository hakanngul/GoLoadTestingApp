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
}

export interface StartRunRequest {
  /** 0 => 1 (local). */
  workers: number;
}

export interface APIErrorBody {
  error: string;
  details?: string[];
}

export interface Health {
  status: string;
  version: string;
}
