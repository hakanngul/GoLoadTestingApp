// Shared building blocks of the reporting screens: verdict badges, the
// report-window control, run label chips, a dropdown menu and the
// "vs baseline" metric table.
import { useEffect, useRef, useState, type ReactNode } from 'react';
import type { Comparison, MetricDelta, Verdict, Window } from '../api/types';
import {
  fmtDelta,
  fmtMetric,
  metricInfo,
  METRIC_ORDER,
  overallVerdict,
  regressionText,
  verdictLabel,
  verdictTone,
  windowRange,
  type WindowSel,
} from '../lib/report';
import { Icon } from './ui';

export function VerdictBadge({ verdict }: { verdict: Verdict | string }) {
  return <span className={`vbadge ${verdictTone(verdict) || 'same'}`}>{verdictLabel(verdict)}</span>;
}

/** Delta text coloured by its verdict. */
export function DeltaText({ d }: { d: Pick<MetricDelta, 'metric' | 'delta' | 'verdict'> | undefined }) {
  if (!d) return <span className="delta same">—</span>;
  const tone = d.verdict === 'better' ? 'better' : d.verdict === 'worse' ? 'worse' : 'same';
  return <span className={`delta ${tone}`}>{fmtDelta(d)}</span>;
}

/**
 * Overall verdict of a comparison: red "Regressed · p95 +23%", green
 * "Improved", grey "No change"; `empty` when there is no comparison.
 */
export function OverallBadge({ c, empty = null }: { c: Comparison | undefined | null; empty?: ReactNode }) {
  const v = overallVerdict(c);
  if (v === 'none') return <>{empty}</>;
  if (v === 'regressed') {
    const t = regressionText(c);
    return (
      <span className="overall bad" title="At least one metric is worse than the reference beyond the tolerance">
        <span className="badge bad">Regressed</span>
        {t && <span className="overall-why">{t}</span>}
      </span>
    );
  }
  if (v === 'improved') return <span className="badge good">Improved</span>;
  return <span className="badge">No change</span>;
}

export function RunLabel({ label, size }: { label: string | undefined; size?: 'lg' }) {
  const l = (label ?? '').trim();
  if (!l) return null;
  return (
    <span className={`run-label ${size ?? ''}`} title={l}>
      {l}
    </span>
  );
}

export function BaselineBadge({ title = 'Baseline run of this test' }: { title?: string }) {
  return (
    <span className="baseline-badge" title={title}>
      <Icon name="flag" size={11} /> Baseline
    </span>
  );
}

// ---------- window control ----------

export function WindowControl({
  value,
  onChange,
  resolved,
  allowCustom = true,
  onResetCustom,
  customHint = 'Drag across the throughput chart',
}: {
  value: WindowSel;
  onChange: (w: WindowSel) => void;
  /** The resolved window of the current report (shows its range). */
  resolved?: Window;
  allowCustom?: boolean;
  onResetCustom?: () => void;
  customHint?: string;
}) {
  const [pickingCustom, setPickingCustom] = useState(false);
  const isCustom = value.kind === 'custom';
  useEffect(() => {
    if (isCustom) setPickingCustom(false);
  }, [isCustom]);
  const range = resolved && resolved.kind === value.kind ? windowRange(resolved) : '';
  return (
    <div className="window-control no-print">
      <span className="wc-label">Window</span>
      <div className="segmented" role="group" aria-label="Report window">
        <button
          className={value.kind === 'steady' && !pickingCustom ? 'on' : ''}
          onClick={() => {
            setPickingCustom(false);
            onChange({ kind: 'steady' });
          }}
          title="Without the initial ramp-up and the final ramp-down"
        >
          Steady state
        </button>
        <button
          className={value.kind === 'full' && !pickingCustom ? 'on' : ''}
          onClick={() => {
            setPickingCustom(false);
            onChange({ kind: 'full' });
          }}
        >
          Full run
        </button>
        {allowCustom && (
          <button className={isCustom || pickingCustom ? 'on' : ''} onClick={() => setPickingCustom(true)}>
            Custom
          </button>
        )}
      </div>
      {isCustom ? (
        <span className="window-chip">
          Window {windowRange(value)}
          <button
            className="btn ghost icon sm"
            aria-label="Reset window"
            title="Reset to steady state"
            onClick={() => (onResetCustom ? onResetCustom() : onChange({ kind: 'steady' }))}
          >
            <Icon name="x" size={12} />
          </button>
        </span>
      ) : pickingCustom ? (
        <span className="hint wc-hint">{customHint}</span>
      ) : range ? (
        <span className="muted num wc-range">{range}</span>
      ) : null}
    </div>
  );
}

// ---------- dropdown menu ----------

export interface MenuItem {
  label: ReactNode;
  icon?: string;
  hint?: ReactNode;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
  divider?: boolean;
}

export function Menu({
  label,
  items,
  align = 'right',
  className = 'btn',
  open: openProp,
  onOpenChange,
}: {
  label: ReactNode;
  items: MenuItem[];
  align?: 'left' | 'right';
  className?: string;
  open?: boolean;
  onOpenChange?: (o: boolean) => void;
}) {
  const [openState, setOpenState] = useState(false);
  const open = openProp ?? openState;
  const setOpen = (o: boolean) => {
    setOpenState(o);
    onOpenChange?.(o);
  };
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
  return (
    <div className="menu-wrap" ref={ref}>
      <button className={className} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)}>
        {label}
        <Icon name="chevron" size={12} />
      </button>
      {open && (
        <div className={`menu ${align}`} role="menu">
          {items.map((it, i) => (
            <div key={i}>
              {it.divider && <div className="menu-sep" />}
              <button
                role="menuitem"
                className={`menu-item ${it.danger ? 'danger' : ''}`}
                disabled={it.disabled}
                onClick={() => {
                  setOpen(false);
                  it.onSelect();
                }}
              >
                {it.icon && <Icon name={it.icon} size={14} />}
                <span className="grow">
                  {it.label}
                  {it.hint && <span className="menu-hint">{it.hint}</span>}
                </span>
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------- metric delta table ----------

/** Reference / candidate / delta / verdict per metric. */
export function MetricDeltaTable({
  c,
  refHead = 'Baseline',
  candHead = 'This run',
}: {
  c: Comparison;
  refHead?: ReactNode;
  candHead?: ReactNode;
}) {
  const byKey = new Map((c.metrics ?? []).map((m) => [m.metric, m]));
  const ordered = [
    ...METRIC_ORDER.filter((k) => byKey.has(k)).map((k) => byKey.get(k)!),
    ...(c.metrics ?? []).filter((m) => !(METRIC_ORDER as readonly string[]).includes(m.metric)),
  ];
  return (
    <table className="t compact">
      <thead>
        <tr>
          <th>Metric</th>
          <th className="num">{refHead}</th>
          <th className="num">{candHead}</th>
          <th className="num">Δ</th>
          <th className="num">Verdict</th>
        </tr>
      </thead>
      <tbody>
        {ordered.map((m) => (
          <tr key={m.metric} className={m.verdict === 'worse' ? 'row-worse' : ''}>
            <td style={{ fontWeight: 600 }}>{metricInfo(m.metric).label}</td>
            <td className="num text-2">{fmtMetric(m.metric, m.reference)}</td>
            <td className="num">{fmtMetric(m.metric, m.candidate)}</td>
            <td className="num">
              <DeltaText d={m} />
            </td>
            <td className="num">
              <VerdictBadge verdict={m.verdict} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Horizontal pass-rate bar with the percentage. */
export function PassRateBar({ rate, runs }: { rate: number; runs?: number }) {
  const pct = Math.max(0, Math.min(1, Number.isFinite(rate) ? rate : 0));
  const tone = pct >= 0.9 ? 'good' : pct >= 0.6 ? 'warn' : 'bad';
  return (
    <div className="passbar" title={runs !== undefined ? `${Math.round(pct * 100)}% of ${runs} finished runs passed` : undefined}>
      <div className="passbar-track">
        <div className={`passbar-fill ${tone}`} style={{ width: `${pct * 100}%` }} />
      </div>
      <span className="num passbar-v">{Math.round(pct * 100)}%</span>
    </div>
  );
}
