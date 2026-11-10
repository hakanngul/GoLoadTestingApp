export const dash = '—';

function finite(v: number | undefined | null): v is number {
  return typeof v === 'number' && Number.isFinite(v);
}

export function fmtMs(v: number | undefined | null): string {
  if (!finite(v)) return dash;
  if (v >= 10_000) return `${(v / 1000).toFixed(1)} s`;
  if (v >= 1000) return `${(v / 1000).toFixed(2)} s`;
  if (v >= 100) return `${v.toFixed(0)} ms`;
  if (v >= 10) return `${v.toFixed(1)} ms`;
  return `${v.toFixed(2)} ms`;
}

export function fmtPct(fraction: number | undefined | null, digits = 2): string {
  if (!finite(fraction)) return dash;
  const p = fraction * 100;
  if (p === 0) return '0%';
  if (p < 0.01) return '<0.01%';
  return `${p.toFixed(p >= 10 ? 1 : digits)}%`;
}

export function fmtInt(v: number | undefined | null): string {
  if (!finite(v)) return dash;
  return Math.round(v).toLocaleString('en-US');
}

export function fmtNum(v: number | undefined | null, digits = 1): string {
  if (!finite(v)) return dash;
  return v.toLocaleString('en-US', { maximumFractionDigits: digits, minimumFractionDigits: digits });
}

export function fmtRps(v: number | undefined | null): string {
  if (!finite(v)) return dash;
  return v >= 100 ? fmtInt(v) : fmtNum(v, 1);
}

export function fmtCompact(v: number): string {
  if (Math.abs(v) >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (Math.abs(v) >= 10_000) return `${Math.round(v / 1000)}k`;
  if (Math.abs(v) >= 1000) return `${(v / 1000).toFixed(1)}k`;
  return String(Math.round(v * 100) / 100);
}

/** Seconds -> "1h 2m", "3m 20s", "45s". */
export function fmtSeconds(s: number | undefined | null): string {
  if (!finite(s)) return dash;
  const total = Math.max(0, Math.round(s));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const sec = total % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return sec ? `${m}m ${sec}s` : `${m}m`;
  return `${sec}s`;
}

/** Seconds -> "mm:ss" (or "h:mm:ss"). */
export function fmtClock(s: number): string {
  const total = Math.max(0, Math.floor(s));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const sec = total % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${pad(m)}:${pad(sec)}`;
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export function timeAgo(iso: string | undefined | null, now = Date.now()): string {
  if (!iso) return dash;
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return dash;
  const d = Math.max(0, (now - t) / 1000);
  if (d < 10) return 'just now';
  if (d < 60) return `${Math.floor(d)}s ago`;
  if (d < 3600) return `${Math.floor(d / 60)}m ago`;
  if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
  if (d < 86400 * 30) return `${Math.floor(d / 86400)}d ago`;
  return new Date(t).toLocaleDateString();
}

export function fmtDateTime(iso: string | undefined | null): string {
  if (!iso) return dash;
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return dash;
  return t.toLocaleString('en-GB', {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

export function fmtTime(iso: string | undefined | null): string {
  if (!iso) return dash;
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return dash;
  return t.toLocaleTimeString('en-GB');
}

/** Duration of a run in seconds (uses now for unfinished runs). */
export function runDurationS(startedAt: string, finishedAt?: string): number {
  const a = Date.parse(startedAt);
  const b = finishedAt ? Date.parse(finishedAt) : Date.now();
  if (!Number.isFinite(a) || !Number.isFinite(b)) return NaN;
  return Math.max(0, (b - a) / 1000);
}

export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}
