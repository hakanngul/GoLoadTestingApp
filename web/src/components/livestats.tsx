import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import type { ErrorGroup, LiveStats } from '../api/types';
import { fmtDateTime, fmtInt, fmtMs, fmtPct, fmtRps } from '../lib/format';
import {
  buildStatRows,
  errorCopyText,
  errorExample,
  errorStatusLabel,
  errorStatusTone,
  errorSteps,
  fmtAgo as agoText,
  fmtRunOffset,
  filterErrorGroups,
  groupKey,
  increasedFails,
  isOverflowGroup,
  messageParts,
  newGroupKeys,
  nextSort,
  sortErrorGroups,
  sortStatRows,
  totalFailures,
  type StatRow,
  type StatSort,
  type StatSortKey,
  type StatsMode,
} from '../lib/livestats';
import { Icon } from './ui';

// =====================================================================
// Live panel: Statistics | Failures (N)
// =====================================================================

type Tab = 'stats' | 'failures';

/**
 * Locust-style live tables under the charts. `stats` is null until the first
 * `event: stats` arrives (skeleton rows are shown meanwhile).
 */
export function LiveStatsPanel({
  stats,
  seq,
  now,
  startedAt,
}: {
  stats: LiveStats | null;
  seq: number;
  now: number;
  /** Run start (ISO); when set, error times are shown as offsets from it. */
  startedAt?: string;
}) {
  const [tab, setTab] = useState<Tab>('stats');
  const [mode, setMode] = useState<StatsMode>('total');
  const groups = stats?.errors ?? [];
  const failures = stats ? totalFailures(groups) : 0;

  // Flash the tab badge when a new error group shows up.
  const seenRef = useRef<Set<string> | null>(null);
  const [badgeFlash, setBadgeFlash] = useState(0);
  useEffect(() => {
    if (!stats) return;
    const g = stats.errors ?? [];
    const prev = seenRef.current;
    const keys = new Set(g.map(groupKey));
    if (prev && newGroupKeys(prev, g).size > 0) setBadgeFlash((n) => n + 1);
    seenRef.current = keys;
  }, [stats]);

  return (
    <div className="card live-stats">
      <div className="card-head live-stats-head">
        <div className="tabs" role="tablist" aria-label="Live tables">
          <button role="tab" aria-selected={tab === 'stats'} className={tab === 'stats' ? 'on' : ''} onClick={() => setTab('stats')}>
            Statistics
          </button>
          <button
            role="tab"
            aria-selected={tab === 'failures'}
            className={tab === 'failures' ? 'on' : ''}
            onClick={() => setTab('failures')}
          >
            Failures{' '}
            <span
              key={badgeFlash}
              className={`tab-count ${failures > 0 ? 'bad' : ''} ${badgeFlash > 0 ? 'flash' : ''}`}
              aria-label={`${failures} failures`}
            >
              {stats ? fmtInt(failures) : '…'}
            </span>
          </button>
        </div>
        <span className="spacer" />
        {tab === 'stats' && (
          <div className="segmented sm" role="group" aria-label="Statistics window">
            <button className={mode === 'total' ? 'on' : ''} onClick={() => setMode('total')} aria-pressed={mode === 'total'}>
              Cumulative
            </button>
            <button className={mode === 'current' ? 'on' : ''} onClick={() => setMode('current')} aria-pressed={mode === 'current'}>
              Last second
            </button>
          </div>
        )}
        {stats && (
          <span className="muted num live-stats-tick" title="Updated once per second">
            <span className="live-dot" /> live
          </span>
        )}
      </div>
      {tab === 'stats' ? (
        <StatisticsTable stats={stats} mode={mode} seq={seq} />
      ) : (
        <FailuresTable
          groups={stats ? groups : null}
          fmtTime={(iso) => (startedAt ? fmtOffsetCell(iso, startedAt) : fmtAgoLive(iso, now))}
          live
          seq={seq}
        />
      )}
    </div>
  );
}

/** "+42s" from the run start, with the wall-clock time as tooltip. */
export function fmtOffsetCell(iso: string, startedAt: string): ReactNode {
  return <span title={fmtDateTime(iso)}>{fmtRunOffset(iso, startedAt)}</span>;
}

function fmtAgoLive(iso: string, now: number): ReactNode {
  return <span title={fmtDateTime(iso)}>{agoText(iso, now)}</span>;
}

// ---------------------------------------------------------------- statistics

const STAT_COLS: { key: StatSortKey; label: string; title?: string; width?: number }[] = [
  { key: 'name', label: 'Name' },
  { key: 'requests', label: '# Requests', width: 104 },
  { key: 'fails', label: '# Fails', width: 84 },
  { key: 'failRate', label: 'Fail %', width: 76 },
  { key: 'currentRps', label: 'Current RPS', title: 'Requests per second over the last second', width: 100 },
  { key: 'p50', label: 'Median', width: 84 },
  { key: 'p90', label: 'p90', width: 84 },
  { key: 'p95', label: 'p95', width: 84 },
  { key: 'p99', label: 'p99', width: 84 },
  { key: 'max', label: 'Max', width: 84 },
  { key: 'avg', label: 'Avg', width: 84 },
];

function StatisticsTable({ stats, mode, seq }: { stats: LiveStats | null; mode: StatsMode; seq: number }) {
  const [sort, setSort] = useState<StatSort | null>(null);
  const rows = useMemo(() => sortStatRows(buildStatRows(stats, mode), sort), [stats, mode, sort]);

  // Rows whose cumulative fail count went up with the latest event.
  const prevFails = useRef(new Map<string, number>());
  const [flash, setFlash] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (!stats) return;
    const cum = buildStatRows(stats, 'total');
    setFlash(increasedFails(prevFails.current, cum));
    prevFails.current = new Map(cum.map((r) => [r.name, r.fails]));
  }, [stats]);

  return (
    <div className="table-wrap">
      <table className="t compact stat-table">
        <colgroup>
          {STAT_COLS.map((c) => (
            <col key={c.key} style={c.width ? { width: c.width } : undefined} />
          ))}
        </colgroup>
        <thead>
          <tr>
            {STAT_COLS.map((c) => {
              const on = sort?.key === c.key;
              return (
                <th
                  key={c.key}
                  className={c.key === 'name' ? '' : 'num'}
                  aria-sort={on ? (sort!.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
                  title={c.title}
                >
                  <button className={`th-sort ${on ? 'on' : ''}`} onClick={() => setSort((s) => nextSort(s, c.key))}>
                    {c.label}
                    <span className="th-arrow" aria-hidden>
                      {on ? (sort!.dir === 'asc' ? '▲' : '▼') : '▲'}
                    </span>
                  </button>
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {!stats ? (
            <SkeletonRows cols={STAT_COLS.length} />
          ) : rows.length <= 1 && rows[0]?.requests === 0 ? (
            <tr>
              <td colSpan={STAT_COLS.length} className="empty" style={{ padding: 24 }}>
                No requests yet.
              </td>
            </tr>
          ) : (
            rows.map((r) => <StatTr key={r.name} r={r} flash={flash.has(r.name) ? `flash-${seq % 2}` : ''} />)
          )}
        </tbody>
      </table>
    </div>
  );
}

function StatTr({ r, flash }: { r: StatRow; flash: string }) {
  return (
    <tr className={`${r.aggregate ? 'agg-row' : ''} ${flash}`}>
      <td className="stat-name" title={r.name}>
        {r.name}
      </td>
      <td className="num">{fmtInt(r.requests)}</td>
      <td className={`num ${r.fails > 0 ? 'bad' : ''}`}>{fmtInt(r.fails)}</td>
      <td className={`num ${r.failRate > 0.01 ? 'bad' : r.failRate > 0 ? 'warn' : ''}`}>{fmtPct(r.failRate)}</td>
      <td className="num">{fmtRps(r.currentRps)}</td>
      <td className="num">{fmtMs(r.p50)}</td>
      <td className="num">{fmtMs(r.p90)}</td>
      <td className="num">{fmtMs(r.p95)}</td>
      <td className="num">{fmtMs(r.p99)}</td>
      <td className="num">{fmtMs(r.max)}</td>
      <td className="num">{fmtMs(r.avg)}</td>
    </tr>
  );
}

function SkeletonRows({ cols, rows = 4 }: { cols: number; rows?: number }) {
  return (
    <>
      {Array.from({ length: rows }, (_, i) => (
        <tr key={i} className="skeleton-row" aria-hidden>
          {Array.from({ length: cols }, (_, j) => (
            <td key={j} className={j === 0 ? '' : 'num'}>
              <span className="skeleton" style={{ width: j === 0 ? `${60 - i * 8}%` : '70%' }} />
            </td>
          ))}
        </tr>
      ))}
    </>
  );
}

// ---------------------------------------------------------------- failures

/**
 * Grouped failures table, shared by the live panel and the result page.
 * `groups` null = still loading (skeleton rows).
 */
export function FailuresTable({
  groups,
  fmtTime,
  live = false,
  seq = 0,
  maxHeight,
}: {
  groups: ErrorGroup[] | null;
  fmtTime: (iso: string) => ReactNode;
  live?: boolean;
  seq?: number;
  maxHeight?: number;
}) {
  const [step, setStep] = useState('');
  const sorted = useMemo(() => sortErrorGroups(groups), [groups]);
  const steps = useMemo(() => errorSteps(groups), [groups]);
  const shown = useMemo(() => filterErrorGroups(sorted, step), [sorted, step]);
  const total = totalFailures(groups);

  // Highlight groups that appeared since the previous update.
  const seenRef = useRef<Set<string> | null>(null);
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (!groups) return;
    const prev = seenRef.current;
    setFresh(prev && live ? newGroupKeys(prev, groups) : new Set());
    seenRef.current = new Set(groups.map(groupKey));
  }, [groups, live]);

  useEffect(() => {
    if (step && !steps.includes(step)) setStep('');
  }, [step, steps]);

  return (
    <div className="failures">
      {groups && groups.length > 0 && (
        <div className="failures-bar">
          <label className="failures-filter">
            <span className="muted">Step</span>
            <select className="input sm" value={step} onChange={(e) => setStep(e.target.value)}>
              <option value="">All steps ({steps.length})</option>
              {steps.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>
          <span className="spacer" />
          <span className="muted num" style={{ fontSize: 12 }}>
            {fmtInt(total)} failure{total === 1 ? '' : 's'} in {groups.length} group{groups.length === 1 ? '' : 's'}
            {live ? ' · counts every failed request' : ''}
          </span>
        </div>
      )}
      <div className="table-wrap" style={maxHeight ? { maxHeight, overflowY: 'auto' } : undefined}>
        <table className="t compact fail-table">
          <colgroup>
            <col style={{ width: 120 }} />
            <col style={{ width: 160 }} />
            <col style={{ width: 128 }} />
            <col />
            <col style={{ width: 104 }} />
            <col style={{ width: 104 }} />
            <col style={{ width: 44 }} />
          </colgroup>
          <thead>
            <tr>
              <th className="num"># Occurrences</th>
              <th>Step</th>
              <th>Status</th>
              <th>Message</th>
              <th className="num">First seen</th>
              <th className="num">Last seen</th>
              <th aria-label="Copy" />
            </tr>
          </thead>
          <tbody>
            {!groups ? (
              <SkeletonRows cols={7} rows={3} />
            ) : shown.length === 0 ? (
              <tr>
                <td colSpan={7} className="empty fail-empty">
                  <strong>{groups.length === 0 ? 'No failures' : 'No failures for this step'}</strong>
                  {groups.length === 0 ? (live ? 'Every request so far succeeded.' : 'Every request succeeded.') : ''}
                </td>
              </tr>
            ) : (
              shown.map((g) => (
                <FailureTr
                  key={groupKey(g)}
                  g={g}
                  share={total > 0 ? g.count / total : 0}
                  fmtTime={fmtTime}
                  flash={fresh.has(groupKey(g)) ? `flash-${seq % 2}` : ''}
                />
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function FailureTr({ g, share, fmtTime, flash }: { g: ErrorGroup; share: number; fmtTime: (iso: string) => ReactNode; flash: string }) {
  const [open, setOpen] = useState(false);
  const overflow = isOverflowGroup(g);
  const example = errorExample(g);
  // Offer "show full message" only when the 2-line clamp actually hides text.
  const msgRef = useRef<HTMLDivElement>(null);
  const [long, setLong] = useState(false);
  useLayoutEffect(() => {
    const el = msgRef.current;
    if (!el || open) return;
    setLong(el.scrollHeight > el.clientHeight + 1);
  }, [g.message, open]);
  const toggle = example ? (open ? 'Hide example' : 'Show example') : long && !overflow ? (open ? 'Show less' : 'Show full message') : null;
  return (
    <tr className={`${overflow ? 'overflow-row' : ''} ${flash}`}>
      <td className="num">
        <div className="occ">
          <span className="occ-n">{fmtInt(g.count)}</span>
          <span className="occ-bar" aria-hidden>
            <span style={{ width: `${Math.max(2, share * 100)}%` }} />
          </span>
        </div>
      </td>
      <td className="fail-step" title={g.step}>
        {overflow && !g.step ? <span className="muted">all steps</span> : g.step || <span className="muted">—</span>}
      </td>
      <td>
        {overflow ? (
          <span className="badge plain">mixed</span>
        ) : (
          <span className={`status-chip ${errorStatusTone(g.status)}`}>{errorStatusLabel(g.status)}</span>
        )}
      </td>
      <td>
        {overflow ? (
          <span className="overflow-msg">
            <Icon name="alert" size={12} /> Other errors — more than 100 distinct errors; the rest are counted here
          </span>
        ) : (
          <div
            ref={msgRef}
            className={`fail-msg mono ${open ? '' : 'clamped'}`}
            title={example ? 'Request-specific parts (IDs, values, times) are shown as {placeholders}' : undefined}
          >
            {messageParts(g.message).map((p, i) =>
              p.ph ? (
                <span key={i} className="fail-ph">
                  {p.text}
                </span>
              ) : (
                p.text
              ),
            )}
          </div>
        )}
        {open && example && (
          <div className="fail-example">
            <span className="fail-example-label">{overflow ? 'First error counted here' : 'Example'}</span>
            <div className="fail-msg mono">{example}</div>
          </div>
        )}
        {toggle && (
          <button className="linklike fail-more" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            {toggle}
          </button>
        )}
      </td>
      <td className="num muted">{fmtTime(g.first)}</td>
      <td className="num">{fmtTime(g.last)}</td>
      <td className="actions">
        {!overflow && <CopyButton text={errorCopyText(g)} label={example ? 'Copy example message' : undefined} />}
      </td>
    </tr>
  );
}

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand('copy');
      ta.remove();
      return ok;
    } catch {
      return false;
    }
  }
}

export function CopyButton({ text, label = 'Copy message' }: { text: string; label?: string }) {
  const [state, setState] = useState<'idle' | 'ok' | 'fail'>('idle');
  useEffect(() => {
    if (state === 'idle') return;
    const h = window.setTimeout(() => setState('idle'), 1500);
    return () => window.clearTimeout(h);
  }, [state]);
  return (
    <button
      className={`btn ghost icon sm copy-btn ${state}`}
      title={state === 'ok' ? 'Copied' : state === 'fail' ? 'Copy failed' : label}
      aria-label={label}
      onClick={() => copyText(text).then((ok) => setState(ok ? 'ok' : 'fail'))}
    >
      <Icon name={state === 'ok' ? 'check' : 'copy'} size={13} />
    </button>
  );
}
