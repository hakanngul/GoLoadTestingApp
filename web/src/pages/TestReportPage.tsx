import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api } from '../api/client';
import type { Run, TrendPoint } from '../api/types';
import { ChartCard, TrendChart, usePalette, yMs, yPct, type Series, type TrendRow } from '../components/charts';
import { BaselineBadge, OverallBadge, RunLabel, WindowControl } from '../components/report';
import { ErrorAlert, Icon, Loading, StatusBadge } from '../components/ui';
import { fmtDateTime, fmtMs, fmtPct, fmtRps, fmtSeconds, runDurationS } from '../lib/format';
import { useAsync } from '../lib/hooks';
import {
  compareSummaries,
  fmtShortDate,
  MAX_COMPARE,
  parseWindow,
  windowLabel,
  windowParams,
  type WindowSel,
} from '../lib/report';

export function TestReportPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const win = parseWindow(params);
  const winKey = win.kind;
  const test = useAsync(() => api.getTest(id), [id]);
  const trend = useAsync(() => api.trend(id, { limit: 30, window: win }), [id, winKey]);
  const runs = useAsync(() => api.listRuns({ testId: id, limit: 200 }), [id]);
  const pal = usePalette();
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState('');
  const [actionErr, setActionErr] = useState<unknown>(null);

  const points = trend.data ?? [];
  const baselineId = test.data ? test.data.baseline_run_id ?? '' : points.find((p) => p.is_baseline)?.run_id ?? '';
  const baseline = points.find((p) => p.run_id === baselineId);
  const runById = useMemo(() => new Map((runs.data ?? []).map((r) => [r.id, r])), [runs.data]);

  const rows: TrendRow[] = useMemo(
    () =>
      points.map((p, i) => ({
        idx: i,
        name: p.label || fmtShortDate(p.started_at),
        title: `${p.label ? `${p.label} · ` : ''}${fmtShortDate(p.started_at, true)}`,
        baseline: p.run_id === baselineId,
        p95_ms: p.summary.p95_ms,
        p99_ms: p.summary.p99_ms,
        rps: p.summary.rps,
        error_rate: p.summary.error_rate,
      })),
    [points, baselineId],
  );
  const baseIdx = rows.find((r) => r.baseline)?.idx;
  const latSeries: Series[] = [
    { key: 'p95_ms', label: 'p95', color: pal['--series-2'] },
    { key: 'p99_ms', label: 'p99', color: pal['--series-3'] },
  ];
  const rpsSeries: Series[] = [{ key: 'rps', label: 'RPS', color: pal['--series-1'] }];
  const errSeries: Series[] = [{ key: 'error_rate', label: 'Error rate', color: pal['--series-red'] }];

  const setWindow = (w: WindowSel) => {
    const next = new URLSearchParams(params);
    for (const k of ['window', 'from', 'to']) next.delete(k);
    if (w.kind !== 'steady') windowParams(w).forEach((v, k) => next.set(k, v));
    setParams(next, { replace: true });
  };

  const setBaseline = async (runId: string) => {
    setBusy(runId || 'clear');
    setActionErr(null);
    try {
      const t = await api.setBaseline(id, runId);
      test.setData(t);
      trend.reload();
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy('');
    }
  };

  const toggle = (rid: string) =>
    setSelected((s) => (s.includes(rid) ? s.filter((x) => x !== rid) : s.length >= MAX_COMPARE ? s : [...s, rid]));

  const compareSelected = () => {
    // Reference = the baseline when selected, else the oldest selected run.
    const chrono = points.filter((p) => selected.includes(p.run_id)).map((p) => p.run_id);
    const ordered = chrono.includes(baselineId) ? [baselineId, ...chrono.filter((x) => x !== baselineId)] : chrono;
    const q = new URLSearchParams({ runs: ordered.join(',') });
    if (win.kind !== 'steady') q.set('window', win.kind);
    nav(`/compare?${q.toString()}`);
  };

  const latest = points[points.length - 1];
  const latestCmp =
    latest && baseline && latest.run_id !== baseline.run_id
      ? compareSummaries(baseline.summary, latest.summary, 0.1, { ref: baseline.run_id, cand: latest.run_id })
      : undefined;

  if (test.error) return <ErrorAlert error={test.error} title="Could not load test" />;

  return (
    <>
      <div className="page-head">
        <div style={{ minWidth: 0 }}>
          <div className="breadcrumb">
            <Link to="/reports">Reports</Link> / test report
          </div>
          <h1>{test.data?.name ?? '…'}</h1>
          <div className="page-sub">
            {points.length} finished run{points.length === 1 ? '' : 's'} (last 30) · {windowLabel(win)} window
          </div>
        </div>
        <span className="spacer" />
        <WindowControl value={win} onChange={setWindow} allowCustom={false} />
        <Link className="btn" to={`/runs?test=${id}`}>
          All runs
        </Link>
        <Link className="btn" to={`/tests/${id}`}>
          <Icon name="edit" size={14} /> Edit test
        </Link>
      </div>

      <ErrorAlert error={actionErr} title="Baseline" />
      {trend.error && !trend.data ? <ErrorAlert error={trend.error} title="Failed to load the trend" /> : null}

      {trend.loading && !trend.data ? (
        <Loading />
      ) : (
        <div className="stack" style={{ gap: 12 }}>
          <div className="baseline-strip card">
            {baseline ? (
              <>
                <BaselineBadge />
                <span className="bs-name">
                  <RunLabel label={baseline.label} /> <span className="text-2">{fmtDateTime(baseline.started_at)}</span>
                </span>
                <span className="bs-metrics num text-2">
                  p95 <b>{fmtMs(baseline.summary.p95_ms)}</b> · p99 <b>{fmtMs(baseline.summary.p99_ms)}</b> · RPS{' '}
                  <b>{fmtRps(baseline.summary.rps)}</b> · errors <b>{fmtPct(baseline.summary.error_rate)}</b>
                </span>
                <span className="spacer" />
                {latestCmp && (
                  <span className="row" style={{ gap: 6 }}>
                    <span className="muted">Latest {latest.label || 'run'} vs baseline</span>
                    <OverallBadge c={latestCmp} />
                  </span>
                )}
                <button className="btn sm ghost" onClick={() => setBaseline('')} disabled={busy !== ''}>
                  Clear baseline
                </button>
              </>
            ) : baselineId ? (
              <span className="muted">The baseline run is older than the runs shown here.</span>
            ) : (
              <>
                <span className="muted">
                  No baseline yet. Pick a known-good run below with <b>Set as baseline</b> to get regression verdicts on new runs.
                </span>
              </>
            )}
          </div>

          <ChartCard title="Latency across runs (ms)" series={latSeries}>
            <TrendChart data={rows} series={latSeries} fmt={fmtMs} yFmt={yMs} height={220} baselineIdx={baseIdx} />
          </ChartCard>
          <div className="grid-2" style={{ gap: 12 }}>
            <ChartCard title="Throughput (req/s)" series={rpsSeries}>
              <TrendChart data={rows} series={rpsSeries} fmt={fmtRps} height={170} baselineIdx={baseIdx} />
            </ChartCard>
            <ChartCard title="Error rate" series={errSeries}>
              <TrendChart data={rows} series={errSeries} fmt={(v) => fmtPct(v)} yFmt={yPct} height={170} baselineIdx={baseIdx} />
            </ChartCard>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>Runs</h2>
              <span className="muted" style={{ fontSize: 12 }}>
                newest first · values over the {windowLabel(win).toLowerCase()} window
              </span>
              <span className="spacer" />
              {selected.length > 0 && (
                <button className="btn sm ghost" onClick={() => setSelected([])}>
                  Clear selection
                </button>
              )}
              <button
                className="btn sm primary"
                disabled={selected.length < 2}
                onClick={compareSelected}
                title={selected.length < 2 ? 'Select 2 to 6 runs' : undefined}
              >
                <Icon name="compare" size={13} /> Compare selected{selected.length ? ` (${selected.length})` : ''}
              </button>
            </div>
            {points.length === 0 ? (
              <div className="empty">
                <strong>No finished runs</strong>
                Start a run from the <Link to="/">Tests</Link> page.
              </div>
            ) : (
              <div className="table-wrap">
                <table className="t">
                  <thead>
                    <tr>
                      <th style={{ width: 28 }} />
                      <th>Run</th>
                      <th>Status</th>
                      <th className="num">Duration</th>
                      <th className="num">Workers</th>
                      <th className="num">p95</th>
                      <th className="num">Error %</th>
                      <th className="num">RPS</th>
                      <th>vs baseline</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {[...points].reverse().map((p) => (
                      <TrendRunRow
                        key={p.run_id}
                        p={p}
                        run={runById.get(p.run_id)}
                        baseline={baseline}
                        isBaseline={p.run_id === baselineId}
                        checked={selected.includes(p.run_id)}
                        canCheck={selected.includes(p.run_id) || selected.length < MAX_COMPARE}
                        busy={busy === p.run_id}
                        onToggle={() => toggle(p.run_id)}
                        onSetBaseline={() => setBaseline(p.run_id)}
                        onClearBaseline={() => setBaseline('')}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );
}

function TrendRunRow({
  p,
  run,
  baseline,
  isBaseline,
  checked,
  canCheck,
  busy,
  onToggle,
  onSetBaseline,
  onClearBaseline,
}: {
  p: TrendPoint;
  run: Run | undefined;
  baseline: TrendPoint | undefined;
  isBaseline: boolean;
  checked: boolean;
  canCheck: boolean;
  busy: boolean;
  onToggle: () => void;
  onSetBaseline: () => void;
  onClearBaseline: () => void;
}) {
  const s = p.summary;
  const cmp =
    baseline && !isBaseline
      ? compareSummaries(baseline.summary, s, 0.1, { ref: baseline.run_id, cand: p.run_id })
      : undefined;
  return (
    <tr className={checked ? 'row-selected' : ''}>
      <td>
        <input
          type="checkbox"
          checked={checked}
          disabled={!canCheck}
          onChange={onToggle}
          aria-label={`Select ${p.label || p.run_id} for comparison`}
          title={canCheck ? undefined : `Up to ${MAX_COMPARE} runs`}
        />
      </td>
      <td>
        <div className="row" style={{ gap: 6 }}>
          {p.label ? <RunLabel label={p.label} /> : null}
          <Link to={`/runs/${p.run_id}`} className={p.label ? 'muted' : ''} style={{ fontSize: 12 }}>
            {fmtShortDate(p.started_at, true)}
          </Link>
          {run?.notes ? (
            <span className="notes-dot" title={run.notes}>
              notes
            </span>
          ) : null}
        </div>
      </td>
      <td>
        <StatusBadge status={p.status} />
      </td>
      <td className="num">{run ? fmtSeconds(runDurationS(run.started_at, run.finished_at)) : '—'}</td>
      <td className="num">{p.workers || 1}</td>
      <td className="num">{fmtMs(s.p95_ms)}</td>
      <td className={`num ${s.error_rate > 0.01 ? 'bad' : ''}`}>{fmtPct(s.error_rate)}</td>
      <td className="num">{fmtRps(s.rps)}</td>
      <td>{isBaseline ? <BaselineBadge /> : <OverallBadge c={cmp} empty={<span className="muted">—</span>} />}</td>
      <td className="actions">
        {isBaseline ? (
          <button className="btn sm ghost" onClick={onClearBaseline} disabled={busy}>
            Clear baseline
          </button>
        ) : (
          <button className="btn sm ghost" onClick={onSetBaseline} disabled={busy} title="Compare new runs of this test against this one">
            <Icon name="flag" size={12} /> Set as baseline
          </button>
        )}
        <Link className="btn sm" to={`/runs/${p.run_id}`}>
          Open
        </Link>
      </td>
    </tr>
  );
}
