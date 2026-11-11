// Tests for the API token / permission helpers (src/lib/tokens.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  DEFAULT_EXPIRY_DAYS,
  EXPIRY_OPTIONS,
  SCOPES,
  SCOPE_INFO,
  ciSnippet,
  emptyTokenForm,
  expiryLabel,
  fmtPrefix,
  forbiddenMessage,
  formatStartedBy,
  isExpired,
  lastUsedLabel,
  mcpSnippet,
  scopeAllows,
  tokenRequest,
  validateTokenForm,
} from '../src/lib/tokens.ts';

test('formatStartedBy', () => {
  assert.equal(formatStartedBy('token:kiro-hakan'), 'kiro-hakan (token)');
  assert.equal(formatStartedBy('schedule:Nightly'), 'Nightly (schedule)');
  assert.equal(formatStartedBy('schedule:Nightly: soak'), 'Nightly: soak (schedule)');
  assert.equal(formatStartedBy('admin'), 'admin');
  assert.equal(formatStartedBy(''), '');
  assert.equal(formatStartedBy(undefined), '');
  assert.equal(formatStartedBy(null), '');
  assert.equal(formatStartedBy('token:'), 'token:');
  assert.equal(formatStartedBy('other:x'), 'other:x');
});

test('scopeAllows follows read < run < write < admin', () => {
  assert.equal(scopeAllows('admin', 'read'), true);
  assert.equal(scopeAllows('run', 'run'), true);
  assert.equal(scopeAllows('read', 'run'), false);
  assert.equal(scopeAllows('write', 'admin'), false);
  assert.equal(scopeAllows(undefined, 'read'), false);
});

test('every scope has a description', () => {
  for (const s of SCOPES) assert.ok(SCOPE_INFO[s].description.length > 10, s);
});

test('expiry labels', () => {
  const now = Date.parse('2026-01-01T00:00:00Z');
  assert.equal(expiryLabel(undefined, now), 'never');
  assert.equal(expiryLabel('2025-12-31T00:00:00Z', now), 'expired');
  assert.equal(expiryLabel('2026-01-13T00:00:00Z', now), 'in 12d');
  assert.equal(expiryLabel('2026-01-01T05:30:00Z', now), 'in 5h');
  assert.equal(expiryLabel('2026-01-01T00:20:00Z', now), 'in 20m');
  assert.equal(isExpired('2025-12-31T00:00:00Z', now), true);
  assert.equal(isExpired('2026-02-01T00:00:00Z', now), false);
  assert.equal(isExpired(undefined, now), false);
});

test('lastUsedLabel and prefix', () => {
  assert.equal(
    lastUsedLabel(undefined, () => 'x'),
    'never',
  );
  assert.equal(
    lastUsedLabel('2026-01-01T00:00:00Z', () => '3h ago'),
    '3h ago',
  );
  assert.equal(fmtPrefix('glt_abcd'), 'glt_abcd…');
});

test('token form defaults, validation and request', () => {
  const f = emptyTokenForm();
  assert.equal(f.expiresDays, DEFAULT_EXPIRY_DAYS);
  assert.ok(EXPIRY_OPTIONS.some((o) => o.days === DEFAULT_EXPIRY_DAYS));
  assert.ok(validateTokenForm(f).name);
  const ok = { ...f, name: '  kiro  ' };
  assert.deepEqual(validateTokenForm(ok), {});
  assert.deepEqual(tokenRequest(ok), { name: 'kiro', scope: 'run', expires_in_days: 90 });
  assert.deepEqual(tokenRequest({ ...ok, expiresDays: 0 }), { name: 'kiro', scope: 'run' });
  assert.ok(validateTokenForm({ ...ok, expiresDays: 400 }).expires);
  assert.ok(validateTokenForm({ ...ok, name: 'x'.repeat(101) }).name);
});

test('forbiddenMessage', () => {
  assert.equal(
    forbiddenMessage('this token has scope "read"; "run" is needed'),
    'Not allowed: this token has scope "read"; "run" is needed',
  );
  assert.equal(forbiddenMessage('Not allowed: nope'), 'Not allowed: nope');
});

test('snippets mention the origin and GOLOAD_TOKEN', () => {
  assert.match(mcpSnippet('http://h:8080'), /mcp --server http:\/\/h:8080/);
  assert.match(mcpSnippet('http://h:8080'), /GOLOAD_TOKEN/);
  assert.match(ciSnippet('http://h:8080'), /GOLOAD_TOKEN/);
});
