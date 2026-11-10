import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { api } from '../api/client';
import { isFinished, type MetricPoint, type Run, type Summary } from '../api/types';
import { ChartCard, TimeSeriesChart, usePalette, yMs, yPct, type Series } from '../components/charts';
import { Delta, ErrorAlert, Loading, StatusBadge } from '../components/ui';
import { fmtDateTime, fmtInt, fmtMs, fmtPct, fmtRps, fmtSeconds, runDurationS, shortId } from '../lib/format';
import { useAsync } from '../lib/hooks';

interface Loaded {
  run: Run;
  metrics: MetricPoint[];
}

async function loadRun(id: string): Promise<Loaded | null> {
  if (!id) return null;
  const [run, metrics] = await Promise.all([api.getRun(id), api.runMetrics(id)]);
  return { run, metrics };
}

type Row = Record<string, number | null>;

function overlay(a: MetricPoint[], b: MetricPoint[], key: keyof MetricPoint): Row[] {
  const m = new Map<number, Row>();
  const put = (pts: MetricPoint[], col: 'a' | 'b') => {
    for (const p of pts) {
      const k = Math.round(p.elapsed);
      const row = m.get(k) ?? { elapsed: k, a: null, b: null };
      row[col] = p[key] as number;
      m.set(k, row);
    }
  };
  put(a, 'a');
  put(b, 'b');
  return [...m.values()].sort((x, y) => (x.elapsed as number) - (y.elapsed as number));
}

const METRICS: { label: string; get: (s: Summary) => number; fmt: (v: number) => string; lower: boolean; points?: boolean }[] = [
  { label: 'Requests', get: (s) => s.requests, fmt: fmtInt, lower: false },
  { label: 'Throughput (req/s)', get: (s) => s.rps, fmt: fmtRps, lower: false },
  { label: 'Error rate', get: (s) => s.error_rate, fmt: (v) => fmtPct(v), lower: true, points: true },
  { label: 'Dropped', get: (s) => s.dropped, fmt: fmtInt, lower: true },
  { label: 'Mean', get: (s) => s.mean_ms, fmt: fmtMs, lower: true },
  { label: 'p50', get: (s) => s.p50_ms, fmt: fmtMs, lower: true },
  { label: 'p90', get: (s) => s.p90_ms, fmt: fmtMs, lower: true },
  { label: 'p95', get: (s) => s.p95_ms, fmt: fmtMs, lower: true },
  { label: 'p99', get: (s) => s.p99_ms, fmt: fmtMs, lower: true },
  { label: 'Max', get: (s) => s.max_ms, fmt: fmtMs, lower: true },
];

export function ComparePage() {
  const [params, setParams] = useSearchParams();
  const a = params.get('a') ?? '';
  const b = params.get('b') ?? '';
  const runs = useAsync(() => api.listRuns({ limit: 200 }), []);
  const ra = useAsync(() => loadRun(a), [a]);
  const rb = useAsync(() => loadRun(b), [b]);
  const pal = usePalette();

  const options = (runs.data ?? []).filter((r) => isFinished(r.status));
  const setSide = (side: 'a' | 'b', v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(side, v);
    else next.delete(side);
    setParams(next);
  };

  const A = ra.data ?? null;
  const B = rb.data ?? null;
  const series = (what: string): Series[] => [
    { key: 'a', label: `A · ${what}`, color: pal['--series-1'] },
    { key: 'b', label: `B · ${what}`, color: pal['--series-2'] },
  ];
  const p95Rows = useMemo(() => (A && B ? overlay(A.metrics, B.metrics, 'p95_ms') : []), [A, B]);
  const rpsRows = useMemo(() => (A && B ? overlay(A.metrics, B.metrics, 'rps') : []), [A, B]);
  const errRows = useMemo(() => (A && B ? overlay(A.metrics, B.metrics, 'error_rate') : []), [A, B]);

  const label = (r: Run) =>
    `${r.test_name} · ${fmtDateTime(r.started_at)} · ${r.status}${r.summary ? ` · p95 ${fmtMs(r.summary.p95_ms)}` : ''}`;

  const picker = (side: 'a' | 'b', value: string) => (
    <div className="field" style={{ flex: 1, minWidth: 0 }}>
      <label className="row" style={{ gap: 6 }}>
        <span
          style={{
            width: 10,
            height: 10,
            borderRadius: 2,
            background: side === 'a' ? pal['--series-1'] : pal['--series-2'],
            display: 'inline-block',
          }}
        />
        Run {side.toUpperCase()} {side === 'a' ? '(baseline)' : '(candidate)'}
      </label>
      <select className="input" value={value} onChange={(e) => setSide(side, e.target.value)}>
        <option value="">— select a run —</option>
        {value && !options.some((r) => r.id === value) && <option value={value}>{shortId(value)}</option>}
        {options.map((r) => (
          <option key={r.id} value={r.id}>
            {label(r)}
          </option>
        ))}
      </select>
    </div>
  );

  const sa = A?.run.summary;
  const sb = B?.run.summary;
  const stepNames = useMemo(() => {
    const names = new Set<string>([...Object.keys(sa?.by_step ?? {}), ...Object.keys(sb?.by_step ?? {})]);
    return [...names];
  }, [sa, sb]);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Compare runs</h1>
          <div className="page-sub">Deltas show B relative to A · green = better, red = worse</div>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-body row" style={{ gap: 16, alignItems: 'flex-end' }}>
          {picker('a', a)}
          <button
            className="btn"
            title="Swap A and B"
            onClick={() => {
              const next = new URLSearchParams(params);
              if (b) next.set('a', b);
              else next.delete('a');
              if (a) next.set('b', a);
              else next.delete('b');
              setParams(next);
            }}
          >
            ⇄ Swap
          </button>
          {picker('b', b)}
        </div>
      </div>

      {ra.error ? <ErrorAlert error={ra.error} title="Run A" /> : null}
      {rb.error ? <ErrorAlert error={rb.error} title="Run B" /> : null}

      {!a || !b ? (
        <div className="card">
          <div className="empty">
            <strong>Select two runs</strong>
            Pick a baseline (A) and a candidate (B) to compare their results side by side.
          </div>
        </div>
      ) : (ra.loading && !A) || (rb.loading && !B) ? (
        <Loading />
      ) : A && B ? (
        <div className="stack" style={{ gap: 12 }}>
          <div className="card">
            <div className="table-wrap">
              <table className="t">
                <thead>
                  <tr>
                    <th style={{ width: '24%' }}>Metric</th>
                    <th className="num">
                      A · <Link className="mono" style={{ textTransform: 'none' }} to={`/runs/${A.run.id}`}>{shortId(A.run.id)}</Link>
                    </th>
                    <th className="num">
                      B · <Link className="mono" style={{ textTransform: 'none' }} to={`/runs/${B.run.id}`}>{shortId(B.run.id)}</Link>
                    </th>
                    <th className="num">Δ B vs A</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td className="text-2">Test</td>
                    <td className="num">{A.run.test_name}</td>
                    <td className="num">{B.run.test_name}</td>
                    <td />
                  </tr>
                  <tr>
                    <td className="text-2">Status</td>
                    <td className="num">
                      <StatusBadge status={A.run.status} />
                    </td>
                    <td className="num">
                      <StatusBadge status={B.run.status} />
                    </td>
                    <td />
                  </tr>
                  <tr>
                    <td className="text-2">Started · duration</td>
                    <td className="num">
                      {fmtDateTime(A.run.started_at)} · {fmtSeconds(runDurationS(A.run.started_at, A.run.finished_at))}
                    </td>
                    <td className="num">
                      {fmtDateTime(B.run.started_at)} · {fmtSeconds(runDurationS(B.run.started_at, B.run.finished_at))}
                    </td>
                    <td />
                  </tr>
                  {METRICS.map((m) => {
                    const va = sa ? m.get(sa) : undefined;
                    const vb = sb ? m.get(sb) : undefined;
                    return (
                      <tr key={m.label}>
                        <td style={{ fontWeight: 600 }}>{m.label}</td>
                        <td className="num">{va !== undefined ? m.fmt(va) : '—'}</td>
                        <td className="num">{vb !== undefined ? m.fmt(vb) : '—'}</td>
                        <td className="num">
                          <Delta a={va} b={vb} lowerIsBetter={m.lower} asPoints={m.points} />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>

          <div className="grid-2" style={{ gap: 12 }}>
            <ChartCard title="Latency p95 (ms)" series={series('p95')}>
              <TimeSeriesChart data={p95Rows} series={series('p95')} fmt={fmtMs} yFmt={yMs} emptyText="No metrics" />
            </ChartCard>
            <ChartCard title="Throughput (req/s)" series={series('rps')}>
              <TimeSeriesChart data={rpsRows} series={series('rps')} fmt={fmtRps} emptyText="No metrics" />
            </ChartCard>
          </div>
          <ChartCard title="Error rate" series={series('error rate')}>
            <TimeSeriesChart
              data={errRows}
              series={series('error rate')}
              fmt={(v) => fmtPct(v)}
              yFmt={yPct}
              height={180}
              emptyText="No metrics"
            />
          </ChartCard>

          <div className="card">
            <div className="card-head">
              <h2>Per step</h2>
            </div>
            {stepNames.length === 0 ? (
              <div className="empty">No per-step data.</div>
            ) : (
              <div className="table-wrap">
                <table className="t compact">
                  <thead>
                    <tr>
                      <th rowSpan={2}>Step</th>
                      <th className="num" colSpan={3}>
                        p95
                      </th>
                      <th className="num" colSpan={3}>
                        p99
                      </th>
                      <th className="num" colSpan={3}>
                        RPS
                      </th>
                      <th className="num" colSpan={3}>
                        Error rate
                      </th>
                    </tr>
                    <tr>
                      {[0, 1, 2, 3].flatMap((i) => [
                        <th key={`a${i}`} className="num">
                          A
                        </th>,
                        <th key={`b${i}`} className="num">
                          B
                        </th>,
                        <th key={`d${i}`} className="num">
                          Δ
                        </th>,
                      ])}
                    </tr>
                  </thead>
                  <tbody>
                    {stepNames.map((n) => {
                      const x = sa?.by_step?.[n];
                      const y = sb?.by_step?.[n];
                      return (
                        <tr key={n}>
                          <td style={{ fontWeight: 600, whiteSpace: 'nowrap' }}>{n}</td>
                          <td className="num">{fmtMs(x?.p95_ms)}</td>
                          <td className="num">{fmtMs(y?.p95_ms)}</td>
                          <td className="num">
                            <Delta a={x?.p95_ms} b={y?.p95_ms} lowerIsBetter />
                          </td>
                          <td className="num">{fmtMs(x?.p99_ms)}</td>
                          <td className="num">{fmtMs(y?.p99_ms)}</td>
                          <td className="num">
                            <Delta a={x?.p99_ms} b={y?.p99_ms} lowerIsBetter />
                          </td>
                          <td className="num">{fmtRps(x?.rps)}</td>
                          <td className="num">{fmtRps(y?.rps)}</td>
                          <td className="num">
                            <Delta a={x?.rps} b={y?.rps} lowerIsBetter={false} />
                          </td>
                          <td className="num">{fmtPct(x?.error_rate)}</td>
                          <td className="num">{fmtPct(y?.error_rate)}</td>
                          <td className="num">
                            <Delta a={x?.error_rate} b={y?.error_rate} lowerIsBetter asPoints />
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      ) : null}
    </>
  );
}
