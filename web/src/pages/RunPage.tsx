import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { api, subscribeRun } from '../api/client';
import { isFinished, type MetricPoint, type Run, type Summary } from '../api/types';
import { RunCharts } from '../components/charts';
import { ConfirmDialog, ErrorAlert, Icon, Kpi, Loading, Modal, StatusBadge } from '../components/ui';
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
import { useAsync, useNow } from '../lib/hooks';
import { OP_SYMBOL, summarizeYaml, unitFor } from '../lib/scenario';

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
  void now;

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
  const cpu = last?.cpu ?? 0;

  return (
    <>
      <RunHeader run={run}>
        <div className="stack" style={{ gap: 4, alignItems: 'flex-end' }}>
          <div className="num" style={{ fontSize: 15, fontWeight: 600 }}>
            {fmtClock(elapsed)} <span className="muted">/ {fmtClock(total)}</span>
          </div>
          <div className="progress">
            <div style={{ width: `${progress * 100}%` }} />
          </div>
        </div>
        <button className="btn danger-solid" onClick={() => setConfirmStop(true)} disabled={stopping}>
          <Icon name="stop" size={11} /> Stop
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
        <Kpi label="p95" value={last ? fmtMs(last.p95_ms) : '—'} sub={`max ${fmtMs(last?.max_ms)}`} />
        <Kpi label="p99" value={last ? fmtMs(last.p99_ms) : '—'} />
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

      {confirmStop && (
        <ConfirmDialog
          title="Stop this run?"
          message="Load generation stops immediately. Partial results are kept and the run is marked as stopped."
          confirmLabel="Stop run"
          busy={stopping}
          error={stopErr}
          onConfirm={stop}
          onCancel={() => setConfirmStop(false)}
        />
      )}
    </>
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

export function ResultView({ run }: { run: Run }) {
  const nav = useNavigate();
  const metrics = useAsync(() => api.runMetrics(run.id), [run.id]);
  const errors = useAsync(() => api.runErrors(run.id), [run.id]);
  const [compareOpen, setCompareOpen] = useState(false);
  const sum = useMemo(() => summarizeYaml(run.yaml), [run.yaml]);
  const unit = sum ? unitFor(sum.model) : 'it/s';
  const s = run.summary;
  const b = bannerInfo[run.status] ?? bannerInfo.error;
  const dur = runDurationS(run.started_at, run.finished_at);
  const thresholds = run.thresholds ?? [];
  const failedCount = thresholds.filter((t) => !t.passed).length;

  return (
    <>
      <RunHeader run={run}>
        <button className="btn" onClick={() => setCompareOpen(true)}>
          <Icon name="compare" size={14} /> Compare with…
        </button>
        <button className="btn" onClick={() => nav(`/tests/${run.test_id}`)}>
          <Icon name="edit" size={14} /> Edit test
        </button>
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
        </div>

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

        <div className="kpis">
          <Kpi label="Requests" value={fmtInt(s?.requests)} sub={`${fmtInt(s?.dropped)} dropped`} />
          <Kpi label="Throughput" value={fmtRps(s?.rps)} unit="req/s" sub={sum ? `peak target ${sum.peak} ${unit}` : undefined} />
          <Kpi
            label="Error rate"
            value={fmtPct(s?.error_rate)}
            tone={s && s.error_rate > 0.05 ? 'bad' : s && s.error_rate > 0.01 ? 'warn' : undefined}
            sub={`${fmtInt(s?.errors)} errors`}
          />
          <Kpi label="p50" value={fmtMs(s?.p50_ms)} sub={`mean ${fmtMs(s?.mean_ms)}`} />
          <Kpi label="p95" value={fmtMs(s?.p95_ms)} sub={`p90 ${fmtMs(s?.p90_ms)}`} />
          <Kpi label="p99" value={fmtMs(s?.p99_ms)} sub={`max ${fmtMs(s?.max_ms)}`} />
        </div>

        <div className="grid-2" style={{ gap: 12, gridTemplateColumns: 'minmax(0, 2fr) minmax(0, 3fr)' }}>
          <div className="card">
            <div className="card-head">
              <h2>Thresholds</h2>
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
                        {OP_SYMBOL[t.op] ?? t.op} {thresholdActual(t.metric, t.value)}
                      </td>
                      <td className={`num ${t.passed ? '' : 'bad'}`} style={{ fontWeight: 600 }}>
                        {thresholdActual(t.metric, t.actual)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
          <StepTable summary={s} />
        </div>

        {metrics.error ? <ErrorAlert error={metrics.error} title="Metrics unavailable" /> : null}
        <RunCharts
          points={metrics.data ?? []}
          targetUnit={unit}
          xMax={undefined}
        />

        <div className="card">
          <div className="card-head">
            <h2>Error samples</h2>
            <span className="spacer" />
            <span className="muted">{errors.data ? `${errors.data.length} kept` : ''}</span>
          </div>
          {errors.loading ? (
            <Loading />
          ) : errors.error ? (
            <div className="card-body">
              <ErrorAlert error={errors.error} />
            </div>
          ) : (errors.data ?? []).length === 0 ? (
            <div className="empty">No errors recorded.</div>
          ) : (
            <div className="table-wrap" style={{ maxHeight: 320, overflowY: 'auto' }}>
              <table className="t compact">
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>Step</th>
                    <th className="num">Status</th>
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
                      <td className="num mono">
                        <span className={e.status >= 500 || e.status === 0 ? 'bad' : 'warn'}>{e.status || '—'}</span>
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
        </div>

        <details className="card collapsible">
          <summary>Scenario YAML used</summary>
          <pre className="code">{run.yaml}</pre>
        </details>
      </div>

      {compareOpen && <CompareDialog run={run} onClose={() => setCompareOpen(false)} />}
    </>
  );
}

export function StepTable({ summary }: { summary: Summary | undefined }) {
  const steps = Object.entries(summary?.by_step ?? {});
  return (
    <div className="card" style={{ minWidth: 0 }}>
      <div className="card-head">
        <h2>Per step</h2>
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
    `${r.test_name} · ${fmtDateTime(r.started_at)} · ${r.status}${r.summary ? ` · p95 ${fmtMs(r.summary.p95_ms)}` : ''}`;

  return (
    <Modal
      title="Compare with…"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!pick} onClick={() => nav(`/compare?a=${pick}&b=${run.id}`)}>
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
          <label>Baseline run (A)</label>
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
          <div className="hint">This run will be shown as B, compared against the baseline A.</div>
        </div>
      )}
    </Modal>
  );
}
