// Cookies / redirects: YAML round trip of target.cookies,
// target.follow_redirects, target.max_redirects and the per-step
// follow_redirects, in the full form and through Quick mode; and the
// redirect lines of the "Test request" panel.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parse } from 'yaml';
import { defaultQuick, formToQuick, quickToForm } from '../src/lib/quick.ts';
import { defaultForm, defaultTarget, formToYaml, yamlToForm } from '../src/lib/scenario.ts';
import { redirectLines } from '../src/lib/trial.ts';

const fromYaml = (y) => {
  const { form, error } = yamlToForm(y);
  assert.equal(error, null);
  return form;
};

const base = `name: t
target:
  base_url: "https://x.test"
load: {model: open, stages: [{duration: 30s, target: 5}, {duration: 1m, target: 5}]}
steps:
  - {name: s, method: GET, path: /a}
`;

test('defaults: cookies kept, redirects followed, nothing emitted', () => {
  const f = fromYaml(base);
  assert.equal(f.target.cookies, true);
  assert.equal(f.target.follow_redirects, true);
  assert.equal(f.target.max_redirects, '');
  assert.equal(f.steps[0].follow_redirects, undefined);
  const out = parse(formToYaml(f));
  assert.deepEqual(Object.keys(out.target), ['base_url']);
  assert.equal('follow_redirects' in out.steps[0], false);

  // New tests (both editors) start with the defaults and emit nothing.
  const t = defaultTarget('https://h.test');
  assert.equal(t.cookies && t.follow_redirects, true);
  for (const y of [formToYaml(defaultForm()), formToYaml(quickToForm({ ...defaultQuick(), url: 'https://h.test/x' }))]) {
    assert.doesNotMatch(y, /cookies|follow_redirects|max_redirects/);
  }
});

test('non-default values survive the YAML round trip', () => {
  const y = base
    .replace('base_url: "https://x.test"', 'base_url: "https://x.test"\n  cookies: false\n  follow_redirects: false\n  max_redirects: 3')
    .replace('path: /a}', 'path: /a, follow_redirects: true}');
  const f = fromYaml(y);
  assert.equal(f.target.cookies, false);
  assert.equal(f.target.follow_redirects, false);
  assert.equal(f.target.max_redirects, '3');
  assert.equal(f.steps[0].follow_redirects, true);
  const out = parse(formToYaml(f));
  assert.deepEqual(out.target, { base_url: 'https://x.test', cookies: false, follow_redirects: false, max_redirects: 3 });
  assert.equal(out.steps[0].follow_redirects, true);
  // And again: stable.
  assert.deepEqual(parse(formToYaml(fromYaml(formToYaml(f)))), out);

  // Explicit `true` is the default and is dropped; a step's `false` is kept.
  const g = fromYaml(
    base
      .replace('base_url: "https://x.test"', 'base_url: "https://x.test"\n  cookies: true\n  follow_redirects: true')
      .replace('path: /a}', 'path: /a, follow_redirects: false}'),
  );
  const o2 = parse(formToYaml(g));
  assert.deepEqual(Object.keys(o2.target), ['base_url']);
  assert.equal(o2.steps[0].follow_redirects, false);
});

test('the login request keeps follow_redirects', () => {
  const y =
    base +
    `auth:
  type: login
  login:
    path: /login
    follow_redirects: false
    extract: [{var: token, from: json, expr: token}]
`;
  const out = parse(formToYaml(fromYaml(y)));
  assert.equal(out.auth.login.follow_redirects, false);
});

test('toggling the form switches emits and removes the keys', () => {
  const f = fromYaml(base);
  f.target.cookies = false;
  assert.equal(parse(formToYaml(f)).target.cookies, false);
  f.target.cookies = true;
  f.target.follow_redirects = false;
  const t = parse(formToYaml(f)).target;
  assert.equal('cookies' in t, false);
  assert.equal(t.follow_redirects, false);
});

test('Quick mode carries the target switches over and rejects a per-step override', () => {
  const f = fromYaml(base.replace('base_url: "https://x.test"', 'base_url: "https://x.test"\n  cookies: false\n  max_redirects: 5'));
  const { quick, reasons } = formToQuick(f);
  assert.deepEqual(reasons, []);
  const back = quickToForm({ ...quick, url: 'https://x.test/b' }, f);
  assert.equal(back.target.cookies, false);
  assert.equal(back.target.follow_redirects, true);
  assert.equal(back.target.max_redirects, '5');
  assert.equal(parse(formToYaml(back)).target.cookies, false);

  const stepOverride = fromYaml(base.replace('path: /a}', 'path: /a, follow_redirects: false}'));
  assert.ok(formToQuick(stepOverride).reasons.some((r) => /follow_redirects/.test(r)));
});

test('redirectLines shortens URLs on the first request origin', () => {
  const step = {
    url: 'https://www.shop.test/tr/slot',
    redirects: [
      { status: 302, method: 'GET', url: 'https://www.shop.test/tr/slot', location: 'https://www.shop.test/tr/slot', cookies_set: ['sid'] },
      { status: 301, method: 'GET', url: 'https://www.shop.test/tr/slot', location: 'https://cdn.shop.test/x' },
      { status: 307, method: 'POST', url: 'https://cdn.shop.test/x', location: 'https://www.shop.test' },
    ],
  };
  assert.deepEqual(redirectLines(step), [
    { status: 302, text: 'GET /tr/slot → /tr/slot', cookies: ['sid'] },
    { status: 301, text: 'GET /tr/slot → https://cdn.shop.test/x', cookies: [] },
    { status: 307, text: 'POST https://cdn.shop.test/x → /', cookies: [] },
  ]);
  assert.deepEqual(redirectLines({ url: 'https://h.test/' }), []);
});
