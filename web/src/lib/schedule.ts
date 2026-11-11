// Pure helpers for schedules: cron presets, human-readable descriptions,
// time-zone formatting and the load-override form. No DOM access, so they
// run under `node --test` (see test/schedule.test.mjs).
import type { LoadOverride, Schedule, ScheduleKind, ScheduleRequest, ScheduleStatus } from '../api/types';
import { parseDuration, stagesToProfile, type FormStage, type LoadModel, type ProfilePoint } from './scenario.ts';

// ---------- recurrence presets <-> cron ----------

export type Preset = 'daily' | 'weekdays' | 'weekly' | 'cron';

export interface Recurrence {
  preset: Preset;
  /** "HH:MM" (daily / weekdays / weekly). */
  time: string;
  /** Weekdays for "weekly", 0 = Sunday .. 6 = Saturday, ascending. */
  days: number[];
  /** Raw expression for "cron". */
  cron: string;
}

export const DAY_SHORT = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
/** Monday-first order for weekday chips. */
export const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0];

const pad2 = (n: number) => String(n).padStart(2, '0');

/** Parses "HH:MM" (24 h). */
export function parseTime(t: string): { h: number; m: number } | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(t.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 23 || min > 59) return null;
  return { h, m: min };
}

function sortDays(days: number[]): number[] {
  return [...new Set(days.map((d) => (d === 7 ? 0 : d)))].sort((a, b) => a - b);
}

/** Builds the cron expression of a recurrence ("" when incomplete). */
export function buildCron(r: Recurrence): string {
  if (r.preset === 'cron') return r.cron.trim().split(/\s+/).join(' ');
  const t = parseTime(r.time);
  if (!t) return '';
  const head = `${t.m} ${t.h} * *`;
  if (r.preset === 'daily') return `${head} *`;
  if (r.preset === 'weekdays') return `${head} 1-5`;
  const days = sortDays(r.days);
  return days.length ? `${head} ${days.join(',')}` : '';
}

/** Recognises the cron shapes the presets produce; anything else is "cron". */
export function cronToRecurrence(expr: string): Recurrence {
  const cron = (expr ?? '').trim().split(/\s+/).join(' ');
  const m = /^(\d{1,2}) (\d{1,2}) \* \* (\*|1-5|[0-7](?:,[0-7])*)$/.exec(cron);
  if (m) {
    const min = Number(m[1]);
    const h = Number(m[2]);
    if (min <= 59 && h <= 23) {
      const time = `${pad2(h)}:${pad2(min)}`;
      if (m[3] === '*') return { preset: 'daily', time, days: [], cron };
      if (m[3] === '1-5') return { preset: 'weekdays', time, days: [1, 2, 3, 4, 5], cron };
      return { preset: 'weekly', time, days: sortDays(m[3].split(',').map(Number)), cron };
    }
  }
  return { preset: 'cron', time: '03:00', days: [], cron };
}

/** Short English description of a cron expression. */
export function describeCron(expr: string): string {
  const r = cronToRecurrence(expr);
  if (r.preset === 'daily') return `Daily at ${r.time}`;
  if (r.preset === 'weekdays') return `Weekdays at ${r.time}`;
  if (r.preset === 'weekly') {
    const d = r.days;
    if (d.length === 7) return `Daily at ${r.time}`;
    if (d.length === 2 && d[0] === 0 && d[1] === 6) return `Weekends at ${r.time}`;
    if (d.length === 1) return `${DAY_SHORT[d[0]]}days at ${r.time}`;
    const order = WEEK_ORDER.filter((x) => d.includes(x));
    return `${order.map((x) => DAY_SHORT[x]).join(', ')} at ${r.time}`;
  }
  const c = r.cron;
  const f = c.split(' ');
  if (c === '* * * * *') return 'Every minute';
  if (f.length === 5 && f.slice(2).join(' ') === '* * *') {
    const every = /^\*\/(\d+)$/.exec(f[0]);
    if (every && f[1] === '*') return `Every ${every[1]} minutes`;
    if (/^\d{1,2}$/.test(f[0]) && f[1] === '*') return `Hourly at :${pad2(Number(f[0]))}`;
    const hours = /^\*\/(\d+)$/.exec(f[1]);
    if (/^\d{1,2}$/.test(f[0]) && hours) return `Every ${hours[1]} hours at :${pad2(Number(f[0]))}`;
  }
  const macros: Record<string, string> = {
    '@hourly': 'Hourly at :00',
    '@daily': 'Daily at 00:00',
    '@midnight': 'Daily at 00:00',
    '@weekly': 'Sundays at 00:00',
    '@monthly': 'Monthly on the 1st at 00:00',
    '@yearly': 'Yearly on 1 Jan at 00:00',
    '@annually': 'Yearly on 1 Jan at 00:00',
  };
  return macros[c.toLowerCase()] ?? `Cron ${c}`;
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/** "2026-11-12T03:00" -> "12 Nov 2026, 03:00". */
export function fmtAt(at: string | undefined): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(at ?? '');
  if (!m) return at ?? '';
  return `${Number(m[3])} ${MONTHS[Number(m[2]) - 1] ?? m[2]} ${m[1]}, ${m[4]}:${m[5]}`;
}

/** e.g. "Weekdays at 03:00 Europe/Istanbul", "Once · 12 Nov 2026, 03:00 UTC". */
export function describeWhen(s: { kind: ScheduleKind; at?: string; cron?: string; timezone: string }): string {
  const tz = s.timezone || 'UTC';
  if (s.kind === 'once') return `Once · ${fmtAt(s.at)} ${tz}`;
  return `${describeCron(s.cron ?? '')} ${tz}`;
}

// ---------- time zones ----------

/** The browser's IANA zone (UTC when unknown). */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/** Zones for the picker: every supported zone, plus UTC and extra. */
export function timezoneList(...extra: string[]): string[] {
  let zones: string[] = [];
  try {
    const sv = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    if (sv) zones = sv('timeZone');
  } catch {
    zones = [];
  }
  if (zones.length === 0) {
    zones = ['Europe/Istanbul', 'Europe/London', 'Europe/Berlin', 'America/New_York', 'America/Los_Angeles', 'Asia/Tokyo'];
  }
  const all = new Set([...zones, 'UTC', ...extra.filter(Boolean)]);
  return [...all].sort((a, b) => a.localeCompare(b));
}

/** The wall clock of `date` in zone tz: { date: "YYYY-MM-DD", time: "HH:MM" }. */
export function wallClock(tz: string, date: Date): { date: string; time: string } {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: tz,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(date);
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? '00';
  return { date: `${get('year')}-${get('month')}-${get('day')}`, time: `${get('hour')}:${get('minute')}` };
}

/** Default date/time of a new once schedule: now + 1 h rounded up to 15 min, in tz. */
export function defaultOnce(tz: string, now: number): { date: string; time: string } {
  const q = 15 * 60_000;
  return wallClock(tz, new Date(Math.ceil((now + 3_600_000) / q) * q));
}

/** An instant shown in zone tz, e.g. "Wed 11 Nov 2026, 03:00 GMT+3". */
export function fmtInZone(iso: string | undefined, tz: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  try {
    return new Intl.DateTimeFormat('en-GB', {
      timeZone: tz || 'UTC',
      weekday: 'short',
      day: 'numeric',
      month: 'short',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23',
      timeZoneName: 'short',
    }).format(d);
  } catch {
    return d.toISOString();
  }
}

/** "in 3h 12m", "in 45s", "5m ago", "now". */
export function fmtRelative(iso: string | undefined, now: number): string {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '—';
  const diff = Math.round((t - now) / 1000);
  const a = Math.abs(diff);
  if (a < 5) return 'now';
  let txt: string;
  if (a < 60) txt = `${a}s`;
  else if (a < 3600) txt = `${Math.floor(a / 60)}m`;
  else if (a < 86400) {
    const m = Math.floor((a % 3600) / 60);
    txt = `${Math.floor(a / 3600)}h${m ? ` ${m}m` : ''}`;
  } else {
    const h = Math.floor((a % 86400) / 3600);
    txt = `${Math.floor(a / 86400)}d${h ? ` ${h}h` : ''}`;
  }
  return diff > 0 ? `in ${txt}` : `${txt} ago`;
}

// ---------- load override form ----------

export type LoadMode = 'keep' | 'scale' | 'profile';
export type LoadUnit = 'users' | 'rate';

export interface LoadForm {
  mode: LoadMode;
  unit: LoadUnit;
  target: string;
  rampUp: string;
  hold: string;
  rampDown: string;
}

/** Milliseconds -> Go duration text ("1m30s", "10m", "0s"). */
export function fmtGoDuration(ms: number | undefined): string {
  const total = Math.max(0, Math.round((ms ?? 0) / 1000));
  if (total === 0) return (ms ?? 0) > 0 ? `${Math.round(ms ?? 0)}ms` : '0s';
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return `${h ? `${h}h` : ''}${m ? `${m}m` : ''}${s ? `${s}s` : ''}`;
}

export function unitForModel(model: LoadModel): LoadUnit {
  return model === 'closed' ? 'users' : 'rate';
}

export function defaultLoadForm(model: LoadModel, peak: number): LoadForm {
  return { mode: 'keep', unit: unitForModel(model), target: String(peak || 10), rampUp: '30s', hold: '5m', rampDown: '0s' };
}

export function loadFormFromOverride(l: LoadOverride | undefined, model: LoadModel, peak: number): LoadForm {
  const f = defaultLoadForm(model, peak);
  if (!l) return f;
  f.mode = l.mode;
  f.unit = l.users ? 'users' : 'rate';
  f.target = String(l.users || l.rate || '');
  if (l.mode === 'profile') {
    f.rampUp = fmtGoDuration(l.ramp_up_ms);
    f.hold = fmtGoDuration(l.hold_ms);
    f.rampDown = fmtGoDuration(l.ramp_down_ms);
  }
  return f;
}

/**
 * Converts the form into the API override. errors is keyed like the
 * server's details ("load.users", "load.hold_ms", ...).
 */
export function buildLoadOverride(f: LoadForm, model: LoadModel): { load?: LoadOverride; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  if (f.mode === 'keep') return { errors };
  const key = f.unit === 'users' ? 'load.users' : 'load.rate';
  const n = Number(f.target);
  if (!f.target.trim() || !Number.isInteger(n) || n < 1) {
    errors[key] =
      f.unit === 'users' ? 'Enter a whole number of virtual users (>= 1)' : 'Enter a whole number of iterations per second (>= 1)';
  }
  if (f.mode === 'scale' && f.unit !== unitForModel(model)) {
    errors[key] =
      model === 'closed'
        ? 'This scenario uses virtual users (closed model); scale it with users or use Ramp & hold'
        : 'This scenario uses an arrival rate (open model); scale it with it/s or use Ramp & hold';
  }
  const load: LoadOverride = { mode: f.mode };
  if (f.unit === 'users') load.users = n;
  else load.rate = n;
  if (f.mode === 'profile') {
    const up = parseDuration(f.rampUp || '0s');
    const hold = parseDuration(f.hold);
    const down = parseDuration(f.rampDown || '0s');
    if (!Number.isFinite(up) || up < 0) errors['load.ramp_up_ms'] = 'Use a duration like 30s or 2m';
    if (!Number.isFinite(hold) || hold < 1) errors['load.hold_ms'] = 'Hold for at least 1s (e.g. 10m)';
    if (!Number.isFinite(down) || down < 0) errors['load.ramp_down_ms'] = 'Use a duration like 30s or 0s';
    load.ramp_up_ms = Math.round(up * 1000);
    load.hold_ms = Math.round(hold * 1000);
    load.ramp_down_ms = Math.round(down * 1000);
  }
  return Object.keys(errors).length ? { errors } : { load, errors };
}

/**
 * Load profile of the scheduled run (for the preview chart), mirroring the
 * server's ApplyLoad: keep = scenario stages; scale = stages scaled to the
 * new peak (rounded, a positive stage stays >= 1); profile = ramp/hold/down
 * (ramp-up 0 = 1 s).
 */
/**
 * Ramp that runs for a profile override with ramp_up 0: the engine needs
 * stage durations > 0, so "start at full load" is a 1 s ramp. Must match
 * instantRamp in internal/scheduler/override.go.
 */
export const INSTANT_RAMP_MS = 1000;

/** Ramp-up that actually runs: ms rounded like the server, 0 -> INSTANT_RAMP_MS. */
export function effectiveRampUpMs(ms: number | undefined): number {
  return Math.round(ms ?? 0) > 0 ? Math.round(ms ?? 0) : INSTANT_RAMP_MS;
}

export function overrideProfile(
  f: LoadForm,
  scenario: { model: LoadModel; stages: FormStage[] } | null,
): { points: ProfilePoint[]; model: LoadModel; total: number; peak: number } | null {
  const target = Number(f.target);
  if (f.mode === 'profile') {
    if (!Number.isFinite(target) || target < 1) return null;
    let up = parseDuration(f.rampUp || '0s');
    const hold = parseDuration(f.hold);
    const down = parseDuration(f.rampDown || '0s');
    if (![up, hold, down].every(Number.isFinite) || hold <= 0) return null;
    up = effectiveRampUpMs(up * 1000) / 1000;
    const points: ProfilePoint[] = [
      { t: 0, target: 0 },
      { t: up, target },
      { t: up + hold, target },
    ];
    if (down > 0) points.push({ t: up + hold + down, target: 0 });
    const model: LoadModel = f.unit === 'users' ? 'closed' : 'open';
    return { points, model, total: points[points.length - 1].t, peak: target };
  }
  if (!scenario) return null;
  let stages = scenario.stages;
  if (f.mode === 'scale' && Number.isFinite(target) && target >= 1) {
    const peak = stages.reduce((mx, s) => Math.max(mx, Number(s.target) || 0), 0);
    if (peak > 0) {
      stages = stages.map((s) => {
        const t = Number(s.target) || 0;
        return { duration: s.duration, target: String(t > 0 ? Math.max(1, Math.round((t * target) / peak)) : 0) };
      });
    }
  }
  const p = stagesToProfile(stages);
  return { points: p.points, model: scenario.model, total: p.total, peak: p.peak };
}

// ---------- server validation details ----------

/** Groups "field: message" details by field ("" for unkeyed ones). */
export function fieldErrors(details: string[]): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const d of details) {
    const m = /^([a-z_][a-z0-9_.[\]]*): (.+)$/.exec(d);
    const key = m ? m[1] : '';
    const msg = m ? m[2] : d;
    (out[key] ??= []).push(msg);
  }
  return out;
}

/** Messages for key and its sub-keys ("load" matches "load.users"). */
export function errorsFor(fe: Record<string, string[]>, ...keys: string[]): string[] {
  const out: string[] = [];
  for (const [k, v] of Object.entries(fe)) {
    if (keys.some((key) => k === key || k.startsWith(`${key}.`))) out.push(...v);
  }
  return out;
}

export function statusTone(s: ScheduleStatus | undefined): string {
  switch (s) {
    case 'started':
      return 'good';
    case 'queued':
      return 'info';
    case 'skipped':
    case 'missed':
      return 'warn';
    case 'error':
      return 'bad';
    default:
      return '';
  }
}

/** Short description of a load override ("Scenario load" when none). */
export function describeLoad(l: LoadOverride | undefined): string {
  if (!l) return 'Scenario load';
  const peak = l.users ? `${l.users} VUs` : `${l.rate ?? 0} it/s`;
  if (l.mode === 'scale') return `Peak ${peak} (scaled)`;
  const parts = [`ramp ${fmtGoDuration(effectiveRampUpMs(l.ramp_up_ms))}`, `hold ${fmtGoDuration(l.hold_ms)}`];
  if (l.ramp_down_ms) parts.push(`down ${fmtGoDuration(l.ramp_down_ms)}`);
  return `${peak} · ${parts.join(', ')}`;
}

// ---------- test / suite target ----------

export type ScheduleTarget = 'test' | 'suite';

/** What a schedule starts: a suite when it has a suite_id, else a test. */
export function targetOf(s: Pick<Schedule, 'suite_id'>): ScheduleTarget {
  return s.suite_id ? 'suite' : 'test';
}

/** The target fields of a request: exactly one of test_id / suite_id. */
export function targetFields(target: ScheduleTarget, testId: string, suiteId: string): Pick<ScheduleRequest, 'test_id' | 'suite_id'> {
  return target === 'suite' ? { suite_id: suiteId } : { test_id: testId };
}

/** Load column of the schedules table: suites carry per-item load. */
export function describeScheduleLoad(s: Pick<Schedule, 'suite_id' | 'load'>): string {
  return s.suite_id ? 'Per test in the suite' : describeLoad(s.load);
}

/** Link to what a schedule's last "started" result points at (a run or a suite run). */
export function lastRunLink(s: Pick<Schedule, 'suite_id' | 'last_run_id' | 'last_status'>): { to: string; label: string } | null {
  if (!s.last_run_id || s.last_status !== 'started') return null;
  return s.suite_id ? { to: `/suite-runs/${s.last_run_id}`, label: 'view suite run' } : { to: `/runs/${s.last_run_id}`, label: 'view run' };
}

/** Where run-now leads: the run page, or the suite run page for `{suite_run}`. */
export function runNowPath(res: { id: string } | { suite_run: { id: string } }): string {
  return 'suite_run' in res ? `/suite-runs/${res.suite_run.id}` : `/runs/${res.id}`;
}
