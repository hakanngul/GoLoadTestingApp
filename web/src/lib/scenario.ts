// Form model for the YAML scenario format (internal/scenario/types.go) and
// conversion in both directions using the `yaml` package.
import { parse, stringify } from 'yaml';

export type LoadModel = 'open' | 'closed';
export type DataMode = 'sequential' | 'random' | 'unique';
export type ExtractSource = 'json' | 'header' | 'regex';
export type AssertType = 'status' | 'json' | 'duration' | 'body_contains';
export type StepType = 'http' | 'ws';

export const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'] as const;
export const DATA_MODES: DataMode[] = ['sequential', 'random', 'unique'];
export const EXTRACT_SOURCES: ExtractSource[] = ['json', 'header', 'regex'];
export const ASSERT_TYPES: AssertType[] = ['status', 'json', 'duration', 'body_contains'];
export const ASSERT_OPS: Record<AssertType, string[]> = {
  status: ['eq', 'ne', 'lt', 'lte', 'gt', 'gte'],
  duration: ['lt', 'lte', 'gt', 'gte'],
  json: ['eq', 'ne', 'lt', 'lte', 'gt', 'gte', 'contains'],
  body_contains: ['contains', 'eq', 'ne'],
};
export const THRESHOLD_METRICS: { value: string; label: string; unit: string }[] = [
  { value: 'latency.p50', label: 'latency.p50', unit: 'ms' },
  { value: 'latency.p90', label: 'latency.p90', unit: 'ms' },
  { value: 'latency.p95', label: 'latency.p95', unit: 'ms' },
  { value: 'latency.p99', label: 'latency.p99', unit: 'ms' },
  { value: 'latency.mean', label: 'latency.mean', unit: 'ms' },
  { value: 'latency.max', label: 'latency.max', unit: 'ms' },
  { value: 'error_rate', label: 'error_rate', unit: 'fraction (0.01 = 1%)' },
  { value: 'rps', label: 'rps', unit: 'req/s' },
];
export const THRESHOLD_OPS = ['lt', 'lte', 'gt', 'gte'] as const;
export const OP_SYMBOL: Record<string, string> = {
  lt: '<',
  lte: '≤',
  gt: '>',
  gte: '≥',
  eq: '=',
  ne: '≠',
  contains: '∋',
};

export interface KV {
  key: string;
  value: string;
}

export interface FormStage {
  duration: string;
  target: string;
}

export interface FormExtract {
  var: string;
  from: ExtractSource;
  expr: string;
}

export interface FormAssert {
  type: AssertType;
  expr: string;
  op: string;
  value: string;
}

/** One ws.messages entry: send a text message and/or wait for one containing expect. */
export interface FormWSMessage {
  send: string;
  expect: string;
  timeout: string;
}

/** The ws block of a WebSocket step (type: ws). */
export interface FormWS {
  hold: string;
  ping_every: string;
  ping_message: string;
  messages: FormWSMessage[];
}

export interface FormStep {
  name: string;
  /** 'http' (default, omitted from YAML) or 'ws'. */
  type: StepType;
  method: string;
  path: string;
  headers: KV[];
  body: string;
  think_time: string;
  extract: FormExtract[];
  assert: FormAssert[];
  /** WebSocket settings; only used (and emitted) when type is 'ws'. */
  ws: FormWS;
}

export function emptyWS(): FormWS {
  return { hold: '', ping_every: '', ping_message: '', messages: [] };
}

export interface FormThreshold {
  metric: string;
  op: string;
  value: string;
}

export interface ScenarioForm {
  name: string;
  target: { base_url: string; headers: KV[]; timeout: string; insecure_skip_verify: boolean };
  load: { model: LoadModel; stages: FormStage[]; max_vus: string };
  data: { enabled: boolean; file: string; mode: DataMode };
  steps: FormStep[];
  thresholds: FormThreshold[];
}

export function emptyStep(i = 1): FormStep {
  return {
    name: `step ${i}`,
    method: 'GET',
    path: '/',
    headers: [],
    body: '',
    think_time: '',
    extract: [],
    assert: [{ type: 'status', expr: '', op: 'eq', value: '200' }],
    type: 'http',
    ws: emptyWS(),
  };
}

export function defaultForm(): ScenarioForm {
  return {
    name: 'new-test',
    target: { base_url: 'https://api.example.test', headers: [], timeout: '10s', insecure_skip_verify: false },
    load: {
      model: 'open',
      stages: [
        { duration: '30s', target: '50' },
        { duration: '2m', target: '50' },
        { duration: '15s', target: '0' },
      ],
      max_vus: '',
    },
    data: { enabled: false, file: '', mode: 'sequential' },
    steps: [emptyStep(1)],
    thresholds: [
      { metric: 'latency.p95', op: 'lt', value: '500' },
      { metric: 'error_rate', op: 'lt', value: '0.01' },
    ],
  };
}

// ---------- helpers ----------

type Obj = Record<string, unknown>;

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}
function str(v: unknown): string {
  if (v === undefined || v === null) return '';
  return String(v);
}
function arr(v: unknown): unknown[] {
  return Array.isArray(v) ? v : [];
}
function kvFromMap(v: unknown): KV[] {
  if (!isObj(v)) return [];
  return Object.entries(v).map(([key, value]) => ({ key, value: str(value) }));
}
function kvToMap(kv: KV[]): Record<string, string> | undefined {
  const rows = kv.filter((r) => r.key.trim() !== '' || r.value.trim() !== '');
  if (rows.length === 0) return undefined;
  const out: Record<string, string> = {};
  for (const r of rows) out[r.key] = r.value;
  return out;
}

/** Parse a Go duration ("1m30s", "250ms", "2h") into seconds. NaN if invalid. */
export function parseDuration(s: string): number {
  const v = s.trim();
  if (!v) return NaN;
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  let total = 0;
  let consumed = 0;
  let m: RegExpExecArray | null;
  const units: Record<string, number> = { ns: 1e-9, us: 1e-6, µs: 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
  while ((m = re.exec(v)) !== null) {
    if (m.index !== consumed) return NaN;
    total += parseFloat(m[1]) * units[m[2]];
    consumed = m.index + m[0].length;
  }
  if (consumed !== v.length || consumed === 0) return v === '0' ? 0 : NaN;
  return total;
}

// ---------- YAML -> form ----------

export interface ParseOutcome {
  form: ScenarioForm | null;
  error: string | null;
}

export function yamlToForm(text: string): ParseOutcome {
  let doc: unknown;
  try {
    doc = parse(text);
  } catch (e) {
    return { form: null, error: (e as Error).message };
  }
  if (doc === null || doc === undefined) doc = {};
  if (!isObj(doc)) return { form: null, error: 'Top level of the scenario must be a mapping' };

  const target = isObj(doc.target) ? doc.target : {};
  const load = isObj(doc.load) ? doc.load : {};
  const data = isObj(doc.data) ? doc.data : null;

  const form: ScenarioForm = {
    name: str(doc.name),
    target: {
      base_url: str(target.base_url),
      headers: kvFromMap(target.headers),
      timeout: str(target.timeout),
      insecure_skip_verify: target.insecure_skip_verify === true,
    },
    load: {
      model: load.model === 'closed' ? 'closed' : 'open',
      stages: arr(load.stages).map((s) => {
        const o = isObj(s) ? s : {};
        return { duration: str(o.duration), target: str(o.target) };
      }),
      max_vus: str(load.max_vus),
    },
    data: {
      enabled: data !== null,
      file: data ? str(data.file) : '',
      mode: data && (DATA_MODES as string[]).includes(str(data.mode)) ? (str(data.mode) as DataMode) : 'sequential',
    },
    steps: arr(doc.steps).map((s, i) => {
      const o = isObj(s) ? s : {};
      const type: StepType = str(o.type).trim().toLowerCase() === 'ws' ? 'ws' : 'http';
      const ws = isObj(o.ws) ? o.ws : {};
      return {
        name: str(o.name) || `step ${i + 1}`,
        type,
        ws: {
          hold: str(ws.hold),
          ping_every: str(ws.ping_every),
          ping_message: str(ws.ping_message),
          messages: arr(ws.messages).map((m) => {
            const x = isObj(m) ? m : {};
            return { send: str(x.send), expect: str(x.expect), timeout: str(x.timeout) };
          }),
        },
        method: type === 'ws' ? '' : (str(o.method) || 'GET').toUpperCase(),
        path: str(o.path),
        headers: kvFromMap(o.headers),
        body: str(o.body),
        think_time: str(o.think_time),
        extract: arr(o.extract).map((e) => {
          const x = isObj(e) ? e : {};
          const from = str(x.from) as ExtractSource;
          return { var: str(x.var), from: EXTRACT_SOURCES.includes(from) ? from : 'json', expr: str(x.expr) };
        }),
        assert: arr(o.assert).map((a) => {
          const x = isObj(a) ? a : {};
          const type = str(x.type) as AssertType;
          return {
            type: ASSERT_TYPES.includes(type) ? type : 'status',
            expr: str(x.expr),
            op: str(x.op) || 'eq',
            value: str(x.value),
          };
        }),
      };
    }),
    thresholds: arr(doc.thresholds).map((t) => {
      const o = isObj(t) ? t : {};
      return { metric: str(o.metric), op: str(o.op) || 'lt', value: str(o.value) };
    }),
  };
  return { form, error: null };
}

// ---------- form -> YAML ----------

function num(s: string): number | string {
  const t = s.trim();
  if (t === '') return t;
  const n = Number(t);
  return Number.isFinite(n) ? n : t;
}

export function formToObject(f: ScenarioForm): Obj {
  const out: Obj = { name: f.name };

  const target: Obj = { base_url: f.target.base_url };
  if (f.target.timeout.trim()) target.timeout = f.target.timeout.trim();
  const th = kvToMap(f.target.headers);
  if (th) target.headers = th;
  if (f.target.insecure_skip_verify) target.insecure_skip_verify = true;
  out.target = target;

  const load: Obj = {
    model: f.load.model,
    stages: f.load.stages.map((s) => ({ duration: s.duration.trim(), target: num(s.target) })),
  };
  if (f.load.max_vus.trim()) load.max_vus = num(f.load.max_vus);
  out.load = load;

  if (f.data.enabled) {
    out.data = { file: f.data.file, mode: f.data.mode };
  }

  out.steps = f.steps.map((s) => {
    if (s.type === 'ws') return wsStepToObject(s);
    const o: Obj = { name: s.name, method: s.method, path: s.path };
    const h = kvToMap(s.headers);
    if (h) o.headers = h;
    if (s.body.trim()) o.body = s.body.endsWith('\n') || !s.body.includes('\n') ? s.body : `${s.body}\n`;
    if (s.extract.length) o.extract = s.extract.map((e) => ({ var: e.var, from: e.from, expr: e.expr }));
    if (s.assert.length)
      o.assert = s.assert.map((a) => {
        const x: Obj = { type: a.type };
        if (a.type === 'json' || a.expr.trim()) x.expr = a.expr;
        if (a.op && a.op !== 'eq') x.op = a.op;
        x.value = a.value;
        return x;
      });
    if (s.think_time.trim()) o.think_time = s.think_time.trim();
    return o;
  });

  if (f.thresholds.length) {
    out.thresholds = f.thresholds.map((t) => ({ metric: t.metric, op: t.op, value: num(t.value) }));
  }
  return out;
}

/** A ws step: no method, body or assert; a ws block instead. */
function wsStepToObject(s: FormStep): Obj {
  const o: Obj = { name: s.name, type: 'ws', path: s.path };
  const h = kvToMap(s.headers);
  if (h) o.headers = h;
  const ws: Obj = {};
  if (s.ws.messages.length)
    ws.messages = s.ws.messages.map((m) => {
      const x: Obj = {};
      if (m.send) x.send = m.send;
      if (m.expect) x.expect = m.expect;
      if (m.timeout.trim()) x.timeout = m.timeout.trim();
      return x;
    });
  if (s.ws.hold.trim()) ws.hold = s.ws.hold.trim();
  if (s.ws.ping_every.trim()) ws.ping_every = s.ws.ping_every.trim();
  if (s.ws.ping_message) ws.ping_message = s.ws.ping_message;
  o.ws = ws;
  if (s.extract.length) o.extract = s.extract.map((e) => ({ var: e.var, from: e.from, expr: e.expr }));
  if (s.think_time.trim()) o.think_time = s.think_time.trim();
  return o;
}

export function formToYaml(f: ScenarioForm): string {
  return stringify(formToObject(f), { lineWidth: 0, defaultStringType: 'PLAIN' });
}

// ---------- profile ----------

export interface ProfilePoint {
  t: number;
  target: number;
}

/** Converts stages into a piecewise-linear profile starting at (0, 0). */
export function stagesToProfile(stages: FormStage[]): { points: ProfilePoint[]; total: number; peak: number } {
  const points: ProfilePoint[] = [{ t: 0, target: 0 }];
  let t = 0;
  let peak = 0;
  for (const s of stages) {
    const d = parseDuration(s.duration);
    const target = Number(s.target);
    if (!Number.isFinite(d) || d <= 0 || !Number.isFinite(target)) continue;
    t += d;
    peak = Math.max(peak, target);
    points.push({ t, target });
  }
  return { points, total: t, peak };
}

/** Lightweight summary of a scenario YAML (for run dialog / headers). */
export function summarizeYaml(text: string): {
  model: LoadModel;
  total: number;
  peak: number;
  steps: number;
  stages: FormStage[];
  maxVus: string;
} | null {
  const { form } = yamlToForm(text);
  if (!form) return null;
  const p = stagesToProfile(form.load.stages);
  return {
    model: form.load.model,
    total: p.total,
    peak: p.peak,
    steps: form.steps.length,
    stages: form.load.stages,
    maxVus: form.load.max_vus,
  };
}

export function unitFor(model: LoadModel): string {
  return model === 'closed' ? 'VUs' : 'it/s';
}
