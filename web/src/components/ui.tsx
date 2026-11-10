import { useEffect, type ReactNode } from 'react';
import type { RunStatus } from '../api/types';
import { errorText } from '../api/client';

// ---------- icons (inline SVG, 16px, stroke) ----------
const paths: Record<string, ReactNode> = {
  tests: (
    <>
      <path d="M3 3h10v10H3z" />
      <path d="M5.5 6.5h5M5.5 9.5h3" />
    </>
  ),
  runs: <path d="M2 12l3.5-4 3 2.5L14 4" />,
  compare: (
    <>
      <path d="M5 2v12M11 2v12" />
      <path d="M2 5h3M11 11h3" />
    </>
  ),
  workers: (
    <>
      <rect x="2.5" y="3" width="11" height="4" rx="1" />
      <rect x="2.5" y="9" width="11" height="4" rx="1" />
      <path d="M5 5h.01M5 11h.01" />
    </>
  ),
  play: <path d="M5 3.5v9l7-4.5z" fill="currentColor" stroke="none" />,
  stop: <rect x="4" y="4" width="8" height="8" rx="1" fill="currentColor" stroke="none" />,
  edit: <path d="M10.5 2.5l3 3L6 13H3v-3z" />,
  trash: <path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.7 8.5h5.6l.7-8.5" />,
  plus: <path d="M8 3v10M3 8h10" />,
  x: <path d="M4 4l8 8M12 4l-8 8" />,
  check: <path d="M3 8.5l3 3 7-7" />,
  alert: (
    <>
      <path d="M8 2l6.5 11.5h-13z" />
      <path d="M8 6.5v3M8 11.5h.01" />
    </>
  ),
  upload: <path d="M8 11V3M4.5 6.5L8 3l3.5 3.5M3 13h10" />,
  file: <path d="M4 2h5l3 3v9H4z M9 2v3h3" />,
  refresh: <path d="M13 8a5 5 0 11-1.5-3.5M13 2.5V5h-2.5" />,
  up: <path d="M8 13V3M4 7l4-4 4 4" />,
  down: <path d="M8 3v10M4 9l4 4 4-4" />,
  logout: <path d="M6.5 2.5H3v11h3.5M10 5l3 3-3 3M13 8H6" />,
  key: (
    <>
      <circle cx="5.5" cy="10.5" r="3" />
      <path d="M7.6 8.4L13.5 2.5M11 5l2 2" />
    </>
  ),
  cloud: <path d="M4.5 12.5h7a3 3 0 00.4-5.97A4 4 0 004.3 6.6 3 3 0 004.5 12.5z" />,
  reports: (
    <>
      <path d="M2.5 13.5h11" />
      <path d="M4.5 11V8M8 11V4.5M11.5 11V6.5" />
    </>
  ),
  download: <path d="M8 2.5v8M4.5 7L8 10.5 11.5 7M3 13.5h10" />,
  print: (
    <>
      <path d="M4.5 6V2.5h7V6" />
      <rect x="2.5" y="6" width="11" height="5" rx="1" />
      <path d="M4.5 9.5h7v4h-7z" />
    </>
  ),
  chevron: <path d="M4.5 6.5L8 10l3.5-3.5" />,
  flag: <path d="M3.5 14V2.5M3.5 3h8l-2 3 2 3h-8" />,
  grip: <path d="M6 4h.01M10 4h.01M6 8h.01M10 8h.01M6 12h.01M10 12h.01" strokeWidth="2.2" />,
  search: (
    <>
      <circle cx="7" cy="7" r="4" />
      <path d="M10 10l3.5 3.5" />
    </>
  ),
  external: <path d="M9 2.5h4.5V7M13.5 2.5L7.5 8.5M11.5 9.5v4h-9v-9h4" />,
};

export function Icon({ name, size = 16 }: { name: keyof typeof paths | string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name]}
    </svg>
  );
}

// ---------- brand ----------
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    <span className="brand-mark" style={{ width: size, height: size, borderRadius: Math.round(size / 4.4) }}>
      <svg
        width={Math.round(size * 0.64)}
        height={Math.round(size * 0.64)}
        viewBox="0 0 16 16"
        fill="none"
        stroke="#fff"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M2 12l3.5-4 3 2.5L14 4" />
      </svg>
    </span>
  );
}

// ---------- status badges ----------
const statusClass: Record<RunStatus, string> = {
  queued: 'warn pulse',
  running: 'info pulse',
  passed: 'good',
  failed: 'bad',
  stopped: 'warn',
  error: 'bad',
};

export function StatusBadge({ status }: { status: RunStatus | undefined }) {
  if (!status) return <span className="muted">never run</span>;
  return <span className={`badge ${statusClass[status] ?? ''}`}>{status}</span>;
}

export function WorkerStateBadge({ state }: { state: string }) {
  const cls = state === 'idle' ? 'good' : state === 'busy' ? 'info pulse' : '';
  return <span className={`badge ${cls}`}>{state}</span>;
}

// ---------- misc ----------
export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="loading">
      <span className="spinner" /> {label}
    </div>
  );
}

export function ErrorAlert({ error, title }: { error: unknown; title?: string }) {
  if (!error) return null;
  const { message, details } = errorText(error);
  return (
    <div className="alert bad" role="alert">
      <div className="alert-title">{title ? `${title}: ${message}` : message}</div>
      {details.length > 0 && (
        <ul>
          {details.map((d, i) => (
            <li key={i}>{d}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function Kpi({
  label,
  value,
  unit,
  sub,
  tone,
}: {
  label: string;
  value: ReactNode;
  unit?: string;
  sub?: ReactNode;
  tone?: 'bad' | 'warn';
}) {
  return (
    <div className={`kpi ${tone ?? ''}`}>
      <div className="k-label">{label}</div>
      <div className="k-value">
        {value}
        {unit && <span className="k-unit">{unit}</span>}
      </div>
      {sub !== undefined && <div className="k-sub">{sub}</div>}
    </div>
  );
}

export function Modal({
  title,
  onClose,
  children,
  footer,
  width,
}: {
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" style={width ? { width } : undefined}>
        <div className="modal-head">
          <h2>{title}</h2>
          <span className="grow" />
          <button className="btn ghost icon sm" onClick={onClose} aria-label="Close">
            <Icon name="x" />
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}

export function ConfirmDialog({
  title,
  message,
  confirmLabel = 'Delete',
  busy,
  error,
  onConfirm,
  onCancel,
}: {
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  busy?: boolean;
  error?: unknown;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <Modal
      title={title}
      onClose={onCancel}
      width={420}
      footer={
        <>
          <button className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn danger-solid" onClick={onConfirm} disabled={busy}>
            {busy ? 'Working…' : confirmLabel}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 10 }}>
        <div className="text-2">{message}</div>
        <ErrorAlert error={error} />
      </div>
    </Modal>
  );
}

/** Delta between two values, coloured by whether b is better than a. */
export function Delta({
  a,
  b,
  lowerIsBetter,
  asPoints,
}: {
  a: number | undefined;
  b: number | undefined;
  lowerIsBetter: boolean;
  /** Show absolute difference in percentage points (for rates). */
  asPoints?: boolean;
}) {
  if (a === undefined || b === undefined || !Number.isFinite(a) || !Number.isFinite(b)) {
    return <span className="delta same">—</span>;
  }
  let text: string;
  const diff = b - a;
  if (asPoints) {
    text = `${diff >= 0 ? '+' : ''}${(diff * 100).toFixed(2)} pp`;
  } else if (a === 0) {
    text = diff === 0 ? '0%' : b > 0 ? '+∞' : '−∞';
  } else {
    const pct = (diff / Math.abs(a)) * 100;
    text = `${pct >= 0 ? '+' : ''}${pct.toFixed(1)}%`;
  }
  const eps = asPoints ? 0.0005 : Math.abs(a) * 0.01;
  let cls = 'same';
  if (Math.abs(diff) > eps) {
    const better = lowerIsBetter ? diff < 0 : diff > 0;
    cls = better ? 'better' : 'worse';
  }
  return <span className={`delta ${cls}`}>{text}</span>;
}
