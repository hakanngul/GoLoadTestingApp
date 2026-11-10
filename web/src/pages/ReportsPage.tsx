import { useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '../api/client';
import type { TestHealth } from '../api/types';
import { ChartCard, StackedBarChart, usePalette, type Series } from '../components/charts';
import { BaselineBadge, OverallBadge, PassRateBar, RunLabel } from '../components/report';
import { ErrorAlert, Kpi, Loading, StatusBadge } from '../components/ui';
import { fmtCompact, fmtDateTime, fmtInt, fmtMs, fmtPct, timeAgo } from '../lib/format';
import { useAsync, useNow } from '../lib/hooks';
import { fmtShortDate } from '../lib/report';

const PERIODS = [7, 30, 90] as const;

export function ReportsPage() {
  const [params, setParams] = useSearchParams();
  const nav = useNavigate();
  const daysParam = Number(params.get('days'));
  const days = (PERIODS as readonly number[]).includes(daysParam) ? daysParam : 30;
  const ov = useAsync(() => api.overview(days), [days], 30000);
  // TestHealth has no baseline id; the test list tells which run it is.
  const testList = useAsync(() => api.listTests(), []);
  const baselineOf = useMemo(
    () => new Map((testList.data ?? []).map((t) => [t.id, t.baseline_run_id ?? ''])),
    [testList.data],
  );
  const now = useNow(30000);
  const pal = usePalette();
  const d = ov.data;

  const finished = d ? d.passed + d.failed + d.errored : 0;
  const passRate = finished ? (d?.passed ?? 0) / finished : NaN;
  const tests = d?.tests ?? [];
  const regressed = tests.filter((t) => t.baseline?.regression).length;

  const daySeries: Series[] = useMemo(
    () => [
      { key: 'passed', label: 'Passed', color: pal['--series-green'] },
      { key: 'failed', label: 'Failed', color: pal['--series-red'] },
      { key: 'other', label: 'Error / stopped', color: pal['--series-muted'] },
    ],
    [pal],
  );
  const dayRows = useMemo(
    () => (d?.runs_per_day ?? []).map((x) => ({ day: x.day, passed: x.passed, failed: x.failed, other: x.other })),
    [d],
  );
  const dayFmt = (v: string) => fmtShortDate(`${v}T12:00:00Z`);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Reports</h1>
          <div className="page-sub">
            Results across all tests{d ? ` since ${fmtDateTime(d.since).replace(/,? \d\d:\d\d:\d\d$/, '')}` : ''}
          </div>
        </div>
        <span className="spacer" />
        <div className="segmented" role="group" aria-label="Period">
          {PERIODS.map((p) => (
            <button key={p} className={p === days ? 'on' : ''} onClick={() => setParams(p === 30 ? {} : { days: String(p) })}>
              {p} days
            </button>
          ))}
        </div>
      </div>

      {ov.error && !d ? <ErrorAlert error={ov.error} title="Failed to load the overview" /> : null}
      {!d ? (
        ov.loading ? <Loading /> : null
      ) : (
        <div className="stack" style={{ gap: 12 }}>
          <div className="kpis">
            <Kpi label="Runs" value={fmtInt(d.runs)} sub={`${tests.length} test${tests.length === 1 ? '' : 's'} · last ${days} days`} />
            <Kpi
              label="Pass rate"
              value={Number.isFinite(passRate) ? Math.round(passRate * 100) : '—'}
              unit={Number.isFinite(passRate) ? '%' : undefined}
              tone={passRate < 0.6 ? 'bad' : passRate < 0.9 ? 'warn' : undefined}
              sub={`${fmtInt(d.passed)} of ${fmtInt(finished)} finished`}
            />
            <Kpi
              label="Failed"
              value={fmtInt(d.failed)}
              tone={d.failed > 0 ? 'bad' : undefined}
              sub={`${fmtInt(d.errored)} error / stopped`}
            />
            <Kpi
              label="Regressions"
              value={fmtInt(regressed)}
              tone={regressed > 0 ? 'bad' : undefined}
              sub="latest run vs baseline"
            />
            <Kpi label="Total requests" value={fmtCompact(d.requests)} sub={`${fmtInt(d.requests)} sent`} />
          </div>

          <div className="grid-2" style={{ gap: 12, gridTemplateColumns: 'minmax(0, 3fr) minmax(0, 2fr)' }}>
            <ChartCard title="Runs per day" series={daySeries}>
              <StackedBarChart data={dayRows} series={daySeries} xKey="day" xFmt={dayFmt} height={236} />
            </ChartCard>
            <div className="card" style={{ minWidth: 0 }}>
              <div className="card-head">
                <h2>Recent runs</h2>
                <span className="spacer" />
                <Link to="/runs" className="text-2" style={{ fontSize: 12 }}>
                  All runs →
                </Link>
              </div>
              {(d.recent_runs ?? []).length === 0 ? (
                <div className="empty">No runs in this period.</div>
              ) : (
                <div className="recent-list">
                  {(d.recent_runs ?? []).slice(0, 8).map((r) => (
                    <Link key={r.id} to={`/runs/${r.id}`} className="recent-item">
                      <StatusBadge status={r.status} />
                      <span className="recent-name">
                        <span className="recent-test">{r.test_name}</span>
                        <RunLabel label={r.label} />
                      </span>
                      <span className="num text-2 recent-p95">{r.summary ? `p95 ${fmtMs(r.summary.p95_ms)}` : ''}</span>
                      <span className="muted recent-when" title={fmtDateTime(r.started_at)}>
                        {timeAgo(r.started_at, now)}
                      </span>
                    </Link>
                  ))}
                </div>
              )}
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <h2>Test health</h2>
              <span className="muted" style={{ fontSize: 12 }}>
                latest finished run · compared with the test's baseline over the steady-state window
              </span>
            </div>
            {tests.length === 0 ? (
              <div className="empty">
                <strong>No runs yet</strong>
                Run a test to see its health here.
              </div>
            ) : (
              <div className="table-wrap">
                <table className="t health">
                  <thead>
                    <tr>
                      <th>Test</th>
                      <th>Latest run</th>
                      <th className="num">p95</th>
                      <th className="num">Error rate</th>
                      <th>vs baseline</th>
                      <th style={{ width: 150 }}>Pass rate</th>
                      <th className="num">Runs</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {tests.map((t) => (
                      <HealthRow
                        key={t.test_id}
                        t={t}
                        now={now}
                        baselineId={baselineOf.get(t.test_id)}
                        onOpen={() => nav(`/reports/tests/${t.test_id}`)} />
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

function HealthRow({
  t,
  now,
  baselineId,
  onOpen,
}: {
  t: TestHealth;
  now: number;
  /** undefined = unknown (test list not loaded), '' = none. */
  baselineId: string | undefined;
  onOpen: () => void;
}) {
  const r = t.latest_run;
  const s = r?.summary;
  return (
    <tr style={{ cursor: 'pointer' }} onClick={onOpen}>
      <td className="name-cell">
        <Link to={`/reports/tests/${t.test_id}`} onClick={(e) => e.stopPropagation()}>
          {t.test_name}
        </Link>
      </td>
      <td>
        {r ? (
          <div className="row" style={{ gap: 6 }}>
            <StatusBadge status={r.status} />
            <RunLabel label={r.label} />
            <Link
              to={`/runs/${r.id}`}
              className="muted"
              style={{ fontSize: 12 }}
              title={fmtDateTime(r.started_at)}
              onClick={(e) => e.stopPropagation()}
            >
              {timeAgo(r.started_at, now)}
            </Link>
          </div>
        ) : (
          <span className="muted">—</span>
        )}
      </td>
      <td className="num">{fmtMs(s?.p95_ms)}</td>
      <td className={`num ${s && s.error_rate > 0.01 ? 'bad' : ''}`}>{fmtPct(s?.error_rate)}</td>
      <td>
        {r && baselineId === r.id ? (
          <BaselineBadge title="The latest run is the baseline" />
        ) : (
          <OverallBadge c={t.baseline} empty={<span className="muted">{baselineId === undefined ? '—' : 'No baseline'}</span>} />
        )}
      </td>
      <td>
        <PassRateBar rate={t.pass_rate} runs={t.run_count} />
      </td>
      <td className="num">{fmtInt(t.run_count)}</td>
      <td className="actions">
        <Link className="btn sm ghost" to={`/reports/tests/${t.test_id}`} onClick={(e) => e.stopPropagation()}>
          Report →
        </Link>
      </td>
    </tr>
  );
}
