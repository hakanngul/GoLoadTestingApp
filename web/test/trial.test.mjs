// Tests for the pure "Test request" helpers (src/lib/trial.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { applyFix, fmtMs, headerValue, prettyBody, sortHints, stepBadge, summary } from '../src/lib/trial.ts';
import { formToQuick, quickToForm, defaultQuick } from '../src/lib/quick.ts';
import { defaultForm, defaultLoginStep, formToYaml, yamlToForm } from '../src/lib/scenario.ts';

const step = (o) => ({
  name: 's',
  kind: 'http',
  index: 0,
  state: 'passed',
  status: 200,
  duration_ms: 12,
  bytes: 0,
  request_headers: [],
  response_headers: [],
  extracted: [],
  assertions: [],
  ...o,
});

test('applyFix base_url updates the form and the YAML', () => {
  const f = defaultForm();
  const next = applyFix(f, { kind: 'base_url', step: -1, value: 'https://www.stageletsbet.de', label: '' });
  assert.equal(next.target.base_url, 'https://www.stageletsbet.de');
  assert.equal(f.target.base_url, 'https://api.example.test', 'input is not mutated');
  assert.match(formToYaml(next), /base_url: https:\/\/www\.stageletsbet\.de/);
  assert.equal(applyFix(f, { kind: 'base_url', step: -1, value: 'not a url', label: '' }), null);
});

test('applyFix base_url keeps Quick mode in sync', () => {
  const q = { ...defaultQuick(), url: 'https://stageletsbet.de/' };
  const f = quickToForm(q);
  const next = applyFix(f, { kind: 'base_url', step: -1, value: 'https://www.stageletsbet.de', label: '' });
  const nq = formToQuick(next).quick;
  assert.equal(nq.url, 'https://www.stageletsbet.de/');
});

test('applyFix step_path for a step and for the login request', () => {
  const f = defaultForm();
  const next = applyFix(f, { kind: 'step_path', step: 0, value: '/health/', label: '' });
  assert.equal(next.steps[0].path, '/health/');
  assert.equal(applyFix(f, { kind: 'step_path', step: 99, value: '/x', label: '' }), null);
  // auth.login only when auth type is login.
  assert.equal(applyFix(f, { kind: 'step_path', step: -1, value: '/auth/', label: '' }), null);
  f.auth.type = 'login';
  f.auth.login = defaultLoginStep();
  assert.equal(applyFix(f, { kind: 'step_path', step: -1, value: '/auth/', label: '' }).auth.login.path, '/auth/');
  const y = formToYaml(applyFix(f, { kind: 'step_path', step: -1, value: '/auth/', label: '' }));
  assert.equal(yamlToForm(y).form.auth.login.path, '/auth/');
});

test('stepBadge', () => {
  assert.deepEqual(stepBadge(step({})), { label: '200', tone: 'good' });
  assert.deepEqual(stepBadge(step({ status: 301 })), { label: '301', tone: 'warn' });
  assert.deepEqual(stepBadge(step({ status: 301, state: 'failed' })), { label: '301', tone: 'bad' });
  assert.deepEqual(stepBadge(step({ status: 0, state: 'failed' })), { label: 'error', tone: 'bad' });
  assert.deepEqual(stepBadge(step({ state: 'not_run', status: 0 })), { label: 'not run', tone: '' });
});

test('summary', () => {
  const ok = { ok: true, duration_ms: 1, data: {}, hints: [], steps: [step({}), step({ name: 't' })] };
  assert.deepEqual(summary(ok), { tone: 'good', text: 'All 2 steps passed' });
  const bad = { ...ok, ok: false, steps: [step({}), step({ name: 'home', state: 'failed' }), step({ state: 'not_run' })] };
  assert.deepEqual(summary(bad), { tone: 'bad', text: 'Failed at step 2 of 3: home' });
});

test('sortHints puts errors first and is stable', () => {
  const h = [
    { level: 'info', message: 'a' },
    { level: 'error', message: 'b' },
    { level: 'warning', message: 'c' },
    { level: 'error', message: 'd' },
  ];
  assert.deepEqual(
    sortHints(h).map((x) => x.message),
    ['b', 'd', 'c', 'a'],
  );
});

test('formatting helpers', () => {
  assert.equal(fmtMs(1234), '1.23 s');
  assert.equal(fmtMs(45.6), '46 ms');
  assert.equal(fmtMs(3.21), '3.2 ms');
  assert.equal(prettyBody('{"a":1}', 'application/json'), '{\n  "a": 1\n}');
  assert.equal(prettyBody('{"a":1', 'application/json'), '{"a":1', 'truncated JSON is left alone');
  assert.equal(prettyBody('<html>', 'text/html'), '<html>');
  assert.equal(headerValue([{ name: 'Content-Type', value: 'x' }], 'content-type'), 'x');
});
