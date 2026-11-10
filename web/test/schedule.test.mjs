// Tests for the schedule helpers (src/lib/schedule.ts).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  buildCron,
  buildLoadOverride,
  cronToRecurrence,
  defaultOnce,
  describeCron,
  describeWhen,
  errorsFor,
  fieldErrors,
  fmtAt,
  fmtGoDuration,
  fmtInZone,
  fmtRelative,
  loadFormFromOverride,
  overrideProfile,
  parseTime,
  timezoneList,
  wallClock,
} from '../src/lib/schedule.ts';

test('buildCron from presets', () => {
  assert.equal(buildCron({ preset: 'daily', time: '03:00', days: [], cron: '' }), '0 3 * * *');
  assert.equal(buildCron({ preset: 'weekdays', time: '09:05', days: [], cron: '' }), '5 9 * * 1-5');
  assert.equal(buildCron({ preset: 'weekly', time: '22:30', days: [5, 1, 3, 1], cron: '' }), '30 22 * * 1,3,5');
  assert.equal(buildCron({ preset: 'weekly', time: '22:30', days: [], cron: '' }), '');
  assert.equal(buildCron({ preset: 'daily', time: '25:00', days: [], cron: '' }), '');
  assert.equal(buildCron({ preset: 'cron', time: '', days: [], cron: '  */5   * * * * ' }), '*/5 * * * *');
});

test('cronToRecurrence round-trips presets and falls back to cron', () => {
  for (const r of [
    { preset: 'daily', time: '03:00', days: [] },
    { preset: 'weekdays', time: '09:05', days: [1, 2, 3, 4, 5] },
    { preset: 'weekly', time: '22:30', days: [0, 3, 6] },
  ]) {
    const cron = buildCron({ ...r, cron: '' });
    const back = cronToRecurrence(cron);
    assert.equal(back.preset, r.preset);
    assert.equal(back.time, r.time);
    if (r.preset === 'weekly') assert.deepEqual(back.days, r.days);
  }
  assert.deepEqual(cronToRecurrence('0 0 * * 7').days, [0]);
  for (const c of ['*/5 * * * *', '0 3 1 * *', '0 3 * 1 *', '0 3-5 * * *', '@daily', '61 3 * * *']) {
    assert.equal(cronToRecurrence(c).preset, 'cron', c);
  }
});

test('describe schedules', () => {
  assert.equal(describeWhen({ kind: 'recurring', cron: '0 3 * * 1-5', timezone: 'Europe/Istanbul' }), 'Weekdays at 03:00 Europe/Istanbul');
  assert.equal(describeWhen({ kind: 'once', at: '2026-11-12T03:00', timezone: 'UTC' }), 'Once · 12 Nov 2026, 03:00 UTC');
  assert.equal(describeCron('0 3 * * *'), 'Daily at 03:00');
  assert.equal(describeCron('30 9 * * 1,3,5'), 'Mon, Wed, Fri at 09:30');
  assert.equal(describeCron('30 9 * * 0,1'), 'Mon, Sun at 09:30');
  assert.equal(describeCron('0 10 * * 0,6'), 'Weekends at 10:00');
  assert.equal(describeCron('0 10 * * 1'), 'Mondays at 10:00');
  assert.equal(describeCron('0 10 * * 0-6'), 'Cron 0 10 * * 0-6');
  assert.equal(describeCron('0 10 * * 0,1,2,3,4,5,6'), 'Daily at 10:00');
  assert.equal(describeCron('* * * * *'), 'Every minute');
  assert.equal(describeCron('*/15 * * * *'), 'Every 15 minutes');
  assert.equal(describeCron('5 * * * *'), 'Hourly at :05');
  assert.equal(describeCron('0 */6 * * *'), 'Every 6 hours at :00');
  assert.equal(describeCron('@weekly'), 'Sundays at 00:00');
  assert.equal(describeCron('0 3 1 * *'), 'Cron 0 3 1 * *');
  assert.equal(fmtAt('bogus'), 'bogus');
});

test('parseTime', () => {
  assert.deepEqual(parseTime('7:05'), { h: 7, m: 5 });
  assert.equal(parseTime('24:00'), null);
  assert.equal(parseTime('12:60'), null);
  assert.equal(parseTime('noon'), null);
});

test('time zones', () => {
  const at = new Date('2026-11-10T23:59:30Z');
  assert.deepEqual(wallClock('Europe/Istanbul', at), { date: '2026-11-11', time: '02:59' });
  assert.deepEqual(wallClock('America/New_York', at), { date: '2026-11-10', time: '18:59' });
  assert.deepEqual(wallClock('UTC', new Date('2026-11-10T00:00:00Z')), { date: '2026-11-10', time: '00:00' });
  // now + 1h, rounded up to a quarter hour.
  assert.deepEqual(defaultOnce('UTC', Date.parse('2026-11-10T10:07:00Z')), { date: '2026-11-10', time: '11:15' });
  assert.match(fmtInZone('2026-11-11T00:00:00Z', 'Europe/Istanbul'), /Wed,? 11 Nov 2026,? 03:00 GMT\+3/);
  assert.equal(fmtInZone(undefined, 'UTC'), '—');
  const zones = timezoneList('Mars/Base');
  assert.ok(zones.includes('UTC') && zones.includes('Europe/Istanbul') && zones.includes('Mars/Base'));
  assert.deepEqual([...zones].sort((a, b) => a.localeCompare(b)), zones);
});

test('fmtRelative', () => {
  const now = Date.parse('2026-11-10T12:00:00Z');
  assert.equal(fmtRelative('2026-11-10T12:00:02Z', now), 'now');
  assert.equal(fmtRelative('2026-11-10T12:00:45Z', now), 'in 45s');
  assert.equal(fmtRelative('2026-11-10T15:12:00Z', now), 'in 3h 12m');
  assert.equal(fmtRelative('2026-11-10T11:55:00Z', now), '5m ago');
  assert.equal(fmtRelative('2026-11-12T14:00:00Z', now), 'in 2d 2h');
  assert.equal(fmtRelative(undefined, now), '—');
});

test('fmtGoDuration', () => {
  assert.equal(fmtGoDuration(0), '0s');
  assert.equal(fmtGoDuration(90_000), '1m30s');
  assert.equal(fmtGoDuration(600_000), '10m');
  assert.equal(fmtGoDuration(3_660_000), '1h1m');
  assert.equal(fmtGoDuration(250), '250ms');
});

test('buildLoadOverride', () => {
  const base = { mode: 'keep', unit: 'users', target: '10', rampUp: '30s', hold: '5m', rampDown: '0s' };
  assert.deepEqual(buildLoadOverride(base, 'closed'), { errors: {} });
  assert.deepEqual(buildLoadOverride({ ...base, mode: 'scale', target: '20' }, 'closed').load, { mode: 'scale', users: 20 });
  assert.deepEqual(buildLoadOverride({ ...base, mode: 'scale', unit: 'rate', target: '50' }, 'open').load, {
    mode: 'scale',
    rate: 50,
  });
  // Scaling with the other unit is refused (the server would too).
  assert.ok(buildLoadOverride({ ...base, mode: 'scale', target: '20' }, 'open').errors['load.users']);
  assert.deepEqual(buildLoadOverride({ ...base, mode: 'profile', target: '20', rampDown: '10s' }, 'open').load, {
    mode: 'profile',
    users: 20,
    ramp_up_ms: 30000,
    hold_ms: 300000,
    ramp_down_ms: 10000,
  });
  const bad = buildLoadOverride({ ...base, mode: 'profile', target: '0', hold: 'x', rampUp: '-' }, 'closed');
  assert.equal(bad.load, undefined);
  assert.deepEqual(Object.keys(bad.errors).sort((a, b) => a.localeCompare(b)), ['load.hold_ms', 'load.ramp_up_ms', 'load.users']);
  // Round trip through the form.
  const f = loadFormFromOverride({ mode: 'profile', rate: 40, ramp_up_ms: 0, hold_ms: 90000, ramp_down_ms: 0 }, 'closed', 5);
  assert.deepEqual(f, { mode: 'profile', unit: 'rate', target: '40', rampUp: '0s', hold: '1m30s', rampDown: '0s' });
  assert.deepEqual(loadFormFromOverride(undefined, 'open', 25).mode, 'keep');
});

test('overrideProfile mirrors the server', () => {
  const sc = {
    model: 'closed',
    stages: [
      { duration: '10s', target: '5' },
      { duration: '30s', target: '10' },
      { duration: '10s', target: '0' },
    ],
  };
  const base = { mode: 'keep', unit: 'users', target: '1', rampUp: '0s', hold: '1m', rampDown: '0s' };
  assert.deepEqual(
    overrideProfile(base, sc).points.map((p) => p.target),
    [0, 5, 10, 0],
  );
  assert.deepEqual(
    overrideProfile({ ...base, mode: 'scale', target: '1' }, sc).points.map((p) => p.target),
    [0, 1, 1, 0],
  );
  const p = overrideProfile({ ...base, mode: 'profile', unit: 'rate', target: '50', rampUp: '0s', hold: '1m', rampDown: '30s' }, sc);
  assert.equal(p.model, 'open');
  assert.deepEqual(p.points, [
    { t: 0, target: 0 },
    { t: 1, target: 50 },
    { t: 61, target: 50 },
    { t: 91, target: 0 },
  ]);
  assert.equal(p.total, 91);
  assert.equal(overrideProfile({ ...base, mode: 'profile', hold: '' }, sc), null);
  assert.equal(overrideProfile(base, null), null);
});

test('fieldErrors / errorsFor', () => {
  const fe = fieldErrors(['name: must be between 1 and 200 characters', 'load.users: bad', 'load: set users', 'something odd']);
  assert.deepEqual(fe.name, ['must be between 1 and 200 characters']);
  assert.deepEqual(errorsFor(fe, 'load'), ['bad', 'set users']);
  assert.deepEqual(errorsFor(fe, 'load.users'), ['bad']);
  assert.deepEqual(fe[''], ['something odd']);
  assert.deepEqual(errorsFor(fe, 'cron'), []);
});

test('describeLoad', async () => {
  const { describeLoad } = await import('../src/lib/schedule.ts');
  assert.equal(describeLoad(undefined), 'Scenario load');
  assert.equal(describeLoad({ mode: 'scale', users: 20 }), 'Peak 20 VUs (scaled)');
  assert.equal(
    describeLoad({ mode: 'profile', rate: 50, ramp_up_ms: 30000, hold_ms: 600000, ramp_down_ms: 10000 }),
    '50 it/s · ramp 30s, hold 10m, down 10s',
  );
  assert.equal(describeLoad({ mode: 'profile', users: 2, ramp_up_ms: 0, hold_ms: 20000 }), '2 VUs · ramp 0s, hold 20s');
});
