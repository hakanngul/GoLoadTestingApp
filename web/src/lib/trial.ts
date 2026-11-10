// Helpers for the "Test request" panel (POST /api/tests/try).
//
// Pure module (no React/DOM) so `node --test` can exercise it directly.
import type { TryFix, TryHint, TryResult, TryStep } from '../api/types.ts';
import type { ScenarioForm } from './scenario.ts';

/**
 * Returns a copy of form with fix applied, or null when the fix does not
 * apply (e.g. the step no longer exists).
 */
export function applyFix(form: ScenarioForm, fix: TryFix): ScenarioForm | null {
  const next: ScenarioForm = structuredClone(form);
  switch (fix.kind) {
    case 'base_url':
      if (!/^https?:\/\/[^/]+/i.test(fix.value)) return null;
      next.target.base_url = fix.value;
      return next;
    case 'step_path':
      if (fix.step === -1) {
        if (next.auth.type !== 'login') return null;
        next.auth.login.path = fix.value;
        return next;
      }
      if (fix.step < 0 || fix.step >= next.steps.length) return null;
      next.steps[fix.step].path = fix.value;
      return next;
  }
  return null;
}

export type BadgeTone = 'good' | 'bad' | 'warn' | 'info' | '';

/** Badge text and tone for a step row. */
export function stepBadge(s: TryStep): { label: string; tone: BadgeTone } {
  if (s.state === 'not_run') return { label: 'not run', tone: '' };
  if (s.status === 0) return { label: s.state === 'failed' ? 'error' : 'ok', tone: s.state === 'failed' ? 'bad' : 'good' };
  const label = String(s.status);
  if (s.state === 'failed') return { label, tone: 'bad' };
  if (s.status >= 300 && s.status < 400 && s.status !== 304) return { label, tone: 'warn' };
  return { label, tone: 'good' };
}

/** Headline for the result: "All 4 steps passed" / "Failed at step 2 of 4: home". */
export function summary(r: TryResult): { tone: 'good' | 'bad'; text: string } {
  const n = r.steps.length;
  if (r.ok) return { tone: 'good', text: `All ${n} step${n === 1 ? '' : 's'} passed` };
  const i = r.steps.findIndex((s) => s.state === 'failed');
  if (i >= 0) return { tone: 'bad', text: `Failed at step ${i + 1} of ${n}: ${r.steps[i].name}` };
  return { tone: 'bad', text: 'Not run' };
}

const LEVEL_ORDER: Record<TryHint['level'], number> = { error: 0, warning: 1, info: 2 };

/** Hints sorted by severity (stable), errors first. */
export function sortHints(h: TryHint[]): TryHint[] {
  return h
    .map((x, i) => ({ x, i }))
    .sort((a, b) => LEVEL_ORDER[a.x.level] - LEVEL_ORDER[b.x.level] || a.i - b.i)
    .map((p) => p.x);
}

/** "123 ms" / "1.24 s". */
export function fmtMs(ms: number): string {
  if (!Number.isFinite(ms)) return '—';
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`;
  if (ms >= 10) return `${Math.round(ms)} ms`;
  return `${ms.toFixed(1)} ms`;
}

/** Pretty-prints a JSON body; other text is returned unchanged. */
export function prettyBody(body: string, contentType = ''): string {
  const t = body.trim();
  if (!(contentType.includes('json') || t.startsWith('{') || t.startsWith('['))) return body;
  try {
    return JSON.stringify(JSON.parse(t), null, 2);
  } catch {
    return body; // truncated or not JSON after all
  }
}

/** Value of a header (case-insensitive) in a TryHeader list. */
export function headerValue(h: { name: string; value: string }[], name: string): string {
  const n = name.toLowerCase();
  return h.find((x) => x.name.toLowerCase() === n)?.value ?? '';
}
