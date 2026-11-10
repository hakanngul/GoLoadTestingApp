// Mock GoLoad backend for UI development and screenshots.
// Plain Node, no dependencies. Implements every route from internal/api/doc.go
// with in-memory data. Usage: node mock/server.mjs [--port 8080]
//
// Environment:
//   MOCK_TOKEN=secret     require `Authorization: Bearer secret` on /api/* except
//                         /api/health (SSE also accepts ?access_token=), like `serve -token`
//   MOCK_PROVISIONER=0    no cloud provisioner: asking for more workers than are idle
//                         returns 409 "not enough idle workers" (default: provisioner on,
//                         such runs start `queued` while spot instances "launch")
//   MOCK_QUEUE_S=150      how long the queued run seeded at boot stays queued
import { createServer } from 'node:http';
import { readFileSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const examplesDir = join(here, '..', '..', 'examples');
const portArg = process.argv.indexOf('--port');
const PORT = Number(portArg > 0 ? process.argv[portArg + 1] : process.env.PORT || 8080);
const TOKEN = process.env.MOCK_TOKEN || '';
const PROVISIONER = process.env.MOCK_PROVISIONER !== '0';
const QUEUE_S = Number(process.env.MOCK_QUEUE_S || 150);
const LAUNCH_S = 8; // spot launch time for runs started from the UI

// ---------------------------------------------------------------- utils
let seed = 1337;
function rand() {
  // mulberry32
  seed |= 0;
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
}
const noise = (amp) => 1 + (rand() * 2 - 1) * amp;
const hex = (n) => Array.from({ length: n }, () => Math.floor(rand() * 16).toString(16)).join('');
const iso = (ms) => new Date(ms).toISOString();
const round = (v, d = 2) => Math.round(v * 10 ** d) / 10 ** d;

function parseDuration(s) {
  const re = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
  const u = { ms: 0.001, s: 1, m: 60, h: 3600 };
  let total = 0;
  let m;
  let matched = false;
  while ((m = re.exec(String(s))) !== null) {
    total += parseFloat(m[1]) * u[m[2]];
    matched = true;
  }
  return matched ? total : NaN;
}

// Minimal regex-based scenario extraction (good enough for the mock).
function scenarioMeta(yaml) {
  const clean = yaml
    .split('\n')
    .map((l) => l.replace(/\s+#.*$/, '').replace(/^#.*$/, ''))
    .join('\n');
  const name = (clean.match(/^name:\s*(.+)$/m) || [])[1]?.trim() ?? '';
  const model = /^\s+model:\s*closed/m.test(clean) ? 'closed' : 'open';
  const stages = [];
  const stRe = /-\s*duration:\s*"?([^\s"]+)"?\s*\n\s*target:\s*(\d+)/g;
  let m;
  while ((m = stRe.exec(clean)) !== null) stages.push({ d: parseDuration(m[1]), target: Number(m[2]) });
  const stepsBlock = clean.split(/^steps:\s*$/m)[1]?.split(/^thresholds:\s*$/m)[0] ?? '';
  const steps = [...stepsBlock.matchAll(/^ {2}- name:\s*(.+)$/gm)].map((x) => x[1].trim());
  if (steps.length === 0) {
    const n = (stepsBlock.match(/^ {2}- /gm) || []).length;
    for (let i = 0; i < n; i++) steps.push(`step ${i + 1}`);
  }
  const thBlock = clean.split(/^thresholds:\s*$/m)[1] ?? '';
  const thresholds = [...thBlock.matchAll(/-\s*metric:\s*(\S+)\s*\n\s*op:\s*(\w+)\s*\n\s*value:\s*([\d.]+)/g)].map(
    (x) => ({ metric: x[1], op: x[2], value: Number(x[3]) }),
  );
  const total = stages.reduce((a, s) => a + (Number.isFinite(s.d) ? s.d : 0), 0);
  const maxVus = Number((clean.match(/max_vus:\s*(\d+)/) || [])[1] || 1000);
  return { name, model, stages, steps, thresholds, total, maxVus };
}

function targetAt(stages, t) {
  let start = 0;
  let prev = 0;
  for (const s of stages) {
    if (t <= start + s.d) return prev + ((s.target - prev) * (t - start)) / s.d;
    start += s.d;
    prev = s.target;
  }
  return prev;
}

// ---------------------------------------------------------------- data
const now = Date.now();
const tests = new Map();
const runs = new Map();
const metrics = new Map(); // runId -> MetricPoint[]
const errorsByRun = new Map();
const errorGroupsByRun = new Map(); // runId -> ErrorGroup[] (finished runs)
const fileContents = new Map(); // `${testId}/${name}` -> size

function readExample(name) {
  const p = join(examplesDir, name);
  return existsSync(p) ? readFileSync(p, 'utf8') : '';
}

const soakYaml = `# Catalog soak: steady 300 rps against the read path for capacity checks.
name: catalog-soak
target:
  base_url: https://catalog.internal.example.test
  timeout: 5s
  headers:
    Accept: application/json
load:
  model: open
  stages:
    - duration: 30s
      target: 300
    - duration: 2m
      target: 300
    - duration: 30s
      target: 0
  max_vus: 1500
steps:
  - name: list
    method: GET
    path: /v2/products?page={{page}}
    assert:
      - type: status
        value: "200"
  - name: detail
    method: GET
    path: /v2/products/{{id}}
    assert:
      - type: status
        value: "200"
      - type: duration
        op: lt
        value: 400ms
  - name: search
    method: GET
    path: /v2/search?q={{term}}
    assert:
      - type: status
        value: "200"
  - name: categories
    method: GET
    path: /v2/categories
    assert:
      - type: status
        value: "200"
thresholds:
  - metric: latency.p95
    op: lt
    value: 250
  - metric: latency.p99
    op: lt
    value: 600
  - metric: error_rate
    op: lt
    value: 0.01
  - metric: rps
    op: gte
    value: 220
`;

function addTest(id, yaml, createdAgoH, files = []) {
  const meta = scenarioMeta(yaml);
  tests.set(id, {
    id,
    name: meta.name,
    yaml,
    files,
    created_at: iso(now - createdAgoH * 3600e3),
    updated_at: iso(now - createdAgoH * 1800e3),
  });
}

const usersCsv = readExample('users.csv');
addTest('t_9c1e44a0', readExample('smoke.yaml') || soakYaml.replace('catalog-soak', 'smoke'), 240);
addTest('t_4b7f02d3', readExample('login_flow.yaml') || soakYaml.replace('catalog-soak', 'login-flow'), 200, [
  { name: 'users.csv', size: usersCsv.length || 1024 },
]);
addTest('t_e81a6c57', readExample('spike.yaml') || soakYaml.replace('catalog-soak', 'spike'), 150);
addTest('t_27d5b9f8', soakYaml, 96);

// A Quick-mode test (one request, ramp/hold/ramp-down) that logs in once per user.
const ordersYaml = `name: orders-api
target:
  base_url: https://shop.example.test
  headers:
    Accept: application/json
load:
  model: closed
  stages:
    - duration: 30s
      target: 25
    - duration: 2m
      target: 25
    - duration: 15s
      target: 0
data:
  file: users.csv
  mode: sequential
auth:
  type: login
  scope: per_user
  refresh: 10m
  login:
    method: POST
    path: /api/login
    headers:
      Content-Type: application/json
    body: '{"username":"{{username}}","password":"{{password}}"}'
    extract:
      - var: token
        from: json
        expr: data.access_token
steps:
  - name: GET /v1/orders
    method: GET
    path: /v1/orders?limit=20
    assert:
      - type: status
        value: "200"
thresholds:
  - metric: latency.p95
    op: lt
    value: 400
  - metric: error_rate
    op: lt
    value: 0.01
`;
addTest('t_5a0c3e19', ordersYaml, 48, [{ name: 'users.csv', size: usersCsv.length || 1024 }]);

// Performance model per test: base latencies + capacity (rps beyond which latency/errors climb).
const perf = {
  t_9c1e44a0: { p50: 38, p95: 92, p99: 160, capacity: 400, err: 0.0005, stepFactor: [0.6, 1.4] },
  t_4b7f02d3: { p50: 140, p95: 380, p99: 690, capacity: 60, err: 0.003, stepFactor: [1.5, 0.5] },
  t_e81a6c57: { p50: 55, p95: 130, p99: 240, capacity: 580, err: 0.001, stepFactor: [1.2, 0.8] },
  t_27d5b9f8: { p50: 48, p95: 118, p99: 205, capacity: 520, err: 0.0015, stepFactor: [1.25, 0.75, 1.7, 0.45] },
  t_5a0c3e19: { p50: 64, p95: 170, p99: 310, capacity: 300, err: 0.002, stepFactor: [1] },
};
function perfFor(testId) {
  return perf[testId] || { p50: 50, p95: 120, p99: 220, capacity: 500, err: 0.002, stepFactor: [1] };
}

function genPoint(run, meta, t, opts = {}) {
  const p = perfFor(run.test_id);
  const target = targetAt(meta.stages, t);
  const steps = Math.max(1, meta.steps.length);
  const degrade = opts.degrade ?? 1;
  const cap = p.capacity / degrade;
  let rps;
  if (meta.model === 'closed') rps = target * steps * 0.42 * noise(0.06);
  else rps = target * noise(0.035);
  const load = rps / cap;
  const factor = (1 + Math.pow(Math.max(0, load), 4) * 1.6) * degrade;
  let errRate = p.err * noise(0.8) * (opts.errMult ?? 1);
  if (load > 1) errRate += Math.min(0.6, (load - 1) * 0.9);
  if (opts.errBurst && t >= opts.errBurst[0] && t <= opts.errBurst[1]) errRate += 0.18 * noise(0.3);
  errRate = Math.max(0, Math.min(1, errRate));
  const requests = Math.max(0, Math.round(rps));
  const errors = Math.round(requests * errRate);
  const lm = opts.latMult ?? 1; // release-to-release latency drift
  const p50 = p.p50 * factor * lm * noise(0.06);
  const p95 = Math.max(p50 * 1.6, p.p95 * factor * lm * noise(0.08));
  const p99 = Math.max(p95 * 1.2, p.p99 * factor * lm * noise(0.12));
  const active = Math.round(meta.model === 'closed' ? target : (rps * p.p50 * factor) / 1000 + rand() * 3);
  const dropped = meta.model === 'open' && active > meta.maxVus ? Math.round((active - meta.maxVus) * 0.8) : 0;
  return {
    t: iso(Date.parse(run.started_at) + t * 1000),
    elapsed: t,
    target: round(target, 2),
    active: Math.min(active, meta.model === 'open' ? meta.maxVus : active),
    requests,
    errors,
    dropped,
    rps: round(requests, 1),
    error_rate: requests ? round(errors / requests, 5) : 0,
    mean_ms: round(p50 * 1.15, 2),
    p50_ms: round(p50, 2),
    p95_ms: round(p95, 2),
    p99_ms: round(p99, 2),
    max_ms: round(p99 * (1.3 + rand() * 0.8), 2),
    cpu: round(Math.min(0.98, 0.06 + rps / 1400 + rand() * 0.04), 3),
    workers: run.workers || 1,
  };
}

function summarize(points, meta, testId) {
  const p = perfFor(testId);
  const sumBy = (k) => points.reduce((a, x) => a + x[k], 0);
  const requests = sumBy('requests');
  const errors = sumBy('errors');
  const dropped = sumBy('dropped');
  const dur = points.length || 1;
  const wavg = (k) => (requests ? points.reduce((a, x) => a + x[k] * x.requests, 0) / requests : 0);
  const p95 = wavg('p95_ms');
  const s = {
    requests,
    errors,
    dropped,
    error_rate: requests ? round(errors / requests, 5) : 0,
    rps: round(requests / dur, 2),
    mean_ms: round(wavg('mean_ms'), 2),
    p50_ms: round(wavg('p50_ms'), 2),
    p90_ms: round(wavg('p50_ms') + (p95 - wavg('p50_ms')) * 0.78, 2),
    p95_ms: round(p95 * 1.04, 2),
    p99_ms: round(wavg('p99_ms') * 1.08, 2),
    max_ms: round(Math.max(0, ...points.map((x) => x.max_ms)), 2),
  };
  const by = {};
  const n = Math.max(1, meta.steps.length);
  meta.steps.forEach((name, i) => {
    const f = p.stepFactor[i] ?? 1;
    const share = Math.round(requests / n);
    const errShare = Math.round(errors * (i === 0 ? 0.6 : 0.4 / Math.max(1, n - 1)));
    by[name] = {
      requests: share,
      errors: Math.min(share, n === 1 ? errors : errShare),
      dropped: Math.round(dropped / n),
      error_rate: share ? round(Math.min(share, n === 1 ? errors : errShare) / share, 5) : 0,
      rps: round(share / dur, 2),
      mean_ms: round(s.mean_ms * f, 2),
      p50_ms: round(s.p50_ms * f, 2),
      p90_ms: round(s.p90_ms * f, 2),
      p95_ms: round(s.p95_ms * f, 2),
      p99_ms: round(s.p99_ms * f, 2),
      max_ms: round(s.max_ms * f * 0.9, 2),
    };
  });
  s.by_step = by;
  return s;
}

function evalThresholds(meta, s) {
  const get = {
    'latency.p50': s.p50_ms,
    'latency.p90': s.p90_ms,
    'latency.p95': s.p95_ms,
    'latency.p99': s.p99_ms,
    'latency.mean': s.mean_ms,
    'latency.max': s.max_ms,
    error_rate: s.error_rate,
    rps: s.rps,
  };
  return meta.thresholds.map((t) => {
    const actual = get[t.metric] ?? 0;
    const ok = { lt: actual < t.value, lte: actual <= t.value, gt: actual > t.value, gte: actual >= t.value }[t.op];
    return { metric: t.metric, op: t.op, value: t.value, actual, passed: !!ok };
  });
}

const ERR_MSGS = [
  [503, 'status 503: upstream connect error or disconnect/reset before headers'],
  [500, 'assert status eq 200: got 500'],
  [0, 'Get "{url}": context deadline exceeded (Client.Timeout exceeded while awaiting headers)'],
  [502, 'status 502: bad gateway'],
  [429, 'assert status eq 200: got 429 (Too Many Requests)'],
  [0, 'dial tcp 10.0.3.17:443: connect: connection refused'],
  [200, 'assert json data.token_type eq "Bearer": got <missing>'],
];

function genErrors(run, meta, points) {
  const out = [];
  const withErr = points.filter((p) => p.errors > 0);
  const n = Math.min(40, withErr.reduce((a, p) => a + p.errors, 0));
  for (let i = 0; i < n; i++) {
    const p = withErr[Math.floor(rand() * withErr.length)];
    const [status, msg] = ERR_MSGS[Math.floor(rand() * (run.status === 'failed' ? 5 : ERR_MSGS.length))];
    const step = meta.steps[Math.floor(rand() * meta.steps.length)] || 'step';
    out.push({
      t: iso(Date.parse(p.t) + Math.floor(rand() * 1000)),
      step,
      status,
      message: msg.replace('{url}', 'https://api.example.test/' + step.replace(/\s+/g, '-')),
    });
  }
  return out.sort((a, b) => a.t.localeCompare(b.t));
}

const ERROR_GROUP_OTHER = '(other errors)';

// Grouped failures of a finished run, derived from its samples and scaled up
// to the run's total error count (samples are capped, groups count everything).
function groupsFromSamples(run, samples, overflow = false) {
  const total = run.summary?.errors ?? 0;
  if (!total || !samples.length) return [];
  const by = new Map();
  for (const e of samples) {
    const k = `${e.step}\u0000${e.status}\u0000${e.message}`;
    const g = by.get(k) || { step: e.step, status: e.status, message: e.message, count: 0, first: e.t, last: e.t };
    g.count++;
    if (e.t < g.first) g.first = e.t;
    if (e.t > g.last) g.last = e.t;
    by.set(k, g);
  }
  const groups = [...by.values()];
  const otherShare = overflow ? 0.04 : 0;
  const budget = Math.round(total * (1 - otherShare));
  let left = budget;
  groups.sort((a, b) => b.count - a.count);
  groups.forEach((g, i) => {
    const c = i === groups.length - 1 ? Math.max(1, left) : Math.max(1, Math.round((g.count / samples.length) * budget));
    left -= c;
    g.count = c;
  });
  if (overflow) {
    groups.push({
      step: '',
      status: 0,
      message: ERROR_GROUP_OTHER,
      count: Math.max(1, total - budget),
      first: groups[0].first,
      last: groups[0].last,
    });
  }
  return groups.sort((a, b) => b.count - a.count);
}

function finishedRun(testId, agoH, opts = {}) {
  const t = tests.get(testId);
  const meta = scenarioMeta(t.yaml);
  const id = 'r_' + hex(10);
  const started = now - agoH * 3600e3;
  const run = {
    id,
    test_id: testId,
    test_name: t.name,
    status: 'passed',
    yaml: t.yaml,
    workers: opts.workers ?? 1,
    started_at: iso(started),
    thresholds: [],
    peak_cpu: 0,
    warnings: [],
    label: opts.label ?? '',
    notes: opts.notes ?? '',
  };
  const total = opts.stopAt ?? meta.total;
  const pts = [];
  for (let s = 1; s <= Math.floor(total); s++) pts.push(genPoint(run, meta, s, opts));
  const summary = summarize(pts, meta, testId);
  run.summary = summary;
  run.thresholds = evalThresholds(meta, summary);
  run.peak_cpu = Math.max(...pts.map((p) => p.cpu));
  run.finished_at = iso(started + total * 1000 + 400);
  if (opts.stopAt) run.status = 'stopped';
  else run.status = run.thresholds.every((x) => x.passed) ? 'passed' : 'failed';
  if (opts.error) {
    run.status = 'error';
    run.error = opts.error;
  }
  if (run.peak_cpu > 0.8) run.warnings.push(`Load generator CPU peaked at ${Math.round(run.peak_cpu * 100)}% — results may be skewed by the client.`);
  if (summary.dropped > 0) run.warnings.push(`${summary.dropped.toLocaleString()} iterations dropped: max_vus (${meta.maxVus}) reached.`);
  runs.set(id, run);
  metrics.set(id, pts);
  errorsByRun.set(id, genErrors(run, meta, pts));
  errorGroupsByRun.set(id, groupsFromSamples(run, errorsByRun.get(id), opts.overflow));
  return run;
}

// Historical runs: ~30 days of labelled releases. catalog-soak has a baseline
// (v1.2.0) and a recent release candidate that regressed p95 by ~25%;
// login-flow has a baseline and improved; orders-api has no baseline.
const D = 24; // hours
const baselines = {};
function history(testId, specs) {
  for (const sp of specs) {
    const r = finishedRun(testId, sp.ago, sp);
    if (sp.baseline) baselines[testId] = r.id;
  }
}
history('t_27d5b9f8', [
  { ago: 29 * D + 3, label: 'v1.0.0', notes: 'First capacity check after the catalog rewrite.' },
  { ago: 25 * D + 5, label: 'v1.0.1', latMult: 1.02 },
  { ago: 21 * D + 2, label: 'v1.1.0', latMult: 0.96 },
  { ago: 17 * D + 6, label: 'v1.1.1', latMult: 0.95, workers: 2 },
  { ago: 12 * D + 1, label: 'v1.2.0', latMult: 0.9, baseline: true, notes: 'New read-through cache. Agreed baseline for the 1.x line.' },
  { ago: 8 * D + 4, label: 'v1.2.1', latMult: 0.92 },
  { ago: 4 * D + 2, label: 'v1.3.0', latMult: 0.94, workers: 2 },
  { ago: 20, label: 'v1.4.0-rc1', latMult: 1.13, errMult: 1.6, notes: 'Switched product detail to the new pricing service.' },
]);
history('t_4b7f02d3', [
  { ago: 28 * D + 2, label: 'v2.3.0' },
  { ago: 23 * D + 7, label: 'v2.3.1', latMult: 1.04 },
  { ago: 18 * D + 3, label: 'v2.4.0', latMult: 1.0, baseline: true },
  { ago: 14 * D + 5, label: 'v2.4.1', stopAt: 74 },
  { ago: 9 * D + 1, label: 'v2.5.0', latMult: 2.3, errMult: 9, notes: 'Token cache disabled by mistake — see INC-2291.' },
  { ago: 5 * D + 6, label: 'v2.5.1', latMult: 0.97 },
  { ago: 30, label: 'v2.6.0', latMult: 0.82, workers: 2 },
]);
history('t_5a0c3e19', [
  { ago: 27 * D, label: 'v0.9.0' },
  { ago: 22 * D + 4, label: 'v0.9.1', latMult: 0.98 },
  { ago: 15 * D + 2, label: 'v1.0.0', latMult: 1.05 },
  { ago: 10 * D + 3, label: 'v1.0.1', latMult: 1.0 },
  { ago: 6 * D + 1, label: 'v1.1.0', latMult: 1.1 },
  { ago: 12, label: 'v1.1.1', latMult: 1.03 },
]);
history('t_9c1e44a0', [
  { ago: 19 * D, label: '' },
  { ago: 7 * D + 2, label: 'nightly' },
  { ago: 26 },
  { ago: 2, label: 'nightly' },
]);
history('t_e81a6c57', [
  { ago: 11 * D, degrade: 1.0, label: 'v3.1.0' },
  { ago: 50, degrade: 1.45, errBurst: [92, 104], label: 'v3.2.0', overflow: true }, // failed: errors during spike
]);
for (const [tid, rid] of Object.entries(baselines)) tests.get(tid).baseline_run_id = rid;
const errRun = {
  id: 'r_' + hex(10),
  test_id: 't_4b7f02d3',
  test_name: 'login-flow',
  status: 'error',
  yaml: tests.get('t_4b7f02d3').yaml,
  workers: 1,
  started_at: iso(now - (3 * D + 6) * 3600e3),
  finished_at: iso(now - (3 * D + 6) * 3600e3 + 120),
  thresholds: [],
  peak_cpu: 0.02,
  warnings: [],
  error: 'data: open users.csv: no such file or directory',
  label: 'v2.5.2',
  notes: '',
};
runs.set(errRun.id, errRun);
metrics.set(errRun.id, []);
errorsByRun.set(errRun.id, []);
errorGroupsByRun.set(errRun.id, []);

// ---------------------------------------------------------------- live runs
const live = new Map(); // runId -> { meta, clients:Set, timer, stopped }

function sse(res, event, data) {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

// ---- live statistics (event: stats) ----
// Error groups that show up over the run: [step index, status, message, from second, weight].
const LIVE_ERRORS = [
  [0, 500, 'assert status eq 200: got 500', 0, 1],
  [1, 503, 'status 503: upstream connect error or disconnect/reset before headers. reset reason: overflow', 30, 3],
  [
    2,
    0,
    'Get "https://catalog.internal.example.test/v2/search?q=wireless+headphones": context deadline exceeded (Client.Timeout exceeded while awaiting headers)',
    55,
    2,
  ],
  [3, 429, 'assert status eq 200: got 429 (Too Many Requests)\nbody: {"error":"rate limit exceeded","retry_after":1}', 95, 1],
  [1, 0, 'dial tcp 10.0.3.17:443: connect: connection refused', 125, 0.6],
];

function newLiveStats(meta) {
  const steps = meta.steps.length ? meta.steps : ['step 1'];
  return {
    steps,
    acc: Object.fromEntries(steps.map((n) => [n, { requests: 0, errors: 0, w: { mean: 0, p50: 0, p90: 0, p95: 0, p99: 0 }, max: 0 }])),
    groups: new Map(),
    current: null,
    elapsed: 0,
  };
}

const p90of = (p50, p95) => p50 + (p95 - p50) * 0.78;

function stepSummary(requests, errors, dur, lat) {
  return {
    requests,
    errors,
    dropped: 0,
    error_rate: requests ? round(errors / requests, 5) : 0,
    rps: round(requests / Math.max(1, dur), 2),
    mean_ms: round(lat.mean, 2),
    p50_ms: round(lat.p50, 2),
    p90_ms: round(lat.p90, 2),
    p95_ms: round(lat.p95, 2),
    p99_ms: round(lat.p99, 2),
    max_ms: round(lat.max, 2),
  };
}

// Folds one second (MetricPoint) into the cumulative per-step stats.
function accumulateLive(run, ls, p) {
  const perf = perfFor(run.test_id);
  const n = ls.steps.length;
  // Request split: list and detail dominate, search is rarer.
  const weights = ls.steps.map((_, i) => [0.36, 0.34, 0.18, 0.12][i] ?? 1 / n);
  const wsum = weights.reduce((a, b) => a + b, 0);
  let left = p.requests;
  const reqs = weights.map((w, i) => {
    const r = i === n - 1 ? left : Math.round((p.requests * w) / wsum);
    left -= r;
    return Math.max(0, r);
  });
  // Errors into the groups active at this second.
  const errs = new Array(n).fill(0);
  const active = LIVE_ERRORS.filter((d) => p.elapsed >= d[3] && d[0] < n);
  const aw = active.reduce((a, d) => a + d[4], 0);
  let eleft = p.errors;
  active.forEach((d, i) => {
    let c = i === active.length - 1 ? eleft : Math.floor((p.errors * d[4]) / aw + rand());
    c = Math.max(0, Math.min(eleft, c));
    eleft -= c;
    if (!c) return;
    errs[d[0]] += c;
    const key = `${d[0]}|${d[1]}|${d[2]}`;
    const at = iso(Date.parse(p.t) + Math.floor(rand() * 900));
    const g = ls.groups.get(key) || { step: ls.steps[d[0]], status: d[1], message: d[2], count: 0, first: at, last: at };
    g.count += c;
    g.last = at;
    ls.groups.set(key, g);
  });
  const cur = {};
  ls.steps.forEach((name, i) => {
    const f = perf.stepFactor[i] ?? 1;
    const j = () => noise(0.05);
    const p50 = p.p50_ms * f * j();
    const p95 = Math.max(p50 * 1.5, p.p95_ms * f * j());
    const p99 = Math.max(p95 * 1.15, p.p99_ms * f * j());
    const lat = { mean: p50 * 1.15, p50, p90: p90of(p50, p95), p95, p99, max: Math.max(p99 * 1.2, p.max_ms * f * (0.7 + rand() * 0.3)) };
    const e = Math.min(reqs[i], errs[i]);
    cur[name] = stepSummary(reqs[i], e, 1, lat);
    const a = ls.acc[name];
    a.requests += reqs[i];
    a.errors += e;
    for (const k of ['mean', 'p50', 'p90', 'p95', 'p99']) a.w[k] += lat[k] * reqs[i];
    a.max = Math.max(a.max, lat.max);
  });
  ls.current = {
    ...stepSummary(p.requests, p.errors, 1, {
      mean: p.mean_ms,
      p50: p.p50_ms,
      p90: p90of(p.p50_ms, p.p95_ms),
      p95: p.p95_ms,
      p99: p.p99_ms,
      max: p.max_ms,
    }),
    dropped: p.dropped,
    by_step: cur,
  };
  ls.elapsed = p.elapsed;
}

function liveStatsPayload(ls) {
  const dur = Math.max(1, ls.elapsed);
  const by = {};
  let requests = 0;
  let errors = 0;
  let max = 0;
  const w = { mean: 0, p50: 0, p90: 0, p95: 0, p99: 0 };
  for (const name of ls.steps) {
    const a = ls.acc[name];
    const r = a.requests || 1;
    // Cumulative tails are a bit wider than the average of per-second tails.
    by[name] = stepSummary(a.requests, a.errors, dur, {
      mean: a.w.mean / r,
      p50: a.w.p50 / r,
      p90: (a.w.p90 / r) * 1.02,
      p95: (a.w.p95 / r) * 1.04,
      p99: (a.w.p99 / r) * 1.08,
      max: a.max,
    });
    requests += a.requests;
    errors += a.errors;
    max = Math.max(max, a.max);
    for (const k of Object.keys(w)) w[k] += a.w[k];
  }
  const r = requests || 1;
  const total = {
    ...stepSummary(requests, errors, dur, {
      mean: w.mean / r,
      p50: w.p50 / r,
      p90: (w.p90 / r) * 1.02,
      p95: (w.p95 / r) * 1.05,
      p99: (w.p99 / r) * 1.1,
      max,
    }),
    by_step: by,
  };
  const empty = stepSummary(0, 0, 1, { mean: 0, p50: 0, p90: 0, p95: 0, p99: 0, max: 0 });
  return {
    elapsed: ls.elapsed,
    total,
    current: ls.current ?? { ...empty, by_step: {} },
    errors: liveGroups(ls),
  };
}

function liveGroups(ls) {
  return [...ls.groups.values()].map((g) => ({ ...g })).sort((a, b) => b.count - a.count);
}

function startLive(run, opts = {}) {
  const meta = scenarioMeta(run.yaml);
  const pts = [];
  metrics.set(run.id, pts);
  errorsByRun.set(run.id, []);
  const ls = newLiveStats(meta);
  const state = { meta, clients: new Set(), stopped: false, opts, ls };
  live.set(run.id, state);
  // Load starts once the run leaves `queued` (spot workers launched).
  const startedMs = Date.parse(run.started_at) + (opts.queueMs ?? 0);
  // Backfill seconds that already "happened" (for a run pre-started at boot).
  const already = Math.floor((Date.now() - startedMs) / 1000);
  for (let s = 1; s <= already && s <= meta.total; s++) {
    const p = genPoint(run, meta, s, opts);
    pts.push(p);
    accumulateLive(run, ls, p);
  }
  state.timer = setInterval(() => {
    if (state.stopped) return finishLive(run);
    if (run.status === 'queued') {
      if (Date.now() < startedMs) return;
      run.status = 'running';
      for (const c of state.clients) sse(c, 'status', run);
    }
    const elapsed = Math.floor((Date.now() - startedMs) / 1000);
    if (elapsed > meta.total) return finishLive(run);
    if (elapsed < 1 || pts.length >= elapsed) return;
    const p = genPoint(run, meta, elapsed, opts);
    pts.push(p);
    accumulateLive(run, ls, p);
    run.peak_cpu = Math.max(run.peak_cpu, p.cpu);
    for (const c of state.clients) sse(c, 'metric', p);
    const payload = liveStatsPayload(ls);
    for (const c of state.clients) sse(c, 'stats', payload);
  }, 250);
}

function finishLive(run) {
  const state = live.get(run.id);
  if (!state) return;
  clearInterval(state.timer);
  live.delete(run.id);
  const pts = metrics.get(run.id) || [];
  const summary = summarize(pts, state.meta, run.test_id);
  run.summary = summary;
  run.thresholds = evalThresholds(state.meta, summary);
  run.finished_at = iso(Date.now());
  run.status = state.stopped ? 'stopped' : run.thresholds.every((x) => x.passed) ? 'passed' : 'failed';
  errorsByRun.set(run.id, genErrors(run, state.meta, pts));
  errorGroupsByRun.set(run.id, liveGroups(state.ls));
  for (const c of state.clients) {
    sse(c, 'done', run);
    c.end();
  }
}

// A run that is in progress when the mock boots (started 75s ago).
function createRun(testId, workers, startedAgoS = 0, queueS = 0, extra = {}) {
  const t = tests.get(testId);
  const run = {
    id: 'r_' + hex(10),
    test_id: testId,
    test_name: t.name,
    status: queueS > 0 ? 'queued' : 'running',
    yaml: t.yaml,
    workers: workers || 1,
    started_at: iso(Date.now() - startedAgoS * 1000),
    thresholds: [],
    peak_cpu: 0,
    warnings: [],
    label: String(extra.label ?? '').slice(0, 100),
    notes: String(extra.notes ?? '').slice(0, 10000),
  };
  runs.set(run.id, run);
  startLive(run, { queueMs: queueS * 1000, errMult: extra.errMult });
  return run;
}
const liveRun = createRun('t_27d5b9f8', 1, 75, 0, { label: 'v1.4.0-rc2', errMult: 4 });
console.log(`[mock] live run ${liveRun.id} (catalog-soak) in progress`);
// A distributed run waiting for spot instances (4 workers wanted, 2 idle).
const queuedRun = createRun('t_9c1e44a0', 4, 42, 42 + QUEUE_S);
console.log(`[mock] queued run ${queuedRun.id} (smoke) waiting ${QUEUE_S}s for workers`);

// ---------------------------------------------------------------- workers
function workers() {
  const runningId = [...runs.values()].find((r) => r.status === 'running')?.id;
  const t = Date.now();
  const lastLive = runningId ? metrics.get(runningId)?.at(-1) : undefined;
  return [
    {
      id: 'local',
      addr: 'in-process',
      state: runningId ? 'busy' : 'idle',
      run_id: runningId,
      cpu: lastLive ? lastLive.cpu : 0.03,
      last_seen: iso(t - 800),
      version: '0.4.0',
    },
    { id: 'wrk-eu-1', addr: '10.0.4.21:7070', state: 'idle', cpu: 0.04, last_seen: iso(t - 2100), version: '0.4.0' },
    { id: 'wrk-eu-2', addr: '10.0.4.22:7070', state: 'idle', cpu: 0.05, last_seen: iso(t - 1300), version: '0.4.0' },
    { id: 'wrk-us-1', addr: '10.8.1.9:7070', state: 'lost', cpu: 0, last_seen: iso(t - 4 * 60e3), version: '0.3.2' },
    { id: 'spot-i-0a3f9c', addr: '172.31.9.140:7070', state: 'lost', cpu: 0, last_seen: iso(t - 37 * 60e3), version: '0.4.0' },
  ];
}

// ---------------------------------------------------------------- validate
// Structural checks of the auth block (see the Auth doc in internal/scenario/types.go).
function validateAuth(yaml) {
  const clean = yaml
    .split('\n')
    .map((l) => l.replace(/\s+#.*$/, '').replace(/^#.*$/, ''))
    .join('\n');
  const m = /^auth:(.*(?:\n(?:[ \t]+.*|\s*))*)/m.exec(clean);
  if (!m) return [];
  const block = m[1];
  const errs = [];
  const field = (k) => {
    const x = new RegExp(`(?:^|[\\s{,])${k}:\\s*("[^"]*"|'[^']*'|[^,}\\n]*)`, 'm').exec(block);
    return x ? x[1].trim().replace(/^["']|["']$/g, '') : undefined;
  };
  const type = field('type');
  if (!type) return ['auth.type: must be one of login, bearer, basic'];
  if (!['login', 'bearer', 'basic'].includes(type)) return [`auth.type: must be one of login, bearer, basic; got "${type}"`];
  const scope = field('scope');
  if (scope && !['per_user', 'shared'].includes(scope)) errs.push(`auth.scope: must be per_user or shared; got "${scope}"`);
  const refresh = field('refresh');
  if (refresh && !Number.isFinite(parseDuration(refresh))) errs.push(`auth.refresh: invalid duration "${refresh}"`);
  if (type === 'login') {
    if (!/^\s+login:/m.test(block)) errs.push('auth.login: required for type login');
    else {
      const path = field('path');
      if (!path) errs.push('auth.login.path: must not be empty');
      if (!/extract:\s*(\n\s+-|\[\s*\{)/.test(block)) errs.push('auth.login.extract: at least one extract is required');
    }
  } else {
    if (/^\s+login:/m.test(block)) errs.push(`auth.login: only allowed for type login`);
    if (scope) errs.push('auth.scope: only allowed for type login');
  }
  if (type === 'bearer' && !field('token')) errs.push('auth.token: must not be empty for type bearer');
  if (type === 'basic') {
    if (!field('username')) errs.push('auth.username: must not be empty for type basic');
    if (field('value')) errs.push('auth.value: must be empty for type basic');
  }
  return errs;
}

function validate(yaml) {
  const errors = [];
  const lines = yaml.split('\n');
  lines.forEach((l, i) => {
    if (/^\t/.test(l)) errors.push(`decode: yaml: line ${i + 1}: found character that cannot start any token`);
  });
  if (errors.length) return { valid: false, errors };
  const meta = scenarioMeta(yaml);
  if (!meta.name) errors.push('name: must not be empty');
  const base = (yaml.match(/base_url:\s*(\S+)/) || [])[1];
  if (!base) errors.push('target.base_url: must not be empty');
  else if (!/^https?:\/\/[^/]+/.test(base)) errors.push(`target.base_url: must be an absolute http or https URL, got "${base}"`);
  if (/^\s+model:\s*(?!open\b|closed\b)(\S+)/m.test(yaml)) {
    const v = yaml.match(/^\s+model:\s*(\S+)/m)[1];
    errors.push(`load.model: must be "open" or "closed", got "${v}"`);
  }
  const durs = [...yaml.matchAll(/duration:\s*"?([^\s"#]+)/g)];
  durs.forEach((d, i) => {
    if (!Number.isFinite(parseDuration(d[1])) || /^\d+$/.test(d[1]))
      errors.push(`load.stages[${i}].duration: invalid duration "${d[1]}": expected a Go duration such as "30s"`);
  });
  if (meta.stages.length === 0 && durs.length === 0) errors.push('load.stages: at least one stage is required');
  else if (meta.stages.length && !meta.stages.some((s) => s.target > 0))
    errors.push('load.stages: at least one stage must have target > 0');
  if (!/^steps:/m.test(yaml) || meta.steps.length === 0) errors.push('steps: at least one step is required');
  const seen = new Set();
  meta.steps.forEach((s, i) => {
    if (seen.has(s)) errors.push(`steps[${i}].name: duplicate step name "${s}"; names must be unique`);
    seen.add(s);
  });
  [...yaml.matchAll(/^\s+path:\s*(.*)$/gm)].forEach((m, i) => {
    const p = m[1].trim().replace(/^["']|["']$/g, '');
    if (!p.startsWith('/') && !p.startsWith('{{')) errors.push(`steps[${i}].path: must start with "/" or a {{variable}}, got "${p}"`);
  });
  errors.push(...validateAuth(yaml));
  const okMetrics = ['latency.p50', 'latency.p90', 'latency.p95', 'latency.p99', 'latency.mean', 'latency.max', 'error_rate', 'rps'];
  [...yaml.matchAll(/-\s*metric:\s*(\S+)/g)].forEach((m, i) => {
    if (!okMetrics.includes(m[1])) errors.push(`thresholds[${i}].metric: unknown metric "${m[1]}"`);
  });
  if (errors.length) return { valid: false, errors };
  return { valid: true, errors: [], model: meta.model, duration_s: meta.total, steps: meta.steps.length };
}

// ---------------------------------------------------------------- reporting
const FINISHED = new Set(['passed', 'failed', 'stopped', 'error']);
const METRIC_KEYS = ['p50_ms', 'p90_ms', 'p95_ms', 'p99_ms', 'max_ms', 'mean_ms', 'rps', 'error_rate'];
const LOWER_BETTER = { rps: false };

// Steady state = without an initial ramp from 0 and a final ramp-down.
function steadyRange(meta, total) {
  const st = meta.stages;
  let from = 0;
  let to = meta.total || total;
  if (st.length > 1 && st[0].target > 0) from = st[0].d;
  if (st.length > 1 && st[st.length - 1].target < st[st.length - 2].target) to -= st[st.length - 1].d;
  to = Math.min(to, total);
  return to - from >= 2 ? { from, to } : { from: 0, to: total };
}

function resolveWindow(run, q) {
  const meta = scenarioMeta(run.yaml);
  const pts = metrics.get(run.id) || [];
  const total = pts.length ? pts[pts.length - 1].elapsed : runSeconds(run);
  const kind = q.window || 'steady';
  if (kind === 'full') return { kind, from: 0, to: total };
  if (kind === 'custom') {
    const from = Number(q.from);
    const to = Number(q.to);
    if (!Number.isFinite(from) || !Number.isFinite(to) || from < 0 || to <= from) return null;
    return { kind, from, to: Math.min(to, Math.max(total, from + 1)) };
  }
  return { kind: 'steady', ...steadyRange(meta, total) };
}

function runSeconds(run) {
  const a = Date.parse(run.started_at);
  const b = run.finished_at ? Date.parse(run.finished_at) : Date.now();
  return Math.max(0, Math.round((b - a) / 1000));
}

function windowSummary(run, w) {
  const meta = scenarioMeta(run.yaml);
  const pts = (metrics.get(run.id) || []).filter((p) => p.elapsed > w.from && p.elapsed <= w.to);
  if (!pts.length) return run.summary ? { ...run.summary } : summarize([], meta, run.test_id);
  return summarize(pts, meta, run.test_id);
}

function delta(metric, ref, cand, tol) {
  const absolute = metric === 'error_rate';
  const lower = LOWER_BETTER[metric] ?? true;
  if (!absolute && ref === 0) return { metric, reference: ref, candidate: cand, delta: 0, verdict: 'n/a' };
  const d = absolute ? cand - ref : (cand - ref) / ref;
  const t = absolute ? tol / 10 : tol;
  const worse = lower ? d > t : d < -t;
  const better = lower ? d < -t : d > t;
  return { metric, reference: ref, candidate: cand, delta: round(d, 6), verdict: worse ? 'worse' : better ? 'better' : 'same' };
}

function compareSummaries(refId, ref, candId, cand, tol) {
  const ms = METRIC_KEYS.map((k) => delta(k, ref[k], cand[k], tol));
  const byStep = {};
  for (const name of Object.keys(ref.by_step || {})) {
    const a = ref.by_step[name];
    const b = cand.by_step?.[name];
    if (b) byStep[name] = METRIC_KEYS.map((k) => delta(k, a[k], b[k], tol));
  }
  return {
    reference_run_id: refId,
    candidate_run_id: candId,
    tolerance: tol,
    metrics: ms,
    regression: ms.some((x) => x.verdict === 'worse'),
    by_step: Object.keys(byStep).length ? byStep : undefined,
  };
}

function reportFor(run, q, tol = 0.1, withBaseline = true) {
  const w = resolveWindow(run, q);
  if (!w) return null;
  const meta = scenarioMeta(run.yaml);
  const summary = run.summary ? windowSummary(run, w) : summarize([], meta, run.test_id);
  const rep = { run, window: w, summary, thresholds: run.summary ? evalThresholds(meta, summary) : [] };
  const t = tests.get(run.test_id);
  const base = t?.baseline_run_id && t.baseline_run_id !== run.id ? runs.get(t.baseline_run_id) : null;
  if (withBaseline && base?.summary && run.summary) {
    const bw = resolveWindow(base, q) || resolveWindow(base, { window: 'steady' });
    rep.baseline = compareSummaries(base.id, windowSummary(base, bw), run.id, summary, tol);
  }
  return rep;
}

const stripSteps = (r) => (r.summary ? { ...r, summary: { ...r.summary, by_step: undefined } } : r);

function overview(days) {
  const since = now - days * 86400e3;
  const all = [...runs.values()].sort((a, b) => b.started_at.localeCompare(a.started_at));
  const inPeriod = all.filter((r) => Date.parse(r.started_at) >= since);
  const finished = inPeriod.filter((r) => FINISHED.has(r.status));
  const perDay = new Map();
  const dayKey = (ms) => new Date(ms).toISOString().slice(0, 10);
  for (let d = since; d <= Date.now() + 1; d += 86400e3) perDay.set(dayKey(d), { day: dayKey(d), passed: 0, failed: 0, other: 0 });
  for (const r of inPeriod) {
    const row = perDay.get(dayKey(Date.parse(r.started_at)));
    if (!row) continue;
    if (r.status === 'passed') row.passed++;
    else if (r.status === 'failed') row.failed++;
    else row.other++;
  }
  const health = [...tests.values()]
    .map((t) => {
      const tr = finished.filter((r) => r.test_id === t.id);
      const latest = all.find((r) => r.test_id === t.id && FINISHED.has(r.status));
      const h = {
        test_id: t.id,
        test_name: t.name,
        latest_run: latest ? stripSteps(latest) : undefined,
        pass_rate: tr.length ? round(tr.filter((r) => r.status === 'passed').length / tr.length, 4) : 0,
        run_count: tr.length,
      };
      if (latest) {
        const rep = reportFor(latest, { window: 'steady' });
        if (rep?.baseline) h.baseline = { ...rep.baseline, by_step: undefined };
      }
      return h;
    })
    .filter((h) => h.latest_run)
    .sort((a, b) => a.test_name.localeCompare(b.test_name));
  return {
    since: iso(since),
    runs: inPeriod.length,
    passed: inPeriod.filter((r) => r.status === 'passed').length,
    failed: inPeriod.filter((r) => r.status === 'failed').length,
    errored: inPeriod.filter((r) => r.status === 'error' || r.status === 'stopped').length,
    requests: inPeriod.reduce((a, r) => a + (r.summary?.requests || 0), 0),
    tests: health,
    recent_runs: inPeriod.slice(0, 10).map(stripSteps),
    runs_per_day: [...perDay.values()],
  };
}

function htmlReport(rep) {
  const r = rep.run;
  const s = rep.summary;
  const row = (k, v) => `<tr><th>${k}</th><td>${v}</td></tr>`;
  return `<!doctype html><html><head><meta charset="utf-8"><title>${r.test_name} ${r.label || r.id} — GoLoad report</title>
<style>body{font:14px system-ui,sans-serif;max-width:760px;margin:32px auto;color:#15181e}th{text-align:left;padding:4px 16px 4px 0;color:#4a5160}td{font-variant-numeric:tabular-nums}</style></head>
<body><h1>${r.test_name} ${r.label ? `· ${r.label}` : ''}</h1><p>Run ${r.id} · ${r.status} · started ${r.started_at} · window ${rep.window.kind} ${rep.window.from}s–${rep.window.to}s</p>
<p><em>Mock report stub — the real server renders charts and per-step tables.</em></p>
<table>${row('Requests', s.requests)}${row('RPS', s.rps)}${row('Error rate', (s.error_rate * 100).toFixed(2) + '%')}${row('p50', s.p50_ms + ' ms')}${row('p95', s.p95_ms + ' ms')}${row('p99', s.p99_ms + ' ms')}</table>
</body></html>`;
}

function sendFile(res, type, name, body) {
  res.writeHead(200, { 'Content-Type': type, 'Content-Disposition': `attachment; filename="${name}"` });
  res.end(body);
}

// ---------------------------------------------------------------- http
function send(res, status, body) {
  if (body === undefined) {
    res.writeHead(status);
    return res.end();
  }
  const data = JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(data);
}
const apiErr = (res, status, error, details) => send(res, status, details ? { error, details } : { error });

function readBody(req) {
  return new Promise((resolve) => {
    const chunks = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => resolve(Buffer.concat(chunks)));
  });
}
async function readJSON(req) {
  const b = await readBody(req);
  try {
    return JSON.parse(b.toString('utf8') || '{}');
  } catch {
    return null;
  }
}

function withLastRun(t) {
  const last = [...runs.values()]
    .filter((r) => r.test_id === t.id)
    .sort((a, b) => b.started_at.localeCompare(a.started_at))[0];
  return last ? { ...t, last_run: last } : t;
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://x');
  const parts = url.pathname.split('/').filter(Boolean).map(decodeURIComponent);
  const m = req.method;
  if (parts[0] !== 'api') return apiErr(res, 404, 'not found');
  const [, a, b, c, d] = parts;
  // tiny latency so loading states are realistic
  await new Promise((r) => setTimeout(r, 40));
  try {
    if (a === 'health' && m === 'GET') return send(res, 200, { status: 'ok', version: '0.4.0-mock' });

    if (TOKEN) {
      const h = req.headers.authorization || '';
      let got = /^bearer /i.test(h) ? h.slice(7).trim() : '';
      if (!got && m === 'GET' && url.pathname.endsWith('/stream')) got = url.searchParams.get('access_token') || '';
      if (got !== TOKEN) {
        res.setHeader('WWW-Authenticate', 'Bearer realm="api"');
        return apiErr(res, 401, 'unauthorized');
      }
    }

    if (a === 'validate' && m === 'POST') {
      const body = await readJSON(req);
      return send(res, 200, validate(String(body?.yaml ?? '')));
    }

    if (a === 'tests') {
      if (!b) {
        if (m === 'GET') {
          const list = [...tests.values()].sort((x, y) => x.name.localeCompare(y.name)).map(withLastRun);
          return send(res, 200, list);
        }
        if (m === 'POST') {
          const body = await readJSON(req);
          const v = validate(String(body?.yaml ?? ''));
          if (!v.valid) return apiErr(res, 400, 'invalid scenario', v.errors);
          const id = 't_' + hex(8);
          const t = {
            id,
            name: body.name || scenarioMeta(body.yaml).name,
            yaml: body.yaml,
            files: [],
            created_at: iso(Date.now()),
            updated_at: iso(Date.now()),
          };
          tests.set(id, t);
          return send(res, 201, t);
        }
      }
      const t = tests.get(b);
      if (!t) return apiErr(res, 404, `test ${b} not found`);
      if (!c) {
        if (m === 'GET') return send(res, 200, t);
        if (m === 'PUT') {
          const body = await readJSON(req);
          const v = validate(String(body?.yaml ?? ''));
          if (!v.valid) return apiErr(res, 400, 'invalid scenario', v.errors);
          t.name = body.name || t.name;
          t.yaml = body.yaml;
          t.updated_at = iso(Date.now());
          return send(res, 200, t);
        }
        if (m === 'DELETE') {
          tests.delete(b);
          return send(res, 204);
        }
      }
      if (c === 'files' && d) {
        if (m === 'PUT') {
          const body = await readBody(req);
          t.files = t.files.filter((f) => f.name !== d).concat({ name: d, size: body.length });
          return send(res, 204);
        }
        if (m === 'DELETE') {
          if (!t.files.some((f) => f.name === d)) return apiErr(res, 404, `file ${d} not found`);
          t.files = t.files.filter((f) => f.name !== d);
          return send(res, 204);
        }
      }
      if (c === 'baseline' && m === 'PUT') {
        const body = (await readJSON(req)) || {};
        const rid = String(body.run_id ?? '');
        if (rid) {
          const r = runs.get(rid);
          if (!r || r.test_id !== t.id) return apiErr(res, 400, 'invalid baseline', [`run_id: run ${rid} is not a run of this test`]);
          if (!r.summary) return apiErr(res, 400, 'invalid baseline', ['run_id: run has no results']);
          t.baseline_run_id = rid;
        } else delete t.baseline_run_id;
        return send(res, 200, t);
      }
      if (c === 'trend' && m === 'GET') {
        const q = Object.fromEntries(url.searchParams);
        const limit = Number(q.limit || 30);
        const list = [...runs.values()]
          .filter((r) => r.test_id === t.id && FINISHED.has(r.status) && r.summary)
          .sort((x, y) => x.started_at.localeCompare(y.started_at))
          .slice(-limit);
        const out = [];
        for (const r of list) {
          const w = resolveWindow(r, q);
          if (!w) return apiErr(res, 400, 'invalid window');
          const sm = windowSummary(r, w);
          delete sm.by_step;
          out.push({
            run_id: r.id,
            started_at: r.started_at,
            label: r.label || '',
            status: r.status,
            workers: r.workers,
            summary: sm,
            is_baseline: t.baseline_run_id === r.id,
          });
        }
        return send(res, 200, out);
      }
      if (c === 'runs' && m === 'POST') {
        const body = (await readJSON(req)) || {};
        if ([...runs.values()].some((r) => (r.status === 'running' || r.status === 'queued') && r.test_id === t.id))
          return apiErr(res, 409, 'a run of this test is already in progress');
        const meta = scenarioMeta(t.yaml);
        const dataFile = (t.yaml.match(/^data:\s*\n\s+file:\s*(\S+)/m) || [])[1];
        if (dataFile && !t.files.some((f) => f.name === dataFile))
          return apiErr(res, 400, 'cannot start run', [`data.file: ${dataFile} is not uploaded`]);
        if (!meta.total) return apiErr(res, 400, 'cannot start run', ['load.stages: total duration is 0']);
        const want = Math.max(1, Number(body.workers) || 1);
        const idle = workers().filter((w) => w.id !== 'local' && w.state === 'idle').length;
        if (want > 1 && want > idle && !PROVISIONER)
          return apiErr(res, 409, 'not enough idle workers', [
            `cluster: not enough idle workers: ${want} requested, ${idle} idle`,
          ]);
        const run = createRun(t.id, want, 0, want > 1 && want > idle ? LAUNCH_S : 0, body);
        return send(res, 201, run);
      }
      return apiErr(res, 405, 'method not allowed');
    }

    if (a === 'runs') {
      if (!b && m === 'GET') {
        const testId = url.searchParams.get('test_id');
        const limit = Number(url.searchParams.get('limit') || 0);
        let list = [...runs.values()].sort((x, y) => y.started_at.localeCompare(x.started_at));
        if (testId) list = list.filter((r) => r.test_id === testId);
        if (limit) list = list.slice(0, limit);
        return send(res, 200, list);
      }
      const run = runs.get(b);
      if (!run) return apiErr(res, 404, `run ${b} not found`);
      if (!c && m === 'GET') return send(res, 200, run);
      if (!c && m === 'PATCH') {
        const body = await readJSON(req);
        if (!body) return apiErr(res, 400, 'invalid JSON');
        const errs = [];
        if (typeof body.label === 'string' && body.label.length > 100) errs.push('label: at most 100 characters');
        if (typeof body.notes === 'string' && body.notes.length > 10000) errs.push('notes: at most 10000 characters');
        if (errs.length) return apiErr(res, 400, 'invalid run update', errs);
        if (typeof body.label === 'string') run.label = body.label.trim();
        if (typeof body.notes === 'string') run.notes = body.notes;
        return send(res, 200, run);
      }
      if (!c && m === 'DELETE') {
        if (!FINISHED.has(run.status)) return apiErr(res, 409, 'run is still active');
        runs.delete(run.id);
        metrics.delete(run.id);
        errorsByRun.delete(run.id);
        errorGroupsByRun.delete(run.id);
        for (const t of tests.values()) if (t.baseline_run_id === run.id) delete t.baseline_run_id;
        return send(res, 204);
      }
      if (c === 'report' && m === 'GET') {
        const rep = reportFor(run, Object.fromEntries(url.searchParams));
        if (!rep) return apiErr(res, 400, 'invalid window', ['window: custom needs 0 <= from < to']);
        return send(res, 200, rep);
      }
      if (c === 'report.html' && m === 'GET') {
        const rep = reportFor(run, Object.fromEntries(url.searchParams));
        if (!rep) return apiErr(res, 400, 'invalid window');
        return sendFile(res, 'text/html; charset=utf-8', `goload-${run.test_name}-${run.id}.html`, htmlReport(rep));
      }
      if (c === 'export.csv' && m === 'GET') {
        const cols = ['t', 'elapsed', 'target', 'active', 'requests', 'errors', 'dropped', 'rps', 'error_rate', 'mean_ms', 'p50_ms', 'p95_ms', 'p99_ms', 'max_ms', 'cpu', 'workers'];
        const lines = [cols.join(',')].concat((metrics.get(run.id) || []).map((p) => cols.map((k) => p[k]).join(',')));
        return sendFile(res, 'text/csv; charset=utf-8', `goload-${run.id}-metrics.csv`, lines.join('\n') + '\n');
      }
      if (c === 'export.json' && m === 'GET') {
        const body = JSON.stringify({ run, metrics: metrics.get(run.id) || [], errors: errorsByRun.get(run.id) || [] }, null, 2);
        return sendFile(res, 'application/json', `goload-${run.id}.json`, body);
      }
      if (c === 'stop' && m === 'POST') {
        const st = live.get(run.id);
        if (!st) return apiErr(res, 409, 'run is not running');
        st.stopped = true;
        return send(res, 202);
      }
      if (c === 'metrics' && m === 'GET') return send(res, 200, metrics.get(run.id) || []);
      if (c === 'errors' && m === 'GET') return send(res, 200, errorsByRun.get(run.id) || []);
      if (c === 'error-groups' && m === 'GET') {
        const st = live.get(run.id);
        return send(res, 200, st ? liveGroups(st.ls) : errorGroupsByRun.get(run.id) || []);
      }
      if (c === 'stream' && m === 'GET') {
        res.writeHead(200, {
          'Content-Type': 'text/event-stream',
          'Cache-Control': 'no-cache',
          Connection: 'keep-alive',
        });
        res.write(': connected\n\n');
        const st = live.get(run.id);
        if (!st) {
          sse(res, 'done', run);
          return res.end();
        }
        sse(res, 'status', run);
        if (run.status === 'running' && st.ls.elapsed > 0) sse(res, 'stats', liveStatsPayload(st.ls));
        st.clients.add(res);
        const ping = setInterval(() => res.write(': ping\n\n'), 15000);
        req.on('close', () => {
          clearInterval(ping);
          st.clients.delete(res);
        });
        return;
      }
      return apiErr(res, 405, 'method not allowed');
    }

    if (a === 'workers' && m === 'GET') return send(res, 200, workers());

    if (a === 'compare' && m === 'GET') {
      const ids = (url.searchParams.get('runs') || '').split(',').map((x) => x.trim()).filter(Boolean);
      if (ids.length < 2 || ids.length > 6) return apiErr(res, 400, 'invalid compare', ['runs: between 2 and 6 run ids']);
      const tolRaw = url.searchParams.get('tolerance');
      const tol = tolRaw === null || tolRaw === '' ? 0.1 : Number(tolRaw);
      if (!Number.isFinite(tol) || tol < 0 || tol > 1) return apiErr(res, 400, 'invalid compare', ['tolerance: must be 0..1']);
      const q = Object.fromEntries(url.searchParams);
      const reps = [];
      for (const id of ids) {
        const r = runs.get(id);
        if (!r) return apiErr(res, 404, `run ${id} not found`);
        if (!FINISHED.has(r.status)) return apiErr(res, 409, `run ${id} is still active`);
        const rep = reportFor(r, q, tol, false);
        if (!rep) return apiErr(res, 400, 'invalid window');
        reps.push(rep);
      }
      const comparisons = reps.slice(1).map((c2) => compareSummaries(reps[0].run.id, reps[0].summary, c2.run.id, c2.summary, tol));
      return send(res, 200, { runs: reps, comparisons });
    }

    if (a === 'reports' && b === 'overview' && m === 'GET') {
      const days = Math.max(1, Math.min(365, Number(url.searchParams.get('days') || 30) || 30));
      return send(res, 200, overview(days));
    }
    return apiErr(res, 404, 'not found');
  } catch (e) {
    console.error(e);
    return apiErr(res, 500, String(e?.message || e));
  }
});

server.listen(PORT, () => console.log(`[mock] GoLoad mock API on http://localhost:${PORT}`));
