// Quick test: a compact model for the common case (one HTTP endpoint, a
// ramp-up / hold / ramp-down profile, a few pass criteria and optional
// authentication) and conversion to and from the full ScenarioForm.
//
// formToQuick() also decides whether a scenario is representable in Quick
// mode; when it is not, it returns the reasons (shown as a tooltip on the
// disabled "Quick" switch). quickToForm(formToQuick(f)) must describe the same
// scenario as f for every representable f.
//
// Pure module (no React/DOM) so `node --test` can exercise it directly.
import {
  defaultForm,
  emptyAuth,
  emptyStep,
  emptyWS,
  parseDuration,
  OP_SYMBOL,
  type FormStep,
  type KV,
  type ScenarioForm,
} from './scenario.ts';

export const QUICK_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] as const;
export type QuickMethod = (typeof QUICK_METHODS)[number];
export type DurUnit = 's' | 'm' | 'h';
export const DUR_UNITS: DurUnit[] = ['s', 'm', 'h'];

/** A friendly duration: number + unit. value '' means "not set". */
export interface Dur {
  value: string;
  unit: DurUnit;
}

export type QuickAuthType = 'none' | 'bearer' | 'login';
/** csv: credentials come from a CSV file via {{vars}} in the body; fixed: one account. */
export type CredMode = 'csv' | 'fixed';

export interface QuickAuth {
  type: QuickAuthType;
  /** bearer */
  token: string;
  /** login */
  loginMethod: 'POST' | 'PUT';
  loginPath: string;
  credMode: CredMode;
  /** csv mode: JSON body with {{username}} / {{password}} placeholders. */
  bodyTemplate: string;
  /** fixed mode: body is {"<userField>": username, "<passField>": password}. */
  userField: string;
  passField: string;
  username: string;
  password: string;
  /** csv mode: data.file */
  csvFile: string;
  /** JSON path of the token in the login response. */
  tokenPath: string;
  scope: 'per_user' | 'shared';
  /** Re-login after this age; value '' = never. */
  refresh: Dur;
}

export interface QuickForm {
  name: string;
  /** Full request URL, e.g. https://api.example.com/v1/products?page=1 */
  url: string;
  method: QuickMethod;
  /** JSON body (non-GET). Content-Type: application/json is added automatically. */
  body: string;
  /** Headers sent with every request (target.headers, also used by the login request). */
  headers: KV[];
  /** vus = closed model (concurrent virtual users), rps = open model (arrivals per second). */
  mode: 'vus' | 'rps';
  target: string;
  rampUp: Dur;
  hold: Dur;
  rampDownOn: boolean;
  rampDown: Dur;
  checkStatus: boolean;
  status: string;
  checkP95: boolean;
  p95Ms: string;
  checkErrors: boolean;
  /** percent, e.g. "1" = 1% = error_rate 0.01 */
  errorPct: string;
  auth: QuickAuth;
  /** Step name kept from an existing scenario; '' = derived from method + path. */
  stepName: string;
}

export const TOKEN_PATH_PICKS = ['token', 'access_token', 'data.token', 'data.access_token'];

export function defaultQuickAuth(): QuickAuth {
  return {
    type: 'none',
    token: '',
    loginMethod: 'POST',
    loginPath: '/login',
    credMode: 'csv',
    bodyTemplate: '{"username":"{{username}}","password":"{{password}}"}',
    userField: 'username',
    passField: 'password',
    username: '',
    password: '',
    csvFile: 'users.csv',
    tokenPath: 'token',
    scope: 'per_user',
    refresh: { value: '', unit: 'm' },
  };
}

export function defaultQuick(): QuickForm {
  return {
    name: 'new-test',
    url: '',
    method: 'GET',
    body: '',
    headers: [],
    mode: 'vus',
    target: '10',
    rampUp: { value: '30', unit: 's' },
    hold: { value: '2', unit: 'm' },
    rampDownOn: false,
    rampDown: { value: '15', unit: 's' },
    checkStatus: true,
    status: '200',
    checkP95: true,
    p95Ms: '500',
    checkErrors: true,
    errorPct: '1',
    auth: defaultQuickAuth(),
    stepName: '',
  };
}

// ---------- small helpers ----------

/** Go duration string for a Dur ("" when unset/invalid). */
export function durToGo(d: Dur): string {
  const v = d.value.trim();
  if (v === '') return '';
  const n = Number(v);
  if (!Number.isFinite(n) || n < 0) return '';
  return `${n}${d.unit}`;
}

export function durSeconds(d: Dur): number {
  const g = durToGo(d);
  return g ? parseDuration(g) : NaN;
}

/** Picks the largest unit that represents the duration exactly. */
export function durFromGo(s: string): Dur | null {
  const sec = parseDuration(s);
  if (!Number.isFinite(sec) || sec < 0) return null;
  if (sec > 0 && sec % 3600 === 0) return { value: String(sec / 3600), unit: 'h' };
  if (sec > 0 && sec % 60 === 0) return { value: String(sec / 60), unit: 'm' };
  return { value: String(Number(sec.toPrecision(12))), unit: 's' };
}

/** {{var}} names used in a template, in order of first use. */
export function templateVars(s: string): string[] {
  const out: string[] = [];
  for (const m of s.matchAll(/\{\{\s*([\w.-]+)\s*\}\}/g)) if (!out.includes(m[1])) out.push(m[1]);
  return out;
}

/**
 * Splits a full URL into base_url (scheme://host[:port]) and path. When
 * preferBase (the scenario's current base_url, which may carry a path
 * prefix) is a prefix of url it is kept, so editing does not churn YAML.
 */
export function splitUrl(url: string, preferBase = ''): { base: string; path: string; ok: boolean } {
  const u = url.trim();
  const pb = preferBase.trim().replace(/\/+$/, '');
  if (pb && /^https?:\/\/[^/?#]+/i.test(pb) && u.startsWith(pb)) {
    const rest = u.slice(pb.length);
    if (rest === '' || rest.startsWith('/') || rest.startsWith('?')) {
      return { base: preferBase.trim(), path: rest === '' ? '/' : rest.startsWith('?') ? `/${rest}` : rest, ok: true };
    }
  }
  const m = /^(https?:\/\/[^/?#\s]+)(.*)$/i.exec(u);
  if (!m) return { base: u, path: '/', ok: false };
  let path = m[2];
  if (path === '') path = '/';
  else if (!path.startsWith('/')) path = `/${path}`;
  return { base: m[1], path, ok: true };
}

export function joinUrl(base: string, path: string): string {
  const b = base.trim().replace(/\/+$/, '');
  if (/^https?:\/\//i.test(path)) return path;
  if (!b && (path === '/' || path === '')) return '';
  return b + (path.startsWith('/') ? path : `/${path}`);
}

function autoStepName(method: string, path: string): string {
  const p = path.split('?')[0] || '/';
  return `${method} ${p}`;
}

function pctFromFraction(v: string): string {
  const n = Number(v);
  if (!Number.isFinite(n)) return v;
  return String(Number((n * 100).toPrecision(12)));
}
function fractionFromPct(v: string): string {
  const n = Number(v);
  if (!Number.isFinite(n)) return v;
  return String(Number((n / 100).toPrecision(12)));
}

function isContentTypeJson(kv: KV): boolean {
  return kv.key.trim().toLowerCase() === 'content-type' && /^application\/json\b/i.test(kv.value.trim());
}

// ---------- quick -> full form ----------

/**
 * Builds the full form for a Quick test. Settings Quick mode does not show
 * (timeout, TLS verification, max_vus) are carried over from base.
 */
export function quickToForm(q: QuickForm, base?: ScenarioForm | null): ScenarioForm {
  const f = defaultForm();
  const { base: baseUrl, path } = splitUrl(q.url, base?.target.base_url ?? '');
  f.name = q.name;
  f.target = {
    base_url: baseUrl,
    headers: q.headers.map((h) => ({ ...h })),
    timeout: base ? base.target.timeout : '',
    insecure_skip_verify: base ? base.target.insecure_skip_verify : false,
  };

  const T = q.target.trim();
  const stages = [
    { duration: durToGo(q.rampUp), target: T },
    { duration: durToGo(q.hold), target: T },
  ];
  if (q.rampDownOn) stages.push({ duration: durToGo(q.rampDown), target: '0' });
  f.load = { model: q.mode === 'vus' ? 'closed' : 'open', stages, max_vus: base ? base.load.max_vus : '' };

  const step: FormStep = {
    ...emptyStep(1),
    name: q.stepName.trim() || autoStepName(q.method, path),
    method: q.method,
    path,
    headers: [],
    body: '',
    assert: [],
  };
  const hasBody = q.method !== 'GET' && q.body.trim() !== '';
  if (hasBody) {
    step.body = q.body;
    step.headers = [{ key: 'Content-Type', value: 'application/json' }];
  }
  if (q.checkStatus && q.status.trim()) step.assert = [{ type: 'status', expr: '', op: 'eq', value: q.status.trim() }];
  f.steps = [step];

  f.thresholds = [];
  if (q.checkP95 && q.p95Ms.trim()) f.thresholds.push({ metric: 'latency.p95', op: 'lt', value: q.p95Ms.trim() });
  if (q.checkErrors && q.errorPct.trim())
    f.thresholds.push({ metric: 'error_rate', op: 'lt', value: fractionFromPct(q.errorPct.trim()) });

  f.auth = emptyAuth();
  f.data = { enabled: false, file: '', mode: 'sequential' };
  const a = q.auth;
  if (a.type === 'bearer') {
    f.auth.type = 'bearer';
    f.auth.token = a.token;
  } else if (a.type === 'login') {
    f.auth.type = 'login';
    f.auth.scope = a.scope;
    f.auth.refresh = durToGo(a.refresh);
    let lp = a.loginPath.trim();
    // A full URL on the request's host becomes a path on base_url.
    const b = baseUrl.replace(/\/+$/, '');
    if (b && lp.startsWith(b) && (lp.length === b.length || '/?'.includes(lp[b.length]))) lp = lp.slice(b.length) || '/';
    let body: string;
    if (a.credMode === 'fixed') {
      body = JSON.stringify({ [a.userField.trim() || 'username']: a.username, [a.passField.trim() || 'password']: a.password });
    } else {
      body = a.bodyTemplate;
      f.data = { enabled: true, file: a.csvFile.trim(), mode: 'sequential' };
    }
    f.auth.login = {
      name: '',
      type: 'http',
      method: a.loginMethod,
      path: lp,
      headers: [{ key: 'Content-Type', value: 'application/json' }],
      body,
      think_time: '',
      extract: [{ var: 'token', from: 'json', expr: a.tokenPath.trim() || 'token' }],
      assert: [],
      ws: emptyWS(),
    };
  }
  return f;
}

// ---------- full form -> quick (+ representability) ----------

export interface QuickOutcome {
  quick: QuickForm | null;
  /** Why the scenario cannot be edited in Quick mode (empty when it can). */
  reasons: string[];
}

export function formToQuick(f: ScenarioForm): QuickOutcome {
  const reasons: string[] = [];
  const q = defaultQuick();
  q.name = f.name;
  q.headers = f.target.headers.map((h) => ({ ...h }));

  // ----- steps -----
  if (f.steps.length !== 1) reasons.push(`Has ${f.steps.length} steps (Quick mode edits exactly one request)`);
  const s = f.steps[0];
  if (s) {
    if (s.type === 'ws') reasons.push('Uses a WebSocket step');
    else {
      if (!(QUICK_METHODS as readonly string[]).includes(s.method)) reasons.push(`Uses method ${s.method}`);
      else q.method = s.method as QuickMethod;
      if (s.extract.length) reasons.push('The step extracts variables');
      if (s.think_time.trim()) reasons.push('The step has a think time');
      const extraHeaders = s.headers.filter((h) => !(isContentTypeJson(h) && s.body.trim() !== ''));
      if (extraHeaders.length) reasons.push('The step sets its own headers');
      if (!/^\//.test(s.path)) reasons.push('The step path is not a plain /path');
      if (s.body.trim() && s.method === 'GET') reasons.push('GET request with a body');
      q.body = s.body;
      q.url = joinUrl(f.target.base_url, s.path || '/');
      q.stepName = s.name === autoStepName(s.method, s.path) ? '' : s.name;
      if (s.assert.length === 0) q.checkStatus = false;
      else if (s.assert.length === 1 && s.assert[0].type === 'status' && (s.assert[0].op || 'eq') === 'eq') {
        q.checkStatus = true;
        q.status = s.assert[0].value;
      } else reasons.push('Has assertions other than one expected status');
    }
  }

  // ----- load -----
  q.mode = f.load.model === 'closed' ? 'vus' : 'rps';
  const st = f.load.stages;
  const shapeOk =
    (st.length === 2 || st.length === 3) &&
    st[0].target.trim() !== '' &&
    st[0].target.trim() === st[1].target.trim() &&
    Number(st[0].target) > 0 &&
    (st.length === 2 || Number(st[2].target) === 0) &&
    st.every((x) => Number.isFinite(parseDuration(x.duration)) && parseDuration(x.duration) > 0);
  if (!shapeOk) reasons.push('Load stages are not a ramp-up → hold → optional ramp-down to 0');
  else {
    q.target = st[0].target.trim();
    q.rampUp = durFromGo(st[0].duration) ?? q.rampUp;
    q.hold = durFromGo(st[1].duration) ?? q.hold;
    q.rampDownOn = st.length === 3;
    if (st[2]) q.rampDown = durFromGo(st[2].duration) ?? q.rampDown;
  }

  // ----- thresholds -----
  q.checkP95 = false;
  q.checkErrors = false;
  const seen = new Set<string>();
  for (const t of f.thresholds) {
    if (seen.has(t.metric)) {
      reasons.push(`Threshold ${t.metric} appears more than once`);
      continue;
    }
    seen.add(t.metric);
    if (t.metric === 'latency.p95' && t.op === 'lt') {
      q.checkP95 = true;
      q.p95Ms = t.value;
    } else if (t.metric === 'error_rate' && t.op === 'lt') {
      q.checkErrors = true;
      q.errorPct = pctFromFraction(t.value);
    } else reasons.push(`Threshold ${t.metric} ${OP_SYMBOL[t.op] ?? t.op} ${t.value} (only p95 < and error rate < fit)`);
  }

  // ----- auth + data -----
  const a = f.auth;
  let dataUsed = false;
  if (a.type === 'basic') reasons.push('Uses basic authentication');
  else if (a.type !== 'none') {
    if (a.header.trim() && a.header.trim().toLowerCase() !== 'authorization') reasons.push(`Auth header is ${a.header}`);
    if (!a.inject) reasons.push('Auth header injection is turned off');
  }
  if (a.type === 'bearer') {
    if (a.value.trim()) reasons.push('Custom auth header value');
    q.auth.type = 'bearer';
    q.auth.token = a.token;
  } else if (a.type === 'login') {
    q.auth.type = 'login';
    if (a.value.trim() && a.value.trim() !== 'Bearer {{token}}') reasons.push('Custom auth header value');
    const l = a.login;
    if (l.method !== 'POST' && l.method !== 'PUT') reasons.push(`Login uses method ${l.method}`);
    else q.auth.loginMethod = l.method;
    q.auth.loginPath = l.path;
    q.auth.scope = a.scope === 'shared' ? 'shared' : 'per_user';
    if (a.refresh.trim()) {
      const d = durFromGo(a.refresh);
      if (d) q.auth.refresh = d;
      else reasons.push('Login refresh is not a valid duration');
    }
    if (l.extract.length !== 1 || l.extract[0].var !== 'token' || l.extract[0].from !== 'json')
      reasons.push('Login extracts something other than one JSON "token"');
    else q.auth.tokenPath = l.extract[0].expr;
    if (l.assert.length) reasons.push('Login has assertions');
    if (l.think_time.trim()) reasons.push('Login has a think time');
    if (l.headers.some((h) => !isContentTypeJson(h)) || l.headers.length !== 1)
      reasons.push('Login headers are not just Content-Type: application/json');
    const vars = templateVars(l.body);
    if (vars.length) {
      q.auth.credMode = 'csv';
      q.auth.bodyTemplate = l.body;
      if (!f.data.enabled) reasons.push('Login body uses {{variables}} without a CSV data file');
      else {
        dataUsed = true;
        q.auth.csvFile = f.data.file;
      }
    } else {
      const fixed = fixedCreds(l.body);
      if (fixed) {
        q.auth.credMode = 'fixed';
        Object.assign(q.auth, fixed);
      } else if (l.body.trim()) {
        // Some other literal body: keep it as a template (no CSV needed).
        q.auth.credMode = 'csv';
        q.auth.bodyTemplate = l.body;
        reasons.push('Login body is not a username/password JSON object');
      } else reasons.push('Login request has no body');
    }
  }
  if (f.data.enabled && !dataUsed) reasons.push('Uses a CSV data file outside the login request');
  if (f.data.enabled && f.data.mode !== 'sequential') reasons.push(`Data mode is ${f.data.mode} (Quick mode uses sequential)`);

  return reasons.length ? { quick: null, reasons } : { quick: q, reasons };
}

/** {"user": "...", "pass": "..."} with exactly two string fields and no placeholders. */
function fixedCreds(body: string): Pick<QuickAuth, 'userField' | 'passField' | 'username' | 'password'> | null {
  let v: unknown;
  try {
    v = JSON.parse(body);
  } catch {
    return null;
  }
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return null;
  const e = Object.entries(v as Record<string, unknown>);
  if (e.length !== 2 || e.some(([, x]) => typeof x !== 'string')) return null;
  return { userField: e[0][0], passField: e[1][0], username: e[0][1] as string, password: e[1][1] as string };
}

/** Problems the Quick form shows inline (the backend validates the rest). */
export function quickIssues(q: QuickForm): Partial<Record<'url' | 'target' | 'body' | 'loginBody' | 'csv' | 'loginPath', string>> {
  const out: Partial<Record<'url' | 'target' | 'body' | 'loginBody' | 'csv' | 'loginPath', string>> = {};
  if (q.url.trim() && !splitUrl(q.url).ok) out.url = 'Enter a full URL starting with http:// or https://';
  const t = Number(q.target);
  if (!(Number.isFinite(t) && t > 0 && Number.isInteger(t))) out.target = 'Enter a whole number greater than 0';
  const jsonProblem = (s: string) => {
    if (!s.trim()) return undefined;
    try {
      // Placeholders may sit outside quotes ({"n": {{n}}}); check JSON shape with them neutralised.
      JSON.parse(s.replace(/\{\{\s*[\w.-]+\s*\}\}/g, '0'));
      return undefined;
    } catch {
      return 'Not valid JSON';
    }
  };
  if (q.method !== 'GET') out.body = jsonProblem(q.body);
  if (q.auth.type === 'login') {
    if (q.auth.credMode === 'csv') {
      out.loginBody = jsonProblem(q.auth.bodyTemplate);
      if (templateVars(q.auth.bodyTemplate).length && !q.auth.csvFile.trim()) out.csv = 'A CSV file name is required';
    }
    if (!q.auth.loginPath.trim()) out.loginPath = 'Enter the login path, e.g. /api/login';
  }
  for (const k of Object.keys(out) as (keyof typeof out)[]) if (!out[k]) delete out[k];
  return out;
}
