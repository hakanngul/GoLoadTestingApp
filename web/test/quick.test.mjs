// Round-trip tests for the pure scenario converters (Quick form <-> full form
// <-> YAML), including the auth block. Run with `pnpm test` (node --test; Node
// >= 22.18 strips the TypeScript types of the imported modules natively).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parse } from 'yaml';
import { defaultQuick, formToQuick, quickToForm, splitUrl, durFromGo } from '../src/lib/quick.ts';
import { formToYaml, yamlToForm } from '../src/lib/scenario.ts';

const fromYaml = (y) => {
  const { form, error } = yamlToForm(y);
  assert.equal(error, null);
  return form;
};

test('splitUrl splits origin and keeps {{vars}} and query verbatim', () => {
  assert.deepEqual(splitUrl('https://api.example.com/v1/products?page=1'), {
    base: 'https://api.example.com',
    path: '/v1/products?page=1',
    ok: true,
  });
  assert.equal(splitUrl('https://h.test/items/{{id}}').path, '/items/{{id}}');
  assert.equal(splitUrl('https://h.test').path, '/');
  assert.equal(splitUrl('https://h.test?x=1').path, '/?x=1');
  assert.equal(splitUrl('api.example.com/x').ok, false);
  // An existing base_url with a path prefix is kept.
  assert.deepEqual(splitUrl('https://h.test/api/v2/x', 'https://h.test/api'), {
    base: 'https://h.test/api',
    path: '/v2/x',
    ok: true,
  });
});

test('durFromGo picks the largest exact unit', () => {
  assert.deepEqual(durFromGo('90s'), { value: '90', unit: 's' });
  assert.deepEqual(durFromGo('2m'), { value: '2', unit: 'm' });
  assert.deepEqual(durFromGo('1h'), { value: '1', unit: 'h' });
  assert.deepEqual(durFromGo('1m30s'), { value: '90', unit: 's' });
});

test('quick with login auth generates the contract YAML', () => {
  const q = defaultQuick();
  q.name = 'products';
  q.url = 'https://api.example.com/v1/products?page=1';
  q.mode = 'rps';
  q.target = '50';
  q.rampDownOn = true;
  q.auth.type = 'login';
  q.auth.loginPath = '/api/login';
  q.auth.tokenPath = 'data.access_token';
  q.auth.refresh = { value: '10', unit: 'm' };
  const y = formToYaml(quickToForm(q));
  const doc = parse(y);
  assert.deepEqual(doc.target, { base_url: 'https://api.example.com' });
  assert.deepEqual(doc.load, {
    model: 'open',
    stages: [
      { duration: '30s', target: 50 },
      { duration: '2m', target: 50 },
      { duration: '15s', target: 0 },
    ],
  });
  assert.deepEqual(doc.data, { file: 'users.csv', mode: 'sequential' });
  assert.deepEqual(doc.auth, {
    type: 'login',
    scope: 'per_user',
    refresh: '10m',
    login: {
      method: 'POST',
      path: '/api/login',
      headers: { 'Content-Type': 'application/json' },
      body: '{"username":"{{username}}","password":"{{password}}"}',
      extract: [{ var: 'token', from: 'json', expr: 'data.access_token' }],
    },
  });
  assert.deepEqual(doc.steps, [
    { name: 'GET /v1/products', method: 'GET', path: '/v1/products?page=1', assert: [{ type: 'status', value: '200' }] },
  ]);
  assert.deepEqual(doc.thresholds, [
    { metric: 'latency.p95', op: 'lt', value: 500 },
    { metric: 'error_rate', op: 'lt', value: 0.01 },
  ]);
  // Keys appear in types.go order: auth after data, before steps.
  assert.deepEqual(Object.keys(doc), ['name', 'target', 'load', 'data', 'auth', 'steps', 'thresholds']);
});

test('quick round-trips through YAML (login csv, fixed, bearer, none)', () => {
  const variants = [];
  const base = defaultQuick();
  base.url = 'https://shop.test/cart';
  base.method = 'POST';
  base.body = '{"sku":"A-1","qty":2}';
  base.headers = [{ key: 'Accept', value: 'application/json' }];
  base.errorPct = '2.5';
  variants.push(structuredClone(base));

  const csv = structuredClone(base);
  csv.auth.type = 'login';
  csv.auth.scope = 'per_user';
  csv.auth.csvFile = 'accounts.csv';
  variants.push(csv);

  const fixed = structuredClone(base);
  fixed.auth.type = 'login';
  fixed.auth.credMode = 'fixed';
  fixed.auth.userField = 'email';
  fixed.auth.username = 'svc@shop.test';
  fixed.auth.password = 'p"w';
  fixed.auth.scope = 'shared';
  fixed.auth.loginMethod = 'PUT';
  variants.push(fixed);

  const bearer = structuredClone(base);
  bearer.auth.type = 'bearer';
  bearer.auth.token = 'abc123';
  bearer.checkP95 = false;
  variants.push(bearer);

  for (const q of variants) {
    const y1 = formToYaml(quickToForm(q));
    const back = formToQuick(fromYaml(y1));
    assert.deepEqual(back.reasons, [], y1);
    const y2 = formToYaml(quickToForm(back.quick));
    assert.equal(y2, y1);
  }
});

test('full form auth round-trips through YAML', () => {
  const y = `name: auth-all
target:
  base_url: https://x.test
load:
  model: closed
  stages:
    - duration: 10s
      target: 1
auth:
  type: login
  scope: shared
  refresh: 5m
  login:
    name: sign in
    method: POST
    path: /oauth/token
    headers:
      Content-Type: application/x-www-form-urlencoded
    body: grant_type=client_credentials
    extract:
      - var: token
        from: json
        expr: access_token
      - var: tenant
        from: header
        expr: X-Tenant
    assert:
      - type: status
        value: "200"
  header: X-Auth
  value: Token {{token}}
  inject: false
steps:
  - name: a
    method: GET
    path: /a
`;
  const f = fromYaml(y);
  assert.equal(f.auth.type, 'login');
  assert.equal(f.auth.inject, false);
  assert.equal(f.auth.login.extract.length, 2);
  const out = formToYaml(f);
  assert.deepEqual(parse(out).auth, parse(y).auth);
  // Not representable in Quick mode, with reasons.
  const r = formToQuick(f);
  assert.equal(r.quick, null);
  assert.ok(r.reasons.some((x) => /X-Auth/.test(x)));
  assert.ok(r.reasons.some((x) => /injection/.test(x)));

  for (const a of [
    { type: 'bearer', token: '{{api_key}}' },
    { type: 'basic', username: '{{user}}', password: '{{pass}}' },
  ]) {
    const doc = parse(y);
    doc.auth = a;
    const g = fromYaml(JSON.stringify(doc));
    assert.deepEqual(parse(formToYaml(g)).auth, a);
  }
  const noAuth = parse(y);
  delete noAuth.auth;
  assert.equal(parse(formToYaml(fromYaml(JSON.stringify(noAuth)))).auth, undefined);
});

test('representability rules', () => {
  const reasons = (y) => formToQuick(fromYaml(y)).reasons;
  const ok = `name: t
target: {base_url: "https://x.test"}
load: {model: open, stages: [{duration: 30s, target: 5}, {duration: 1m, target: 5}]}
steps: [{name: s, method: GET, path: /a}]
`;
  assert.deepEqual(reasons(ok), []);
  assert.ok(reasons(ok.replace('steps: [', 'steps: [{name: b, method: GET, path: /b}, ')).some((r) => /2 steps/.test(r)));
  assert.ok(reasons(ok.replace('target: 5}]}', 'target: 9}]}')).some((r) => /ramp-up/.test(r)));
  assert.ok(reasons(ok.replace('method: GET, path: /a', 'type: ws, path: /ws')).some((r) => /WebSocket/.test(r)));
  assert.ok(
    reasons(ok + 'thresholds: [{metric: latency.p99, op: lt, value: 9}]\n').some((r) => /latency.p99/.test(r)),
  );
  assert.ok(reasons(ok + 'data: {file: u.csv}\n').some((r) => /CSV/.test(r)));
  assert.ok(reasons(ok + 'auth: {type: basic, username: a, password: b}\n').some((r) => /basic/.test(r)));
  assert.deepEqual(reasons(ok + 'auth: {type: bearer, token: t}\n'), []);
});
