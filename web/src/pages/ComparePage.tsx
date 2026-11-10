import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { api, saveBlob } from '../api/client';
import { isFinished, type CompareResult, type MetricPoint, type Run, type RunReport, type Summary } from '../api/types';
import { ChartCard, runColors, TimeSeriesChart, usePalette, yMs, yPct, type Series } from '../components/charts';
import { DeltaText, OverallBadge, RunLabel, WindowControl } from '../components/report';
import { ErrorAlert, Icon, Loading, StatusBadge } from '../components/ui';
import { fmtDateTime, fmtInt, fmtMs, fmtPct, fmtRps, shortId } from '../lib/format';
import { useAsync, useDebounced } from '../lib/hooks';
import { trimPartialTail } from '../lib/metrics';
import {
  compareCsv,
  fmtMetric,
  fmtShortDate,
  MAX_COMPARE,
  metricInfo,
  METRIC_ORDER,
  moveItem,
  parseRunIds,
  parseTolerance,
  parseWindow,
  runName,
  verdictTone,
  windowLabel,
  windowParams,
  type WindowSel,
} from '../lib/report';

type Row = Record<string, number | null>;

/** Per-second rows keyed by elapsed second with one column per run (r0..r5). */
function overlay(series: MetricPoint[][], key: keyof MetricPoint): Row[] {
  const m = new Map<number, Row>();
  series.forEach((pts, i) => {
    for (const p of pts) {
      const k = Math.round(p.elapsed);
      const row = m.get(k) ?? { elapsed: k };
      row[`r${i}`] = p[key] as number;
      m.set(k, row);
    }
  });
  return [...m.values()].sort((x, y) => (x.elapsed as number) - (y.elapsed as number));
}

export function ComparePage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  // Keep run-id lists readable (commas unescaped) in the address bar.
  const setParams = (next: URLSearchParams) =>
    navigate({ search: `?${next.toString().replace(/%2C/gi, ',')}` }, { replace: true });
  const ids = useMemo(() => parseRunIds(params), [params]);
  const idsKey = ids.join(',');
  const win = parseWindow(params);
  const winKey = windowParams(win).toString();
  const tolParam = params.get('tol');
  const tolPct = tolParam !== null && parseTolerance(tolParam) !== null ? tolParam : '10';
  const [tolInput, setTolInput] = useState(tolPct);
  const tolDebounced = useDebounced(tolInput, 400);
  const tolerance = parseTolerance(tolPct) ?? 0.1;
  const pal = usePalette();
  const colors = runColors(pal);

  // Legacy ?a=&b= links: rewrite to ?runs=.
  useEffect(() => {
    if (params.get('runs') === null && (params.get('a') || params.get('b'))) {
      const next = new URLSearchParams(params);
      next.delete('a');
      next.delete('b');
      if (ids.length) next.set('runs', ids.join(','));
      setParams(next);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params, ids]);

  const update = (mut: (p: URLSearchParams) => void) => {
    const next = new URLSearchParams(params);
    next.delete('a');
    next.delete('b');
    mut(next);
    setParams(next);
  };
  const setIds = (list: string[]) =>
    update((p) => (list.length ? p.set('runs', list.join(',')) : p.delete('runs')));
  const setWindow = (w: WindowSel) =>
    update((p) => {
      for (const k of ['window', 'from', 'to']) p.delete(k);
      if (w.kind !== 'steady') windowParams(w).forEach((v, k) => p.set(k, v));
    });

  useEffect(() => {
    const t = parseTolerance(tolDebounced);
    if (t === null || tolDebounced === tolPct) return;
    update((p) => (tolDebounced.trim() === '10' ? p.delete('tol') : p.set('tol', tolDebounced.trim().replace(/%$/, ''))));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tolDebounced]);

  const allRuns = useAsync(() => api.listRuns({ limit: 200 }), []);
  const result = useAsync<CompareResult | null>(
    () => (ids.length >= 2 ? api.compare(ids, win, tolerance) : Promise.resolve(null)),
    [idsKey, winKey, tolerance],
  );
  const metrics = useAsync<MetricPoint[][]>(
    () =>
      Promise.all(
        ids.map((id) =>
          api
            .runMetrics(id)
            .then(trimPartialTail)
            .catch(() => [] as MetricPoint[]),
        ),
      ),
    [idsKey],
  );

  const known = useMemo(() => new Map((allRuns.data ?? []).map((r) => [r.id, r])), [allRuns.data]);
  const res = result.data && result.data.runs.map((r) => r.run.id).join(',') === idsKey ? result.data : null;
  const reports = res?.runs ?? [];

  const exportCsv = () => {
    if (!res) return;
    saveBlob(new Blob([compareCsv(res)], { type: 'text/csv;charset=utf-8' }), `goload-compare-${ids.map(shortId).join('-')}.csv`);
  };

  const runSeries: Series[] = ids.map((id, i) => {
    const r = reports[i]?.run ?? known.get(id);
    return { key: `r${i}`, label: r ? `${runName(r)}${i === 0 ? ' (ref)' : ''}` : shortId(id), color: colors[i] };
  });
  const pts = metrics.data && metrics.data.length === ids.length ? metrics.data : [];
  const p95Rows = useMemo(() => overlay(pts, 'p95_ms'), [pts]);
  const rpsRows = useMemo(() => overlay(pts, 'rps'), [pts]);
  const errRows = useMemo(() => overlay(pts, 'error_rate'), [pts]);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Compare runs</h1>
          <div className="page-sub">
            First run is the reference · deltas are relative to it · <span className="good">green</span> better,{' '}
            <span className="bad">red</span> worse beyond the tolerance
          </div>
        </div>
        <span className="spacer" />
        <button className="btn" onClick={exportCsv} disabled={!res}>
          <Icon name="download" size={14} /> Export CSV
        </button>
      </div>

      <div className="card compare-setup" style={{ marginBottom: 12 }}>
        <div className="card-head">
          <h2>Runs</h2>
          <span className="muted num" style={{ fontSize: 12 }}>
            {ids.length}/{MAX_COMPARE} · drag to reorder
          </span>
          <span className="spacer" />
          <WindowControl value={win} onChange={setWindow} allowCustom={false} />
          <label className="tol-field" title="Changes smaller than this are shown as “same” (error rate: a tenth of it, in points)">
            <span className="wc-label">Tolerance</span>
            <span className="input-addon">
              <input
                className={`input num ${parseTolerance(tolInput) === null ? 'invalid' : ''}`}
                style={{ width: 56 }}
                value={tolInput}
                inputMode="decimal"
                onChange={(e) => setTolInput(e.target.value)}
              />
              <span className="addon">%</span>
            </span>
          </label>
        </div>
        <div className="card-body">
          <RunSlots
            ids={ids}
            runFor={(id, i) => reports[i]?.run ?? known.get(id)}
            colors={colors}
            onChange={setIds}
            candidates={(allRuns.data ?? []).filter((r) => isFinished(r.status) && !!r.summary && !ids.includes(r.id))}
            loadingCandidates={allRuns.loading && !allRuns.data}
          />
        </div>
      </div>

      {result.error ? <ErrorAlert error={result.error} title="Compare failed" /> : null}

      {ids.length < 2 ? (
        <div className="card">
          <div className="empty">
            <strong>Select at least two runs</strong>
            The first run is the reference; up to {MAX_COMPARE - 1} more are compared against it. Tip: open a{' '}
            <Link to="/reports">test report</Link> and tick runs to compare.
          </div>
        </div>
      ) : !res ? (
        result.loading ? <Loading /> : null
      ) : (
        <div className="stack" style={{ gap: 12 }}>
          <SummaryMatrix res={res} colors={colors} />

          <div className="grid-2" style={{ gap: 12 }}>
            <ChartCard title="Latency p95 (ms)" series={runSeries}>
              <TimeSeriesChart data={p95Rows} series={runSeries} fmt={fmtMs} yFmt={yMs} emptyText="No metrics" />
            </ChartCard>
            <ChartCard title="Throughput (req/s)" series={runSeries}>
              <TimeSeriesChart data={rpsRows} series={runSeries} fmt={fmtRps} emptyText="No metrics" />
            </ChartCard>
          </div>
          <ChartCard title="Error rate" series={runSeries}>
            <TimeSeriesChart data={errRows} series={runSeries} fmt={(v) => fmtPct(v)} yFmt={yPct} height={180} emptyText="No metrics" />
          </ChartCard>
          <div className="muted" style={{ fontSize: 11.5, marginTop: -4 }}>
            Charts are aligned on seconds since each run's start and show the whole runs; the tables use the {windowLabel(win).toLowerCase()}{' '}
            window.
          </div>

          <StepCompare res={res} colors={colors} />
        </div>
      )}
    </>
  );
}

// ---------- run slots (selection, reference, reorder, picker) ----------

function RunSlots({
  ids,
  runFor,
  colors,
  onChange,
  candidates,
  loadingCandidates,
}: {
  ids: string[];
  runFor: (id: string, i: number) => Run | undefined;
  colors: string[];
  onChange: (ids: string[]) => void;
  candidates: Run[];
  loadingCandidates: boolean;
}) {
  const [dragFrom, setDragFrom] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const [picking, setPicking] = useState(false);
  return (
    <div className="run-slots">
      {ids.map((id, i) => {
        const r = runFor(id, i);
        return (
          <div
            key={id}
            className={`run-slot ${i === 0 ? 'ref' : ''} ${over === i && dragFrom !== null && dragFrom !== i ? 'drop' : ''}`}
            draggable
            onDragStart={(e) => {
              setDragFrom(i);
              e.dataTransfer.effectAllowed = 'move';
            }}
            onDragOver={(e) => {
              e.preventDefault();
              setOver(i);
            }}
            onDragLeave={() => setOver((o) => (o === i ? null : o))}
            onDrop={(e) => {
              e.preventDefault();
              if (dragFrom !== null) onChange(moveItem(ids, dragFrom, i));
              setDragFrom(null);
              setOver(null);
            }}
            onDragEnd={() => {
              setDragFrom(null);
              setOver(null);
            }}
          >
            <span className="slot-grip" aria-hidden="true">
              <Icon name="grip" size={14} />
            </span>
            <span className="slot-swatch" style={{ background: colors[i] }} />
            <div className="slot-body">
              <div className="slot-title">
                {r?.label ? <RunLabel label={r.label} /> : <span className="slot-name">{r ? fmtShortDate(r.started_at, true) : shortId(id)}</span>}
                {r && <span className={`status-dot ${r.status}`} title={r.status} />}
              </div>
              <div className="slot-sub">
                {r ? (
                  <>
                    <Link to={`/runs/${id}`}>{r.test_name}</Link>
                    {r.label ? ` · ${fmtShortDate(r.started_at, true)}` : ''}
                  </>
                ) : (
                  <span className="mono">{shortId(id)}</span>
                )}
              </div>
            </div>
            <div className="slot-actions">
              {i === 0 ? (
                <span className="ref-badge">Reference</span>
              ) : (
                <button className="btn sm ghost" onClick={() => onChange(moveItem(ids, i, 0))} title="Compare the other runs against this one">
                  Make reference
                </button>
              )}
              <button className="btn ghost icon sm" aria-label="Remove run" title="Remove" onClick={() => onChange(ids.filter((x) => x !== id))}>
                <Icon name="x" size={12} />
              </button>
            </div>
          </div>
        );
      })}
      {ids.length < MAX_COMPARE && (
        <div className="picker-anchor">
          <button className="run-slot add" onClick={() => setPicking((p) => !p)}>
            <Icon name="plus" size={14} /> Add run
          </button>
          {picking && (
            <RunPicker
              runs={candidates}
              loading={loadingCandidates}
              onPick={(id) => {
                onChange([...ids, id]);
                if (ids.length + 1 >= MAX_COMPARE) setPicking(false);
              }}
              onClose={() => setPicking(false)}
            />
          )}
        </div>
      )}
    </div>
  );
}

function RunPicker({
  runs,
  loading,
  onPick,
  onClose,
}: {
  runs: Run[];
  loading: boolean;
  onPick: (id: string) => void;
  onClose: () => void;
}) {
  const [q, setQ] = useState('');
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node) && !(e.target as HTMLElement).closest('.run-slot.add')) onClose();
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [onClose]);
  const needle = q.trim().toLowerCase();
  const filtered = runs.filter(
    (r) =>
      !needle ||
      r.test_name.toLowerCase().includes(needle) ||
      (r.label ?? '').toLowerCase().includes(needle) ||
      r.id.toLowerCase().includes(needle) ||
      r.status.includes(needle),
  );
  const groups = new Map<string, Run[]>();
  for (const r of filtered) groups.set(r.test_name, [...(groups.get(r.test_name) ?? []), r]);
  return (
    <div className="run-picker" ref={ref} role="dialog" aria-label="Add a run">
      <div className="rp-search">
        <Icon name="search" size={14} />
        <input autoFocus placeholder="Search test, label, status or id…" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      <div className="rp-list">
        {loading ? (
          <Loading />
        ) : groups.size === 0 ? (
          <div className="empty" style={{ padding: 20 }}>
            No matching finished runs.
          </div>
        ) : (
          [...groups.entries()]
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([test, list]) => (
              <div key={test}>
                <div className="rp-group">{test}</div>
                {list.map((r) => (
                  <button key={r.id} className="rp-item" onClick={() => onPick(r.id)}>
                    <span className="rp-label">{r.label ? <RunLabel label={r.label} /> : <span className="muted">no label</span>}</span>
                    <span className="text-2 num">{fmtDateTime(r.started_at)}</span>
                    <StatusBadge status={r.status} />
                    <span className="num muted rp-p95">{r.summary ? `p95 ${fmtMs(r.summary.p95_ms)}` : ''}</span>
                  </button>
                ))}
              </div>
            ))
        )}
      </div>
    </div>
  );
}

// ---------- summary matrix ----------

const MATRIX: { key: string; extra?: (s: Summary) => string }[] = [
  { key: 'rps' },
  { key: 'error_rate' },
  { key: 'p50_ms' },
  { key: 'p90_ms' },
  { key: 'p95_ms' },
  { key: 'p99_ms' },
  { key: 'max_ms' },
  { key: 'mean_ms' },
];

function RunHead({ r, i, color }: { r: RunReport; i: number; color: string }) {
  return (
    <div className="mx-head">
      <div className="row" style={{ gap: 6, justifyContent: 'flex-end' }}>
        <span className="slot-swatch sm" style={{ background: color }} />
        <Link to={`/runs/${r.run.id}`} className="mx-name">
          {runName(r.run)}
        </Link>
      </div>
      <div className="mx-sub">
        {i === 0 ? <span className="ref-badge sm">Reference</span> : null} {r.run.test_name}
      </div>
    </div>
  );
}

function SummaryMatrix({ res, colors }: { res: CompareResult; colors: string[] }) {
  const runs = res.runs;
  return (
    <div className="card">
      <div className="card-head">
        <h2>Summary</h2>
        <span className="muted" style={{ fontSize: 12 }}>
          {windowLabel(runs[0].window)} window
        </span>
      </div>
      <div className="table-wrap">
        <table className="t matrix">
          <thead>
            <tr>
              <th style={{ width: 140 }}>Metric</th>
              {runs.map((r, i) => (
                <th key={r.run.id} className="num">
                  <RunHead r={r} i={i} color={colors[i]} />
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            <tr className="mx-verdict">
              <td className="text-2">Verdict</td>
              {runs.map((r, i) => (
                <td key={r.run.id} className="num">
                  {i === 0 ? <span className="muted">reference</span> : <OverallBadge c={res.comparisons[i - 1]} />}
                </td>
              ))}
            </tr>
            <tr>
              <td className="text-2">Status · window</td>
              {runs.map((r) => (
                <td key={r.run.id} className="num">
                  <span className="row" style={{ gap: 6, justifyContent: 'flex-end' }}>
                    <StatusBadge status={r.run.status} />
                    <span className="muted num" style={{ fontSize: 11.5 }}>
                      {windowLabel(r.window).replace(/^.*· /, '')}
                    </span>
                  </span>
                </td>
              ))}
            </tr>
            <tr>
              <td className="text-2">Requests</td>
              {runs.map((r) => (
                <td key={r.run.id} className="num">
                  {fmtInt(r.summary.requests)}
                </td>
              ))}
            </tr>
            {MATRIX.map((m) => (
              <tr key={m.key}>
                <td style={{ fontWeight: 600 }}>{metricInfo(m.key).label}</td>
                {runs.map((r, i) => {
                  const v = r.summary[m.key as keyof Summary] as number;
                  const d = i > 0 ? (res.comparisons[i - 1]?.metrics ?? []).find((x) => x.metric === m.key) : undefined;
                  return (
                    <td key={r.run.id} className={`num mx-cell ${d ? verdictTone(d.verdict) : ''}`}>
                      <span className="mx-v">{fmtMetric(m.key, v)}</span>
                      {i > 0 && (
                        <span className="mx-d">
                          <DeltaText d={d} />
                        </span>
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------- per step ----------

function StepCompare({ res, colors }: { res: CompareResult; colors: string[] }) {
  const steps = useMemo(() => {
    const names: string[] = [];
    for (const r of res.runs) for (const n of Object.keys(r.summary.by_step ?? {})) if (!names.includes(n)) names.push(n);
    return names;
  }, [res]);
  const [step, setStep] = useState('');
  const cur = steps.includes(step) ? step : steps[0] ?? '';
  return (
    <div className="card">
      <div className="card-head">
        <h2>Per step</h2>
        <span className="spacer" />
        {steps.length > 1 && (
          <div className="segmented" role="group" aria-label="Step">
            {steps.slice(0, 8).map((n) => (
              <button key={n} className={n === cur ? 'on' : ''} onClick={() => setStep(n)}>
                {n}
              </button>
            ))}
          </div>
        )}
        {steps.length > 8 && (
          <select className="input" style={{ width: 200 }} value={cur} onChange={(e) => setStep(e.target.value)}>
            {steps.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
        )}
      </div>
      {!cur ? (
        <div className="empty">No per-step data.</div>
      ) : (
        <div className="table-wrap">
          <table className="t matrix compact">
            <thead>
              <tr>
                <th style={{ width: 140 }}>{cur}</th>
                {res.runs.map((r, i) => (
                  <th key={r.run.id} className="num">
                    <RunHead r={r} i={i} color={colors[i]} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              <tr>
                <td className="text-2">Requests</td>
                {res.runs.map((r) => (
                  <td key={r.run.id} className="num">
                    {fmtInt(r.summary.by_step?.[cur]?.requests)}
                  </td>
                ))}
              </tr>
              {METRIC_ORDER.map((m) => (
                <tr key={m}>
                  <td style={{ fontWeight: 600 }}>{metricInfo(m).label}</td>
                  {res.runs.map((r, i) => {
                    const st = r.summary.by_step?.[cur];
                    const d = i > 0 ? (res.comparisons[i - 1]?.by_step?.[cur] ?? []).find((x) => x.metric === m) : undefined;
                    return (
                      <td key={r.run.id} className={`num mx-cell ${d ? verdictTone(d.verdict) : ''}`}>
                        <span className="mx-v">{st ? fmtMetric(m, st[m]) : '—'}</span>
                        {i > 0 && (
                          <span className="mx-d">
                            {st ? <DeltaText d={d} /> : <span className="delta same">not in run</span>}
                          </span>
                        )}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
