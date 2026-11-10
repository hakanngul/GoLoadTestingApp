import type {
  APIErrorBody,
  ErrorSample,
  Health,
  MetricPoint,
  Run,
  StartRunRequest,
  Test,
  ValidateResult,
  Worker,
} from './types';

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
  validate: (yaml: string) => request<ValidateResult>('POST', '/api/validate', { yaml }),

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

  listWorkers: () => request<Worker[] | null>('GET', '/api/workers').then((r) => r ?? []),
};

export interface RunStreamHandlers {
  onMetric?: (p: MetricPoint) => void;
  onStatus?: (r: Run) => void;
  onDone?: (r: Run) => void;
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
  if (e instanceof APIError) return { message: e.message, details: e.details };
  if (e instanceof Error) return { message: e.message, details: [] };
  return { message: String(e), details: [] };
}
