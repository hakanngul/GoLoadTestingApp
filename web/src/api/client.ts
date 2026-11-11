import type {
  APIErrorBody,
  APIToken,
  CreateTokenRequest,
  CreateTokenResponse,
  Me,
  CompareResult,
  ErrorGroup,
  ErrorSample,
  Health,
  LiveStats,
  MetricPoint,
  NotificationChannel,
  NotificationRequest,
  NotificationTestResult,
  Overview,
  Run,
  RunReport,
  Schedule,
  SchedulePreview,
  SchedulePreviewRequest,
  ScheduleRequest,
  ScheduleSuiteRunResponse,
  StartRunRequest,
  StartSuiteRequest,
  Suite,
  SuiteRequest,
  SuiteRun,
  Test,
  TrendPoint,
  UpdateRunRequest,
  TryResult,
  ValidateResult,
  Worker,
} from './types';
import { forbiddenMessage } from '../lib/tokens';
import { windowParams, type WindowSel } from '../lib/report';

export const TOKEN_KEY = 'goload.token';

/** Error thrown for every non-2xx API response (body is model.APIError). */
export class APIError extends Error {
  readonly status: number;
  readonly details: string[];

  constructor(status: number, message: string, details: string[] = []) {
    super(message);
    this.name = 'APIError';
    this.status = status;
    this.details = details;
  }
}

export function getToken(): string | null {
  try {
    const t = window.localStorage.getItem(TOKEN_KEY);
    return t && t.trim() ? t.trim() : null;
  } catch {
    return null;
  }
}

export function setToken(token: string): void {
  try {
    window.localStorage.setItem(TOKEN_KEY, token.trim());
  } catch {
    /* storage unavailable (private mode / blocked) */
  }
}

export function clearToken(): void {
  try {
    window.localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* storage unavailable */
  }
}

// ---------- 401 handling ----------
// Any API call that comes back 401 notifies the listeners (the auth gate in
// App), which replaces the whole UI with the sign-in screen.
type UnauthorizedListener = () => void;
const unauthorizedListeners = new Set<UnauthorizedListener>();

export function onUnauthorized(fn: UnauthorizedListener): () => void {
  unauthorizedListeners.add(fn);
  return () => {
    unauthorizedListeners.delete(fn);
  };
}

function notifyUnauthorized() {
  for (const fn of unauthorizedListeners) fn();
}

function authHeaders(): Record<string, string> {
  const t = getToken();
  return t ? { Authorization: `Bearer ${t}` } : {};
}

interface RequestOpts {
  /** Explicit token (sign-in check) instead of the stored one. */
  token?: string;
  /** Do not notify the auth gate on 401 (the caller handles it). */
  quiet401?: boolean;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  raw?: BodyInit,
  opts: RequestOpts = {},
): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(opts.token !== undefined ? { Authorization: `Bearer ${opts.token}` } : authHeaders()),
  };
  let payload: BodyInit | undefined;
  if (raw !== undefined) {
    payload = raw;
    headers['Content-Type'] = 'text/csv';
  } else if (body !== undefined) {
    payload = JSON.stringify(body);
    headers['Content-Type'] = 'application/json';
  }
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: payload });
  } catch (e) {
    throw new APIError(0, `Network error: ${(e as Error).message}`);
  }
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    let details: string[] = [];
    try {
      const b = (await res.json()) as Partial<APIErrorBody>;
      if (b && typeof b.error === 'string') msg = b.error;
      if (b && Array.isArray(b.details)) details = b.details;
    } catch {
      /* non-JSON error body */
    }
    if (res.status === 401 && !opts.quiet401) notifyUnauthorized();
    throw new APIError(res.status, msg, details);
  }
  if (res.status === 204 || res.status === 202) {
    return undefined as T;
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

const enc = encodeURIComponent;

export const api = {
  health: () => request<Health>('GET', '/api/health'),

  /** Checks a token without touching stored state (GET /api/tests). */
  checkToken: (token: string) =>
    request<unknown>('GET', '/api/tests', undefined, undefined, { token, quiet401: true }).then(() => undefined),

  getMe: () => request<Me>('GET', '/api/me'),
  listTokens: () => request<APIToken[] | null>('GET', '/api/tokens').then((r) => r ?? []),
  createToken: (req: CreateTokenRequest) => request<CreateTokenResponse>('POST', '/api/tokens', req),
  deleteToken: (id: string) => request<void>('DELETE', `/api/tokens/${enc(id)}`),

  listTests: () => request<Test[] | null>('GET', '/api/tests').then((r) => r ?? []),
  getTest: (id: string) => request<Test>('GET', `/api/tests/${enc(id)}`),
  createTest: (name: string, yaml: string) => request<Test>('POST', '/api/tests', { name, yaml }),
  updateTest: (id: string, name: string, yaml: string) =>
    request<Test>('PUT', `/api/tests/${enc(id)}`, { name, yaml }),
  deleteTest: (id: string) => request<void>('DELETE', `/api/tests/${enc(id)}`),
  uploadFile: (id: string, name: string, content: Blob) =>
    request<void>('PUT', `/api/tests/${enc(id)}/files/${enc(name)}`, undefined, content),
  deleteFile: (id: string, name: string) =>
    request<void>('DELETE', `/api/tests/${enc(id)}/files/${enc(name)}`),
  validate: (yaml: string, testId?: string) =>
    request<ValidateResult>('POST', '/api/validate', testId ? { yaml, test_id: testId } : { yaml }),
  /** "Test request": one iteration of the (possibly unsaved) scenario. */
  tryRequest: (yaml: string, testId?: string) =>
    request<TryResult>('POST', '/api/tests/try', testId ? { yaml, test_id: testId } : { yaml }),

  startRun: (testId: string, req: StartRunRequest) =>
    request<Run>('POST', `/api/tests/${enc(testId)}/runs`, req),
  listRuns: (opts: { testId?: string; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (opts.testId) q.set('test_id', opts.testId);
    if (opts.limit) q.set('limit', String(opts.limit));
    const qs = q.toString();
    return request<Run[] | null>('GET', `/api/runs${qs ? `?${qs}` : ''}`).then((r) => r ?? []);
  },
  getRun: (id: string) => request<Run>('GET', `/api/runs/${enc(id)}`),
  stopRun: (id: string) => request<void>('POST', `/api/runs/${enc(id)}/stop`),
  runMetrics: (id: string) =>
    request<MetricPoint[] | null>('GET', `/api/runs/${enc(id)}/metrics`).then((r) => r ?? []),
  runErrors: (id: string) =>
    request<ErrorSample[] | null>('GET', `/api/runs/${enc(id)}/errors`).then((r) => r ?? []),
  /** Grouped failure counts (live while running, final after). */
  runErrorGroups: (id: string) =>
    request<ErrorGroup[] | null>('GET', `/api/runs/${enc(id)}/error-groups`).then((r) => r ?? []),

  listWorkers: () => request<Worker[] | null>('GET', '/api/workers').then((r) => r ?? []),

  // ---------- reporting ----------
  updateRun: (id: string, req: UpdateRunRequest) => request<Run>('PATCH', `/api/runs/${enc(id)}`, req),
  deleteRun: (id: string) => request<void>('DELETE', `/api/runs/${enc(id)}`),
  runReport: (id: string, w: WindowSel) =>
    request<RunReport>('GET', `/api/runs/${enc(id)}/report?${windowParams(w).toString()}`),
  compare: (ids: string[], w: WindowSel, tolerance?: number) => {
    const q = windowParams(w);
    q.set('runs', ids.join(','));
    if (tolerance !== undefined && Number.isFinite(tolerance)) q.set('tolerance', String(tolerance));
    return request<CompareResult>('GET', `/api/compare?${q.toString()}`);
  },
  trend: (testId: string, opts: { limit?: number; window?: WindowSel } = {}) => {
    const q = opts.window ? windowParams(opts.window) : new URLSearchParams();
    if (opts.limit) q.set('limit', String(opts.limit));
    return request<TrendPoint[] | null>('GET', `/api/tests/${enc(testId)}/trend?${q.toString()}`).then((r) => r ?? []);
  },
  /** Sets the test's baseline run ("" clears it). */
  setBaseline: (testId: string, runId: string) =>
    request<Test>('PUT', `/api/tests/${enc(testId)}/baseline`, { run_id: runId }),
  overview: (days = 30) => request<Overview>('GET', `/api/reports/overview?days=${days}`),

  // ---------- schedules ----------
  listSchedules: (testId?: string) =>
    request<Schedule[] | null>('GET', `/api/schedules${testId ? `?test_id=${enc(testId)}` : ''}`).then((r) => r ?? []),
  getSchedule: (id: string) => request<Schedule>('GET', `/api/schedules/${enc(id)}`),
  createSchedule: (req: ScheduleRequest) => request<Schedule>('POST', '/api/schedules', req),
  updateSchedule: (id: string, req: ScheduleRequest) => request<Schedule>('PUT', `/api/schedules/${enc(id)}`, req),
  deleteSchedule: (id: string) => request<void>('DELETE', `/api/schedules/${enc(id)}`),
  /** A Run for a test schedule, `{suite_run}` for a suite schedule. */
  runScheduleNow: (id: string) => request<Run | ScheduleSuiteRunResponse>('POST', `/api/schedules/${enc(id)}/run-now`),
  previewSchedule: (req: SchedulePreviewRequest) => request<SchedulePreview>('POST', '/api/schedules/preview', req),

  // ---------- test suites ----------
  listSuites: () => request<Suite[] | null>('GET', '/api/suites').then((r) => r ?? []),
  getSuite: (id: string) => request<Suite>('GET', `/api/suites/${enc(id)}`),
  createSuite: (req: SuiteRequest) => request<Suite>('POST', '/api/suites', req),
  updateSuite: (id: string, req: SuiteRequest) => request<Suite>('PUT', `/api/suites/${enc(id)}`, req),
  deleteSuite: (id: string) => request<void>('DELETE', `/api/suites/${enc(id)}`),
  startSuite: (id: string, req: StartSuiteRequest = {}) => request<SuiteRun>('POST', `/api/suites/${enc(id)}/runs`, req),
  listSuiteRuns: (suiteId: string, limit = 50) =>
    request<SuiteRun[] | null>('GET', `/api/suites/${enc(suiteId)}/runs?limit=${limit}`).then((r) => r ?? []),
  getSuiteRun: (id: string) => request<SuiteRun>('GET', `/api/suite-runs/${enc(id)}`),
  stopSuiteRun: (id: string) => request<void>('POST', `/api/suite-runs/${enc(id)}/stop`),

  // ---------- notifications ----------
  listNotifications: () => request<NotificationChannel[] | null>('GET', '/api/notifications').then((r) => r ?? []),
  createNotification: (req: NotificationRequest) => request<NotificationChannel>('POST', '/api/notifications', req),
  updateNotification: (id: string, req: NotificationRequest) => request<NotificationChannel>('PUT', `/api/notifications/${enc(id)}`, req),
  deleteNotification: (id: string) => request<void>('DELETE', `/api/notifications/${enc(id)}`),
  testNotification: (id: string) => request<NotificationTestResult>('POST', `/api/notifications/${enc(id)}/test`),
};

// ---------- downloads ----------

/** URLs of the run export endpoints (no auth: use `download` to fetch them). */
export const exportUrls = {
  html: (id: string, w?: WindowSel) =>
    `/api/runs/${enc(id)}/report.html${w ? `?${windowParams(w).toString()}` : ''}`,
  csv: (id: string) => `/api/runs/${enc(id)}/export.csv`,
  json: (id: string) => `/api/runs/${enc(id)}/export.json`,
};

function filenameFrom(res: Response, fallback: string): string {
  const cd = res.headers.get('Content-Disposition') ?? '';
  const star = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(cd);
  if (star) {
    try {
      return decodeURIComponent(star[1].trim());
    } catch {
      /* fall through */
    }
  }
  const plain = /filename\s*=\s*"?([^";]+)"?/i.exec(cd);
  return plain ? plain[1].trim() : fallback;
}

/** Saves a Blob under `filename` through a temporary object URL. */
export function saveBlob(blob: Blob, filename: string): void {
  const href = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = href;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(href), 10_000);
}

/**
 * Downloads an API file. Without a stored token a plain link is enough; with
 * one the file is fetched with the Authorization header (a link cannot send
 * headers) and saved via a Blob.
 */
export async function download(url: string, fallbackName: string): Promise<void> {
  const t = getToken();
  if (!t) {
    const a = document.createElement('a');
    a.href = url;
    a.download = fallbackName;
    document.body.appendChild(a);
    a.click();
    a.remove();
    return;
  }
  let res: Response;
  try {
    res = await fetch(url, { headers: authHeaders() });
  } catch (e) {
    throw new APIError(0, `Network error: ${(e as Error).message}`);
  }
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const b = (await res.json()) as Partial<APIErrorBody>;
      if (b && typeof b.error === 'string') msg = b.error;
    } catch {
      /* non-JSON */
    }
    if (res.status === 401) notifyUnauthorized();
    throw new APIError(res.status, msg);
  }
  saveBlob(await res.blob(), filenameFrom(res, fallbackName));
}

export interface RunStreamHandlers {
  onMetric?: (p: MetricPoint) => void;
  onStatus?: (r: Run) => void;
  onDone?: (r: Run) => void;
  /** Live per-step table + error groups, once per second (and right after subscribing). */
  onStats?: (s: LiveStats) => void;
  /** Called when the connection drops; EventSource retries automatically. */
  onConnectionChange?: (connected: boolean) => void;
}

/**
 * Subscribes to the SSE stream of a run. EventSource cannot send headers, so
 * the bearer token (if any) is passed as `?access_token=`. Returns an unsubscribe
 * function. The stream is closed after the `done` event.
 */
export function subscribeRun(id: string, h: RunStreamHandlers): () => void {
  const t = getToken();
  const url = `/api/runs/${enc(id)}/stream${t ? `?access_token=${enc(t)}` : ''}`;
  const es = new EventSource(url);
  let closed = false;

  const parse = <T,>(ev: MessageEvent): T | null => {
    try {
      return JSON.parse(ev.data as string) as T;
    } catch {
      return null;
    }
  };

  es.addEventListener('open', () => h.onConnectionChange?.(true));
  let probing = false;
  es.addEventListener('error', () => {
    if (closed) return;
    h.onConnectionChange?.(false);
    // EventSource hides the HTTP status. Probe with a normal request so a
    // 401 (token revoked / missing) reaches the auth gate.
    if (!probing) {
      probing = true;
      request<Run>('GET', `/api/runs/${enc(id)}`)
        .catch(() => undefined)
        .finally(() => {
          probing = false;
        });
    }
  });
  es.addEventListener('metric', (ev) => {
    const p = parse<MetricPoint>(ev as MessageEvent);
    if (p) h.onMetric?.(p);
  });
  es.addEventListener('status', (ev) => {
    const r = parse<Run>(ev as MessageEvent);
    if (r) h.onStatus?.(r);
  });
  es.addEventListener('stats', (ev) => {
    const s = parse<LiveStats>(ev as MessageEvent);
    if (s) h.onStats?.(s);
  });
  es.addEventListener('done', (ev) => {
    const r = parse<Run>(ev as MessageEvent);
    closed = true;
    es.close();
    if (r) h.onDone?.(r);
  });

  return () => {
    closed = true;
    es.close();
  };
}

export function errorText(e: unknown): { message: string; details: string[] } {
  if (e instanceof APIError) return { message: e.status === 403 ? forbiddenMessage(e.message) : e.message, details: e.details };
  if (e instanceof Error) return { message: e.message, details: [] };
  return { message: String(e), details: [] };
}
