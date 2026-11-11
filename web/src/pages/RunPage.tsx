import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api, download, errorText, exportUrls, subscribeRun } from '../api/client';
import { isFinished, type Comparison, type LiveStats, type MetricPoint, type Run, type Summary } from '../api/types';
import { RunCharts } from '../components/charts';
import { FailuresTable, LiveStatsPanel } from '../components/livestats';
import { BaselineBadge, DeltaText, Menu, MetricDeltaTable, OverallBadge, RunLabel, WindowControl } from '../components/report';
import { ConfirmDialog, ErrorAlert, Icon, Kpi, Loading, Modal, ScheduledBadge, StatusBadge } from '../components/ui';
import {
  fmtClock,
  fmtCompact,
  fmtDateTime,
  fmtInt,
  fmtMs,
  fmtPct,
  fmtRps,
  fmtSeconds,
  fmtTime,
  runDurationS,
  shortId,
} from '../lib/format';
import { fmtRegressionValue, isRegression, regressionMetric, regressionRule } from '../lib/regression';
import { useAsync, useNow } from '../lib/hooks';
import { OP_SYMBOL, summarizeYaml, unitFor } from '../lib/scenario';
import { remoteWorkers } from '../lib/workers';
import { errorStatusLabel, errorStatusTone, fmtRunOffset } from '../lib/livestats';
import {
  customWindow,
  findDelta,
  overallVerdict,
  parseWindow,
  regressionText,
  windowLabel,
  windowParams,
  type WindowSel,
} from '../lib/report';

export function RunPage() {
  const { id = '' } = useParams<{ id: string }>();
  const [run, setRun] = useState<Run | null>(null);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    let alive = true;
    setRun(null);
    setErr(null);
    api
      .getRun(id)
      .then((r) => alive && setRun(r))
      .catch((e) => alive && setErr(e));
    return () => {
      alive = false;
    };
  }, [id]);

  const onDone = useCallback((r: Run) => setRun(r), []);

  if (err) return <ErrorAlert error={err} title="Could not load run" />;
  if (!run) return <Loading />;
  return isFinished(run.status) ? <ResultView run={run} /> : <LiveView initial={run} onDone={onDone} />;
}

function RunHeader({ run, children }: { run: Run; children?: ReactNode }) {
  return (
    <div className="page-head">
      <div style={{ minWidth: 0 }}>
        <div className="breadcrumb">
          <Link to="/runs">Runs</Link> / <span className="mono">{shortId(run.id)}</span>
        </div>
        <h1 className="row" style={{ gap: 10 }}>
          <Link to={`/tests/${run.test_id}`} style={{ color: 'inherit' }}>
            {run.test_name}
          </Link>
          <StatusBadge status={run.status} />
          <RunLabel label={run.label} size="lg" />
          <ScheduledBadge scheduleId={run.schedule_id} size="lg" />
        </h1>
      </div>
      <span className="spacer" />
      {children}
    </div>
  );
}

// =====================================================================
// Live monitoring
// =====================================================================

function mergePoints(map: Map<number, MetricPoint>, pts: MetricPoint[]) {
  for (const p of pts) map.set(Math.round(p.elapsed), p);
}

function LiveView({ initial, onDone }: { initial: Run; onDone: (r: Run) => void }) {
  const [run, setRun] = useState(initial);
  const [points, setPoints] = useState<MetricPoint[]>([]);
  const [connected, setConnected] = useState(true);
  const [confirmStop, setConfirmStop] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [stopErr, setStopErr] = useState<unknown>(null);
  const [live, setLive] = useState<{ stats: LiveStats | null; seq: number }>({ stats: null, seq: 0 });
  const mapRef = useRef(new Map<number, MetricPoint>());
  const now = useNow(1000);

  useEffect(() => {
    const map = mapRef.current;
    map.clear();
    let alive = true;
    let raf = 0;
    const flush = () => {
      raf = 0;
      if (!alive) return;
      setPoints([...map.values()].sort((a, b) => a.elapsed - b.elapsed));
    };
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(flush);
    };
    const unsub = subscribeRun(initial.id, {
      onMetric: (p) => {
        mergePoints(map, [p]);
        schedule();
      },
      onStatus: (r) => alive && setRun(r),
      onStats: (st) => alive && setLive((cur) => ({ stats: st, seq: cur.seq + 1 })),
      onDone: (r) => alive && onDone(r),
      onConnectionChange: (c) => alive && setConnected(c),
    });
    api
      .runMetrics(initial.id)
      .then((pts) => {
        // Backfill without overwriting points that already arrived live.
        for (const p of pts) {
          const k = Math.round(p.elapsed);
          if (!map.has(k)) map.set(k, p);
        }
        schedule();
      })
      .catch(() => undefined);
    return () => {
      alive = false;
      if (raf) cancelAnimationFrame(raf);
      unsub();
    };
  }, [initial.id, onDone]);

  // Safety net while queued: the SSE `status` event flips it to running, but
  // re-check now and then in case an event was missed during a reconnect.
  const isQueued = run.status === 'queued';
  useEffect(() => {
    if (!isQueued) return;
    let alive = true;
    const h = window.setInterval(() => {
      api
        .getRun(initial.id)
        .then((r) => {
          if (!alive || r.status === 'queued') return;
          if (isFinished(r.status)) onDone(r);
          else setRun(r);
        })
        .catch(() => undefined);
    }, 10000);
    return () => {
      alive = false;
      window.clearInterval(h);
    };
  }, [isQueued, initial.id, onDone]);

  const sum = useMemo(() => summarizeYaml(run.yaml), [run.yaml]);
  const total = sum?.total ?? 0;
  const unit = sum ? unitFor(sum.model) : 'it/s';
  const elapsed = run.status === 'queued' ? 0 : runDurationS(run.started_at, undefined);
  const last = points[points.length - 1];
  const totals = useMemo(() => {
    let req = 0;
    let errs = 0;
    let dropped = 0;
    for (const p of points) {
      req += p.requests;
      errs += p.errors;
      dropped += p.dropped;
    }
    return { req, errs, dropped };
  }, [points]);
  const cum = live.stats?.total;
  const cumSub = (v: number | undefined) => (cum && cum.requests > 0 ? `cumulative ${fmtMs(v)}` : 'cumulative —');

  const stop = async () => {
    setStopping(true);
    setStopErr(null);
    try {
      await api.stopRun(run.id);
      setConfirmStop(false);
    } catch (e) {
      setStopErr(e);
    } finally {
      setStopping(false);
    }
  };

  const progress = total > 0 ? Math.min(1, elapsed / total) : 0;
  const queued = run.status === 'queued';
  const cpu = last?.cpu ?? 0;

  return (
    <>
      <RunHeader run={run}>
        {queued ? (
          <div className="stack" style={{ gap: 2, alignItems: 'flex-end' }}>
            <div className="num" style={{ fontSize: 15, fontWeight: 600 }}>
              {fmtClock(runDurationS(run.started_at, undefined))}
            </div>
            <div className="muted" style={{ fontSize: 11.5 }}>waiting for workers</div>
          </div>
        ) : (
          <div className="stack" style={{ gap: 4, alignItems: 'flex-end' }}>
            <div className="num" style={{ fontSize: 15, fontWeight: 600 }}>
              {fmtClock(elapsed)} <span className="muted">/ {fmtClock(total)}</span>
            </div>
            <div className="progress">
              <div style={{ width: `${progress * 100}%` }} />
            </div>
          </div>
        )}
        <button className="btn danger-solid" onClick={() => setConfirmStop(true)} disabled={stopping}>
          <Icon name="stop" size={11} /> {queued ? 'Cancel' : 'Stop'}
        </button>
      </RunHeader>

      {!connected && (
        <div className="alert warn" style={{ marginBottom: 12 }}>
          Live stream disconnected — reconnecting…
        </div>
      )}
      {(run.warnings ?? []).length > 0 && (
        <div className="alert warn" style={{ marginBottom: 12 }}>
          {(run.warnings ?? []).map((w, i) => (
            <div key={i}>{w}</div>
          ))}
        </div>
      )}

      {queued ? (
        <QueuedState run={run} />
      ) : (
        <>
      <div className="kpis" style={{ marginBottom: 12 }}>
        <Kpi
          label="Throughput"
          value={fmtRps(last?.rps)}
          unit="req/s"
          sub={
            <>
              target {last ? fmtRps(last.target) : '—'} {unit}
            </>
          }
        />
        <Kpi label="p50" value={last ? fmtMs(last.p50_ms) : '—'} sub={`mean ${fmtMs(last?.mean_ms)}`} />
        <Kpi label="p95" value={last ? fmtMs(last.p95_ms) : '—'} sub={<span title="Since the start of the run">{cumSub(cum?.p95_ms)}</span>} />
        <Kpi label="p99" value={last ? fmtMs(last.p99_ms) : '—'} sub={<span title="Since the start of the run">{cumSub(cum?.p99_ms)}</span>} />
        <Kpi
          label="Error rate"
          value={fmtPct(last?.error_rate)}
          tone={last && last.error_rate > 0.05 ? 'bad' : last && last.error_rate > 0.01 ? 'warn' : undefined}
          sub={`${fmtInt(totals.errs)} of ${fmtCompact(totals.req)} req`}
        />
        <Kpi label="Active" value={fmtInt(last?.active)} sub={sum?.model === 'open' && sum.maxVus ? `max_vus ${sum.maxVus}` : 'in-flight'} />
        <Kpi
          label="Dropped"
          value={fmtInt(totals.dropped)}
          tone={totals.dropped > 0 ? 'warn' : undefined}
          sub={`${fmtInt(last?.dropped ?? 0)} last second`}
        />
        <Kpi
          label="Generator CPU"
          value={last ? Math.round(cpu * 100) : '—'}
          unit={last ? '%' : undefined}
          tone={cpu > 0.9 ? 'bad' : cpu > 0.75 ? 'warn' : undefined}
          sub={`${last?.workers ?? run.workers} worker${(last?.workers ?? run.workers) === 1 ? '' : 's'}`}
        />
      </div>

      <RunCharts points={points} targetUnit={unit} xMax={total} />
      <LiveStatsPanel stats={live.stats} seq={live.seq} now={now} />
        </>
      )}

      {confirmStop && (
        <ConfirmDialog
          title={queued ? 'Cancel this run?' : 'Stop this run?'}
          message={
            queued
              ? 'The run has not started yet. Launching workers is aborted and the run is marked as stopped.'
              : 'Load generation stops immediately. Partial results are kept and the run is marked as stopped.'
          }
          confirmLabel={queued ? 'Cancel run' : 'Stop run'}
          busy={stopping}
          error={stopErr}
          onConfirm={stop}
          onCancel={() => setConfirmStop(false)}
        />
      )}
    </>
  );
}

/** Shown while a run is `queued` (spot workers are being launched). */
function QueuedState({ run }: { run: Run }) {
  const fleet = useAsync(() => api.listWorkers(), [], 4000);
  const want = Math.max(1, run.workers || 1);
  const remote = remoteWorkers(fleet.data ?? []);
  // Workers already attached to this run, plus idle ones it can claim.
  const ready = Math.min(
    want,
    remote.filter((w) => w.run_id === run.id || (w.state === 'idle' && !w.run_id)).length,
  );
  const launching = Math.max(0, want - ready);
  return (
    <div className="card">
      <div className="queued-state">
        <span className="spinner lg" />
        <h2>Starting workers…</h2>
        <p>
          {launching > 0 ? (
            <>
              Launching <strong>{launching}</strong> spot instance{launching === 1 ? '' : 's'} for this {want}-worker run;
              this can take a few minutes.
            </>
          ) : (
            <>All {want} workers are connected; dispatching the scenario…</>
          )}{' '}
          Load starts automatically once every worker has registered with the master — you can leave this page open.
        </p>
        {fleet.data && (
          <>
            <div className="worker-slots" aria-label={`${ready} of ${want} workers ready`}>
              {Array.from({ length: Math.min(want, 32) }, (_, i) => (
                <span key={i} className={i < ready ? 'on' : ''} />
              ))}
            </div>
            <div className="muted num" style={{ fontSize: 12 }}>
              {ready} of {want} worker{want === 1 ? '' : 's'} ready
            </div>
          </>
        )}
        <div className="queued-steps">
          <span className="queued-step done">
            <Icon name="check" size={12} /> Run accepted
          </span>
          <span className="queued-step active">
            <Icon name="cloud" size={12} /> Launching workers
          </span>
          <span className="queued-step">
            <Icon name="play" size={10} /> Generating load
          </span>
        </div>
      </div>
    </div>
  );
}

// =====================================================================
// Result
// =====================================================================

const bannerInfo: Record<string, { tone: string; title: string; icon: string }> = {
  passed: { tone: 'good', title: 'Passed', icon: 'check' },
  failed: { tone: 'bad', title: 'Failed — threshold not met', icon: 'x' },
  stopped: { tone: 'warn', title: 'Stopped by user', icon: 'stop' },
  error: { tone: 'bad', title: 'Error — run did not complete', icon: 'alert' },
};

function thresholdActual(metric: string, v: number): string {
  if (metric === 'error_rate') return fmtPct(v);
  if (metric === 'rps') return `${fmtRps(v)} req/s`;
  return fmtMs(v);
}

export function ResultView({ run: initialRun }: { run: Run }) {
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const [run, setRun] = useState(initialRun);
  useEffect(() => setRun(initialRun), [initialRun]);
  const win = parseWindow(params);
  const winKey = windowParams(win).toString();
  const report = useAsync(() => api.runReport(run.id, win), [run.id, winKey]);
  const test = useAsync(() => api.getTest(run.test_id), [run.test_id]);
  const metrics = useAsync(() => api.runMetrics(run.id), [run.id]);
  const errors = useAsync(() => api.runErrors(run.id), [run.id]);
  const errorGroups = useAsync(() => api.runErrorGroups(run.id), [run.id]);
  const [compareOpen, setCompareOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [delBusy, setDelBusy] = useState(false);
  const [delErr, setDelErr] = useState<unknown>(null);
  const [toast, setToast] = useState<{ text: string; bad?: boolean } | null>(null);
  const [baselineBusy, setBaselineBusy] = useState(false);
  const sum = useMemo(() => summarizeYaml(run.yaml), [run.yaml]);
  const unit = sum ? unitFor(sum.model) : 'it/s';
  const rep = report.data && report.data.run.id === run.id ? report.data : undefined;
  // Until the windowed report arrives (or if it is unavailable) show the whole-run summary.
  const s = rep?.summary ?? run.summary;
  const b = bannerInfo[run.status] ?? bannerInfo.error;
  const dur = runDurationS(run.started_at, run.finished_at);
  const thresholds = (rep ? rep.thresholds : run.thresholds) ?? [];
  const failedCount = thresholds.filter((t) => !t.passed).length;
  const winText = rep ? windowLabel(rep.window) : windowLabel(win);
  const cmp = rep?.baseline;
  const baselineId = test.data?.baseline_run_id ?? '';
  const isBaseline = baselineId === run.id;
  const hasResults = !!run.summary;

  useEffect(() => {
    if (!toast) return;
    const h = window.setTimeout(() => setToast(null), 3500);
    return () => window.clearTimeout(h);
  }, [toast]);

  const setWindow = (w: WindowSel) => {
    const next = new URLSearchParams(params);
    for (const k of ['window', 'from', 'to']) next.delete(k);
    if (w.kind !== 'steady') windowParams(w).forEach((v, k) => next.set(k, v));
    setParams(next, { replace: true });
  };
  const onSelectRange = useCallback(
    (a: number, b2: number) => {
      const w = customWindow(a, b2);
      if (w) setWindow(w);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [params],
  );
  const highlight =
    rep && rep.window.kind !== 'full' && rep.window.to > rep.window.from
      ? { from: rep.window.from, to: rep.window.to, label: rep.window.kind === 'custom' ? 'window' : 'steady state' }
      : null;

  const doDelete = async () => {
    setDelBusy(true);
    setDelErr(null);
    try {
      await api.deleteRun(run.id);
      nav(`/runs`, { replace: true });
    } catch (e) {
      setDelErr(e);
      setDelBusy(false);
    }
  };
  const setAsBaseline = async (rid: string) => {
    setBaselineBusy(true);
    try {
      const t = await api.setBaseline(run.test_id, rid);
      test.setData(t);
      report.reload();
      setToast({ text: rid ? 'This run is now the baseline of the test.' : 'Baseline cleared.' });
    } catch (e) {
      setToast({ text: `Baseline: ${errorText(e).message}`, bad: true });
    } finally {
      setBaselineBusy(false);
    }
  };
  const dl = (url: string, name: string, what: string) => {
    setToast({ text: `Downloading ${what}…` });
    download(url, name).catch((e) => setToast({ text: `${what}: ${errorText(e).message}`, bad: true }));
  };
  const compareWithBaselineUrl = cmp
    ? `/compare?${new URLSearchParams({
        runs: `${cmp.reference_run_id},${run.id}`,
        ...(win.kind === 'steady' ? {} : Object.fromEntries(windowParams(win))),
      }).toString()}`
    : '';

  return (
    <>
      <RunHeader run={run}>
        <div className="row no-print" style={{ gap: 8 }}>
          <button className="btn" onClick={() => setCompareOpen(true)}>
            <Icon name="compare" size={14} /> Compare with…
          </button>
          <Menu
            label={
              <>
                <Icon name="download" size={14} /> Export
              </>
            }
            items={[
              {
                label: 'HTML report',
                hint: `printable, ${winText.toLowerCase()}`,
                icon: 'file',
                onSelect: () => dl(exportUrls.html(run.id, win), `goload-${run.test_name}-${shortId(run.id)}.html`, 'HTML report'),
              },
              {
                label: 'CSV',
                hint: 'per-second metrics',
                icon: 'download',
                onSelect: () => dl(exportUrls.csv(run.id), `goload-${shortId(run.id)}-metrics.csv`, 'CSV'),
              },
              {
                label: 'JSON',
                hint: 'run, metrics and errors',
                icon: 'download',
                onSelect: () => dl(exportUrls.json(run.id), `goload-${shortId(run.id)}.json`, 'JSON'),
              },
              { label: 'Print…', icon: 'print', divider: true, onSelect: () => window.print() },
            ]}
          />
          <button className="btn" onClick={() => nav(`/tests/${run.test_id}`)}>
            <Icon name="edit" size={14} /> Edit test
          </button>
          <button className="btn ghost icon danger" title="Delete run" aria-label="Delete run" onClick={() => setConfirmDelete(true)}>
            <Icon name="trash" size={15} />
          </button>
        </div>
      </RunHeader>

      <div className="stack" style={{ gap: 12 }}>
        <div className={`banner ${b.tone}`}>
          <div className="banner-icon">
            <Icon name={b.icon} size={16} />
          </div>
          <div className="grow" style={{ minWidth: 0 }}>
            <div className="b-title">{b.title}</div>
            <div className="b-meta">
              {fmtDateTime(run.started_at)} · ran {fmtSeconds(dur)}
              {sum ? ` of ${fmtSeconds(sum.total)} planned` : ''} · {run.workers || 1} worker
              {(run.workers || 1) === 1 ? '' : 's'} · peak CPU {Math.round(run.peak_cpu * 100)}%
              {thresholds.length > 0 && ` · ${thresholds.length - failedCount}/${thresholds.length} thresholds passed`}
            </div>
          </div>
          {cmp && (
            <a className="banner-verdict" href={`#vs-baseline`}>
              <OverallBadge c={cmp} />
              <span className="muted">vs baseline</span>
            </a>
          )}
        </div>

        <LabelNotes run={run} onSaved={setRun} />

        {run.error && (
          <div className="alert bad">
            <div className="alert-title">Error</div>
            <div className="mono" style={{ marginTop: 4 }}>
              {run.error}
            </div>
          </div>
        )}
        {(run.warnings ?? []).length > 0 && (
          <div className="alert warn">
            <div className="alert-title">Warnings</div>
            <ul style={{ fontFamily: 'inherit' }}>
              {(run.warnings ?? []).map((w, i) => (
                <li key={i} style={{ fontFamily: 'inherit' }}>
                  {w}
                </li>
              ))}
            </ul>
          </div>
        )}

        {hasResults && (
          <div className="report-toolbar">
            <WindowControl value={win} onChange={setWindow} resolved={rep?.window} />
            <span className="spacer" />
            {report.loading && rep && <span className="spinner" aria-label="Updating" />}
            <span className="report-scope">
              Summary, thresholds and steps over <b>{winText}</b>
            </span>
          </div>
        )}
        {report.error && hasResults ? <ErrorAlert error={report.error} title="Windowed report unavailable — showing the whole run" /> : null}

        <div className="kpis">
          <Kpi label="Requests" value={fmtInt(s?.requests)} sub={`${fmtInt(s?.dropped)} dropped`} />
          <Kpi
            label="Throughput"
            value={fmtRps(s?.rps)}
            unit="req/s"
            sub={<KpiDelta c={cmp} metric="rps" fallback={sum ? `peak target ${sum.peak} ${unit}` : undefined} />}
          />
          <Kpi
            label="Error rate"
            value={fmtPct(s?.error_rate)}
            tone={s && s.error_rate > 0.05 ? 'bad' : s && s.error_rate > 0.01 ? 'warn' : undefined}
            sub={<KpiDelta c={cmp} metric="error_rate" fallback={`${fmtInt(s?.errors)} errors`} />}
          />
          <Kpi label="p50" value={fmtMs(s?.p50_ms)} sub={<KpiDelta c={cmp} metric="p50_ms" fallback={`mean ${fmtMs(s?.mean_ms)}`} />} />
          <Kpi label="p95" value={fmtMs(s?.p95_ms)} sub={<KpiDelta c={cmp} metric="p95_ms" fallback={`p90 ${fmtMs(s?.p90_ms)}`} />} />
          <Kpi label="p99" value={fmtMs(s?.p99_ms)} sub={<KpiDelta c={cmp} metric="p99_ms" fallback={`max ${fmtMs(s?.max_ms)}`} />} />
        </div>

        <div className="grid-2" style={{ gap: 12, gridTemplateColumns: 'minmax(0, 2fr) minmax(0, 3fr)' }}>
          <div className="card">
            <div className="card-head">
              <h2>Thresholds</h2>
              <span className="spacer" />
              {hasResults && <span className="window-tag">{winText}</span>}
            </div>
            {thresholds.length === 0 ? (
              <div className="empty">No thresholds defined.</div>
            ) : (
              <table className="t">
                <thead>
                  <tr>
                    <th>Result</th>
                    <th>Metric</th>
                    <th className="num">Rule</th>
                    <th className="num">Actual</th>
                  </tr>
                </thead>
                <tbody>
                  {thresholds.map((t, i) => (
                    <tr key={i}>
                      <td>
                        <span className={`badge ${t.passed ? 'good' : 'bad'}`}>{t.passed ? 'pass' : 'fail'}</span>
                      </td>
                      <td className="mono">{t.metric}</td>
                      <td className="num mono">
                        {isRegression(t) ? regressionRule(t) : `${OP_SYMBOL[t.op] ?? t.op} ${thresholdActual(t.metric, t.value)}`}
                      </td>
                      <td className={`num ${t.passed ? '' : 'bad'}`} style={{ fontWeight: 600 }}>
                        {isRegression(t) ? fmtRegressionValue(regressionMetric(t), t.actual) : thresholdActual(t.metric, t.actual)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {rep && (run.status === 'passed' || run.status === 'failed') && failedCount > 0 !== (run.status === 'failed') && (
              <div className="hint" style={{ padding: '6px 14px 10px' }}>
                The run status reflects the whole run; over this window the thresholds {failedCount ? 'fail' : 'pass'}.
              </div>
            )}
          </div>
          <BaselinePanel
            cmp={cmp}
            run={run}
            testLoaded={!!test.data}
            baselineId={baselineId}
            isBaseline={isBaseline}
            hasResults={hasResults}
            busy={baselineBusy}
            compareUrl={compareWithBaselineUrl}
            onSet={() => setAsBaseline(run.id)}
            onClear={() => setAsBaseline('')}
          />
        </div>

        <StepTable summary={s} title={hasResults ? winText : undefined} />

        {metrics.error ? <ErrorAlert error={metrics.error} title="Metrics unavailable" /> : null}
        <RunCharts
          points={metrics.data ?? []}
          targetUnit={unit}
          finished
          highlight={highlight}
          onSelectRange={hasResults ? onSelectRange : undefined}
          throughputExtra={hasResults ? <span className="muted no-print" style={{ fontSize: 11.5 }}>drag to set a custom window</span> : undefined}
        />

        <div className="card" id="failures">
          <div className="card-head">
            <h2>Failures</h2>
            <span className="muted" style={{ fontSize: 12 }}>
              every failed request, grouped by step, status and message
            </span>
            <span className="spacer" />
            <span className="window-tag">whole run</span>
          </div>
          {errorGroups.error ? (
            <div className="card-body">
              <ErrorAlert error={errorGroups.error} title="Grouped failures unavailable" />
            </div>
          ) : (
            <FailuresTable
              groups={errorGroups.loading && !errorGroups.data ? null : errorGroups.data ?? []}
              fmtTime={(iso) => (
                <span title={fmtDateTime(iso)}>{fmtRunOffset(iso, run.started_at)}</span>
              )}
              maxHeight={420}
            />
          )}
        </div>

        <details className="card collapsible samples" open={!!errorGroups.error}>
          <summary>
            Error samples
            <span className="muted">{errors.data ? `${errors.data.length} kept · whole run` : ''}</span>
          </summary>
          {errors.loading ? (
            <Loading />
          ) : errors.error ? (
            <div className="card-body">
              <ErrorAlert error={errors.error} />
            </div>
          ) : (errors.data ?? []).length === 0 ? (
            <div className="empty">No error samples recorded.</div>
          ) : (
            <div className="table-wrap errors-wrap">
              <table className="t compact">
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>Step</th>
                    <th>Status</th>
                    <th>Message</th>
                  </tr>
                </thead>
                <tbody>
                  {(errors.data ?? []).map((e, i) => (
                    <tr key={i}>
                      <td className="num mono" style={{ textAlign: 'left' }}>
                        {fmtTime(e.t)}
                      </td>
                      <td>{e.step}</td>
                      <td>
                        <span className={`status-chip ${errorStatusTone(e.status)}`}>{errorStatusLabel(e.status)}</span>
                      </td>
                      <td className="mono" style={{ wordBreak: 'break-word' }}>
                        {e.message}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </details>

        <details className="card collapsible no-print">
          <summary>Scenario YAML used</summary>
          <pre className="code">{run.yaml}</pre>
        </details>
      </div>

      {compareOpen && <CompareDialog run={run} onClose={() => setCompareOpen(false)} />}
      {confirmDelete && (
        <ConfirmDialog
          title="Delete this run?"
          message={
            <>
              The run{run.label ? <> <b>{run.label}</b></> : ''} of <b>{run.test_name}</b> from {fmtDateTime(run.started_at)}, its
              metrics and error samples are deleted permanently.
              {isBaseline && ' It is the baseline of its test; the test will have no baseline afterwards.'}
            </>
          }
          confirmLabel="Delete run"
          busy={delBusy}
          error={delErr}
          onConfirm={doDelete}
          onCancel={() => setConfirmDelete(false)}
        />
      )}
      {toast && <div className={`toast ${toast.bad ? 'bad' : ''}`}>{toast.text}</div>}
    </>
  );
}

/** KPI sub line: delta vs baseline when there is a comparison, else the fallback. */
function KpiDelta({ c, metric, fallback }: { c: Comparison | undefined; metric: string; fallback?: ReactNode }) {
  const d = findDelta(c, metric);
  if (!d) return <>{fallback}</>;
  return (
    <>
      <DeltaText d={d} /> <span className="muted">vs baseline</span>
    </>
  );
}

function BaselinePanel({
  cmp,
  run,
  testLoaded,
  baselineId,
  isBaseline,
  hasResults,
  busy,
  compareUrl,
  onSet,
  onClear,
}: {
  cmp: Comparison | undefined;
  run: Run;
  testLoaded: boolean;
  baselineId: string;
  isBaseline: boolean;
  hasResults: boolean;
  busy: boolean;
  compareUrl: string;
  onSet: () => void;
  onClear: () => void;
}) {
  const v = overallVerdict(cmp);
  return (
    <div className="card" id="vs-baseline" style={{ minWidth: 0 }}>
      <div className="card-head">
        <h2>vs baseline</h2>
        {cmp && (
          <span className={`verdict-pill ${v === 'regressed' ? 'bad' : 'good'}`}>
            {v === 'regressed' ? `Regression · ${regressionText(cmp)}` : 'No regression'}
          </span>
        )}
        <span className="spacer" />
        {cmp && (
          <>
            <span className="muted num" style={{ fontSize: 11.5 }}>
              tolerance ±{Math.round((cmp.tolerance || 0.1) * 100)}%
            </span>
            <Link className="btn sm ghost no-print" to={compareUrl}>
              Open in compare →
            </Link>
          </>
        )}
      </div>
      {cmp ? (
        <>
          <div className="baseline-ref">
            Reference: <Link to={`/runs/${cmp.reference_run_id}`}>baseline run {shortId(cmp.reference_run_id)}</Link> · same window
          </div>
          <MetricDeltaTable c={cmp} refHead="Baseline" candHead="This run" />
        </>
      ) : !testLoaded ? (
        <Loading />
      ) : isBaseline ? (
        <div className="empty">
          <strong>
            <span className="row" style={{ justifyContent: 'center' }}>
              <BaselineBadge /> This run is the baseline of {run.test_name}
            </span>
          </strong>
          New runs of this test are compared against it.{' '}
          <button className="linklike no-print" onClick={onClear} disabled={busy}>
            Clear baseline
          </button>
        </div>
      ) : baselineId ? (
        <div className="empty">
          <strong>Not comparable with the baseline</strong>
          The baseline run has no results for this window.
        </div>
      ) : (
        <div className="empty">
          <strong>No baseline for {run.test_name}</strong>
          A baseline is the reference run that new runs are checked against for regressions.
          {hasResults && (
            <div style={{ marginTop: 10 }}>
              <button className="btn no-print" onClick={onSet} disabled={busy}>
                <Icon name="flag" size={13} /> Set this run as baseline
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** Inline-editable label (chip) and notes, saved with PATCH /api/runs/{id}. */
function LabelNotes({ run, onSaved }: { run: Run; onSaved: (r: Run) => void }) {
  const [editing, setEditing] = useState(false);
  const [label, setLabel] = useState(run.label ?? '');
  const [notes, setNotes] = useState(run.notes ?? '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const start = () => {
    setLabel(run.label ?? '');
    setNotes(run.notes ?? '');
    setErr(null);
    setEditing(true);
  };
  const save = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.updateRun(run.id, { label: label.trim(), notes });
      onSaved({ ...run, ...r, label: r.label ?? label.trim(), notes: r.notes ?? notes });
      setEditing(false);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  if (editing) {
    return (
      <div className="card label-notes editing no-print">
        <div className="card-body stack" style={{ gap: 10 }}>
          <div className="row" style={{ gap: 12, alignItems: 'flex-start' }}>
            <div className="field" style={{ width: 220, flex: 'none' }}>
              <label htmlFor="run-label">Label</label>
              <input
                id="run-label"
                className="input mono"
                value={label}
                maxLength={100}
                placeholder="e.g. v1.4.2"
                autoFocus
                onChange={(e) => setLabel(e.target.value)}
                onKeyDown={(e) => {
                  // save() reports its own errors through setErr.
                  if (e.key === 'Enter') void save();
                  if (e.key === 'Escape') setEditing(false);
                }}
              />
            </div>
            <div className="field grow">
              <label htmlFor="run-notes">Notes</label>
              <textarea
                id="run-notes"
                className="input"
                rows={3}
                value={notes}
                maxLength={10000}
                placeholder="What changed, environment, links…"
                onChange={(e) => setNotes(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void save();
                  if (e.key === 'Escape') setEditing(false);
                }}
              />
            </div>
          </div>
          <ErrorAlert error={err} title="Could not save" />
          <div className="row" style={{ justifyContent: 'flex-end' }}>
            <button className="btn" onClick={() => setEditing(false)}>
              Cancel
            </button>
            <button className="btn primary" onClick={save} disabled={busy}>
              {busy ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      </div>
    );
  }
  const hasAny = (run.label ?? '').trim() || (run.notes ?? '').trim();
  return (
    <div className={`label-notes ${hasAny ? 'card' : ''}`}>
      {hasAny ? (
        <div className="ln-view">
          <span className="ln-key">Notes</span>
          <span className={`ln-notes ${run.notes ? '' : 'muted'}`}>{run.notes || 'No notes.'}</span>
          <button className="btn sm ghost no-print" onClick={start}>
            <Icon name="edit" size={12} /> Edit label & notes
          </button>
        </div>
      ) : (
        <div className="ln-empty no-print">
          <button className="linklike" onClick={start}>
            + Add a label and notes
          </button>
          <span className="muted"> — e.g. the version under test, so runs are easy to find and compare</span>
        </div>
      )}
    </div>
  );
}

export function StepTable({ summary, title }: { summary: Summary | undefined; title?: string }) {
  const steps = Object.entries(summary?.by_step ?? {});
  return (
    <div className="card" style={{ minWidth: 0 }}>
      <div className="card-head">
        <h2>Per step</h2>
        <span className="spacer" />
        {title && <span className="window-tag">{title}</span>}
      </div>
      {steps.length === 0 ? (
        <div className="empty">No per-step data.</div>
      ) : (
        <div className="table-wrap">
          <table className="t compact">
            <thead>
              <tr>
                <th>Step</th>
                <th className="num">Requests</th>
                <th className="num">Errors</th>
                <th className="num">Err %</th>
                <th className="num">RPS</th>
                <th className="num">Mean</th>
                <th className="num">p50</th>
                <th className="num">p90</th>
                <th className="num">p95</th>
                <th className="num">p99</th>
                <th className="num">Max</th>
              </tr>
            </thead>
            <tbody>
              {steps.map(([name, st]) => (
                <tr key={name}>
                  <td style={{ fontWeight: 600, whiteSpace: 'nowrap' }}>{name}</td>
                  <td className="num">{fmtInt(st.requests)}</td>
                  <td className={`num ${st.errors > 0 ? 'bad' : ''}`}>{fmtInt(st.errors)}</td>
                  <td className={`num ${st.error_rate > 0.01 ? 'bad' : ''}`}>{fmtPct(st.error_rate)}</td>
                  <td className="num">{fmtRps(st.rps)}</td>
                  <td className="num">{fmtMs(st.mean_ms)}</td>
                  <td className="num">{fmtMs(st.p50_ms)}</td>
                  <td className="num">{fmtMs(st.p90_ms)}</td>
                  <td className="num">{fmtMs(st.p95_ms)}</td>
                  <td className="num">{fmtMs(st.p99_ms)}</td>
                  <td className="num">{fmtMs(st.max_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function CompareDialog({ run, onClose }: { run: Run; onClose: () => void }) {
  const nav = useNavigate();
  const runs = useAsync(() => api.listRuns({ limit: 100 }), []);
  const candidates = (runs.data ?? []).filter((r) => r.id !== run.id && isFinished(r.status));
  const same = candidates.filter((r) => r.test_id === run.test_id);
  const other = candidates.filter((r) => r.test_id !== run.test_id);
  const [pick, setPick] = useState('');
  useEffect(() => {
    if (!pick && candidates.length) setPick((same[0] ?? candidates[0]).id);
  }, [candidates, same, pick]);

  const label = (r: Run) =>
    `${r.test_name}${r.label ? ` · ${r.label}` : ''} · ${fmtDateTime(r.started_at)} · ${r.status}${r.summary ? ` · p95 ${fmtMs(r.summary.p95_ms)}` : ''}`;

  return (
    <Modal
      title="Compare with…"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!pick} onClick={() => nav(`/compare?runs=${pick},${run.id}`)}>
            Compare
          </button>
        </>
      }
    >
      {runs.loading ? (
        <Loading />
      ) : candidates.length === 0 ? (
        <div className="muted">No other finished runs to compare with.</div>
      ) : (
        <div className="field">
          <label>Reference run</label>
          <select className="input" value={pick} onChange={(e) => setPick(e.target.value)}>
            {same.length > 0 && (
              <optgroup label={`Same test (${run.test_name})`}>
                {same.map((r) => (
                  <option key={r.id} value={r.id}>
                    {label(r)}
                  </option>
                ))}
              </optgroup>
            )}
            {other.length > 0 && (
              <optgroup label="Other tests">
                {other.map((r) => (
                  <option key={r.id} value={r.id}>
                    {label(r)}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
          <div className="hint">This run is compared against the reference. Add more runs (up to 6) on the compare page.</div>
        </div>
      )}
    </Modal>
  );
}
