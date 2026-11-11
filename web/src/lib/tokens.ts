// Pure helpers for API tokens, permissions and "started by" labels.

export type Scope = 'read' | 'run' | 'write' | 'admin';

export const SCOPES: Scope[] = ['read', 'run', 'write', 'admin'];

export const SCOPE_INFO: Record<Scope, { label: string; description: string }> = {
  read: {
    label: 'Read',
    description: 'View tests, runs, reports and schedules. Cannot change or start anything.',
  },
  run: {
    label: 'Run',
    description: 'Read, try requests, start and stop runs and suites, run schedules now. Cannot edit tests.',
  },
  write: {
    label: 'Write',
    description: 'Run, plus create, edit and delete tests, suites, schedules, notifications, baselines and pins.',
  },
  admin: {
    label: 'Admin',
    description: 'Everything, including managing API tokens.',
  },
};

/** Recommended presets shown next to the scope picker. */
export const PRESETS: { label: string; scope: Scope }[] = [
  { label: 'AI agent (Kiro, Claude Code)', scope: 'run' },
  { label: 'CI pipeline', scope: 'run' },
  { label: 'Dashboard / read-only viewer', scope: 'read' },
];

export const EXPIRY_OPTIONS: { days: number; label: string }[] = [
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
  { days: 365, label: '1 year' },
  { days: 0, label: 'Never' },
];
export const DEFAULT_EXPIRY_DAYS = 90;

export const AUTH_DISABLED_MESSAGE = 'API tokens need serve -token (auth is disabled)';

const RANK: Record<Scope, number> = { read: 0, run: 1, write: 2, admin: 3 };

/** True when `have` is at least as powerful as `need`. */
export function scopeAllows(have: Scope | undefined | null, need: Scope): boolean {
  if (!have) return false;
  return RANK[have] >= RANK[need];
}

export interface TokenForm {
  name: string;
  scope: Scope;
  expiresDays: number;
}

export function emptyTokenForm(): TokenForm {
  return { name: '', scope: 'run', expiresDays: DEFAULT_EXPIRY_DAYS };
}

/** Field errors keyed by field name. */
export function validateTokenForm(f: TokenForm): Record<string, string> {
  const errs: Record<string, string> = {};
  const name = f.name.trim();
  if (!name) errs.name = 'Name is required';
  else if (name.length > 100) errs.name = 'Name must be at most 100 characters';
  if (!SCOPES.includes(f.scope)) errs.scope = 'Choose a scope';
  if (!Number.isInteger(f.expiresDays) || f.expiresDays < 0 || f.expiresDays > 365) {
    errs.expires = 'Expiry must be 0 (never) to 365 days';
  }
  return errs;
}

export function tokenRequest(f: TokenForm): { name: string; scope: Scope; expires_in_days?: number } {
  const req: { name: string; scope: Scope; expires_in_days?: number } = { name: f.name.trim(), scope: f.scope };
  if (f.expiresDays > 0) req.expires_in_days = f.expiresDays;
  return req;
}

/** "glt_abcd…" */
export function fmtPrefix(prefix: string): string {
  return `${prefix}…`;
}

export function isExpired(expiresAt: string | undefined | null, now = Date.now()): boolean {
  if (!expiresAt) return false;
  const t = Date.parse(expiresAt);
  return Number.isFinite(t) && t <= now;
}

/** Expiry cell text: "never", "expired" or "in 12d". */
export function expiryLabel(expiresAt: string | undefined | null, now = Date.now()): string {
  if (!expiresAt) return 'never';
  const t = Date.parse(expiresAt);
  if (!Number.isFinite(t)) return 'never';
  if (t <= now) return 'expired';
  const d = (t - now) / 1000;
  if (d < 3600) return `in ${Math.max(1, Math.ceil(d / 60))}m`;
  if (d < 86400) return `in ${Math.floor(d / 3600)}h`;
  return `in ${Math.floor(d / 86400)}d`;
}

/** Last-used cell text; "never" when the token was not used yet. */
export function lastUsedLabel(lastUsedAt: string | undefined | null, ago: (iso: string) => string): string {
  return lastUsedAt ? ago(lastUsedAt) : 'never';
}

/** Formats started_by: "token:kiro" -> "kiro (token)". Empty -> "". */
export function formatStartedBy(by: string | undefined | null): string {
  const s = (by ?? '').trim();
  if (!s) return '';
  const i = s.indexOf(':');
  if (i < 0) return s;
  const kind = s.slice(0, i);
  const name = s.slice(i + 1).trim();
  if ((kind === 'token' || kind === 'schedule') && name) return `${name} (${kind})`;
  return s;
}

/** Prefixes a 403 message so it reads as a permission problem. */
export function forbiddenMessage(message: string): string {
  const m = message.trim();
  return m.toLowerCase().startsWith('not allowed') ? m : `Not allowed: ${m}`;
}

/** Snippet for registering the MCP server with the new token. */
export function mcpSnippet(origin: string): string {
  return `GOLOAD_TOKEN=<token> goloadtest mcp --server ${origin}`;
}

/** Snippet for a CI pipeline. */
export function ciSnippet(origin: string): string {
  return `# CI secret: GOLOAD_TOKEN\ncurl -fsS -H "Authorization: Bearer $GOLOAD_TOKEN" ${origin}/api/health`;
}
