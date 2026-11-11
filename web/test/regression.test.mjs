// Scenario `regression` key: YAML <-> form round trip, Quick mode fallback,
// and the label of baseline rows on the run page.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parse } from 'yaml';
import { formToQuick } from '../src/lib/quick.ts';
import { isRegression, regressionRule } from '../src/lib/regression.ts';
import { formToYaml, yamlToForm } from '../src/lib/scenario.ts';

const base = `name: t
target:
  base_url: "https://x.test"
load: {model: open, stages: [{duration: 30s, target: 5}, {duration: 1m, target: 5}]}
steps:
  - {name: s, method: GET, path: /a}
`;
const withRules = `${base}regression:
  - {metric: p95_ms, max_increase_pct: 20}
  - {metric: error_rate, max_increase: 0.01}
  - {metric: rps, max_decrease_pct: 10}
  - {metric: mean_ms, max_decrease: 3}
`;

test('regression rules round trip through the form', () => {
  const { form, error } = yamlToForm(withRules);
  assert.equal(error, null);
  assert.equal(form.regression.length, 4);
  assert.deepEqual(form.regression[0], { metric: 'p95_ms', kind: 'max_increase_pct', value: '20' });
  const out = parse(formToYaml(form));
  assert.deepEqual(out.regression, [
    { metric: 'p95_ms', max_increase_pct: 20 },
    { metric: 'error_rate', max_increase: 0.01 },
    { metric: 'rps', max_decrease_pct: 10 },
    { metric: 'mean_ms', max_decrease: 3 },
  ]);
});

test('no regression key is emitted when there are no rules', () => {
  const { form } = yamlToForm(base);
  assert.deepEqual(form.regression, []);
  assert.equal('regression' in parse(formToYaml(form)), false);
});

test('regression rules make the scenario not Quick-compatible', () => {
  const { form } = yamlToForm(withRules);
  const r = formToQuick(form);
  assert.ok(JSON.stringify(r).toLowerCase().includes('regression'));
});

test('baseline row label', () => {
  const t = { metric: 'p95_ms vs baseline', op: 'lte', value: 54, actual: 50, passed: true, baseline: 45 };
  assert.equal(isRegression(t), true);
  assert.equal(regressionRule(t), '≤ 54.0 ms (baseline 45.0 ms, +20%)');
  assert.equal(isRegression({ metric: 'rps', op: 'gte', value: 1, actual: 1, passed: true }), false);
  const g = { metric: 'rps vs baseline', op: 'gte', value: 90, actual: 95, passed: true, baseline: 100 };
  assert.match(regressionRule(g), /^≥ 90.* req\/s \(baseline 100.* req\/s, -10%\)$/);
});
