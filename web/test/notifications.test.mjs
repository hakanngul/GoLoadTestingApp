// Tests for the notification-channel helpers (src/lib/notifications.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  channelToForm,
  describeEvents,
  emptyNotificationForm,
  formToRequest,
  toggleEvent,
  toggleRequest,
  urlError,
  validateForm,
} from '../src/lib/notifications.ts';

const channel = {
  id: 'c1',
  name: 'ops',
  kind: 'slack',
  url_masked: 'https://hooks.slack.com/…a1b2',
  events: ['run_finished'],
  only_failures: true,
  enabled: true,
  created_at: '2025-01-01T00:00:00Z',
  updated_at: '2025-01-01T00:00:00Z',
};

test('urlError requires https, allows http only for localhost', () => {
  assert.equal(urlError('https://hooks.slack.com/services/T/B/x'), '');
  assert.equal(urlError(' https://example.com/x '), '');
  assert.equal(urlError('http://localhost:18202/x'), '');
  assert.equal(urlError('http://127.0.0.1/x'), '');
  assert.notEqual(urlError('http://example.com/x'), '');
  assert.notEqual(urlError('ftp://example.com/x'), '');
  assert.notEqual(urlError('example.com/x'), '');
  assert.notEqual(urlError(''), '');
});

test('validateForm: URL is optional only when editing', () => {
  const f = { ...emptyNotificationForm(), name: 'ops' };
  assert.deepEqual(Object.keys(validateForm(f, false)), ['url']);
  assert.deepEqual(validateForm(f, true), {});
  assert.ok(validateForm({ ...f, url: 'http://example.com' }, true).url);
  assert.ok(validateForm({ ...f, name: '  ', url: 'https://x.test/y' }, false).name);
  assert.ok(validateForm({ ...f, name: 'x'.repeat(101), url: 'https://x.test/y' }, false).name);
  assert.ok(validateForm({ ...f, url: 'https://x.test/y', events: [] }, false).events);
  assert.deepEqual(validateForm({ ...f, url: 'https://x.test/y' }, false), {});
});

test('toggleEvent keeps canonical order', () => {
  assert.deepEqual(toggleEvent(['suite_finished'], 'run_finished'), ['run_finished', 'suite_finished']);
  assert.deepEqual(toggleEvent(['run_finished', 'suite_finished'], 'run_finished'), ['suite_finished']);
  assert.deepEqual(toggleEvent([], 'suite_finished'), ['suite_finished']);
});

test('describeEvents', () => {
  assert.equal(describeEvents(['suite_finished', 'run_finished']), 'Run finished, Suite finished');
  assert.equal(describeEvents([]), 'none');
});

test('channelToForm never carries a URL; formToRequest trims', () => {
  assert.equal(channelToForm(channel).url, '');
  assert.equal(channelToForm(channel).only_failures, true);
  const req = formToRequest({ ...channelToForm(channel), name: ' ops ', url: ' https://x.test/y ' });
  assert.equal(req.name, 'ops');
  assert.equal(req.url, 'https://x.test/y');
});

test('toggleRequest flips enabled and sends no URL', () => {
  const req = toggleRequest(channel);
  assert.equal(req.enabled, false);
  assert.equal(req.url, '');
  assert.deepEqual(req.events, ['run_finished']);
});
