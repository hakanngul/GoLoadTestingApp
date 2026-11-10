import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '../api/client';
import { ErrorAlert, Loading, StatusBadge } from '../components/ui';
import { fmtDateTime, fmtInt, fmtMs, fmtPct, fmtRps, fmtSeconds, runDurationS, shortId, timeAgo } from '../lib/format';
import { useAsync, useNow } from '../lib/hooks';

export function RunsPage() {
  const [params, setParams] = useSearchParams();
  const nav = useNavigate();
  const testId = params.get('test') ?? '';
  const tests = useAsync(() => api.listTests(), []);
  const runs = useAsync(() => api.listRuns({ testId: testId || undefined, limit: 200 }), [testId], 5000);
  const now = useNow(5000);
  const list = runs.data ?? [];

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Runs</h1>
          <div className="page-sub">Execution history, newest first</div>
        </div>
        <span className="spacer" />
        <label className="label" htmlFor="test-filter" style={{ margin: 0 }}>
          Test
        </label>
        <select
          id="test-filter"
          className="input"
          style={{ width: 220 }}
          value={testId}
          onChange={(e) => {
            const v = e.target.value;
            setParams(v ? { test: v } : {});
          }}
        >
          <option value="">All tests</option>
          {(tests.data ?? []).map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </select>
      </div>

      {runs.error && !runs.data ? <ErrorAlert error={runs.error} title="Failed to load runs" /> : null}

      <div className="card">
        {runs.loading && !runs.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <div className="empty">
            <strong>No runs yet</strong>
            Start a run from the <Link to="/">Tests</Link> page.
          </div>
        ) : (
          <div className="table-wrap">
            <table className="t">
              <thead>
                <tr>
                  <th>Run</th>
                  <th>Test</th>
                  <th>Status</th>
                  <th>Started</th>
                  <th className="num">Duration</th>
                  <th className="num">Requests</th>
                  <th className="num">RPS</th>
                  <th className="num">p95</th>
                  <th className="num">p99</th>
                  <th className="num">Errors</th>
                  <th className="num">Workers</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((r) => {
                  const s = r.summary;
                  return (
                    <tr key={r.id} style={{ cursor: 'pointer' }} onClick={() => nav(`/runs/${r.id}`)}>
                      <td>
                        <Link to={`/runs/${r.id}`} className="mono" onClick={(e) => e.stopPropagation()}>
                          {shortId(r.id)}
                        </Link>
                      </td>
                      <td style={{ fontWeight: 600 }}>{r.test_name}</td>
                      <td>
                        <StatusBadge status={r.status} />
                      </td>
                      <td title={fmtDateTime(r.started_at)} className="text-2">
                        {timeAgo(r.started_at, now)}
                      </td>
                      <td className="num">{fmtSeconds(runDurationS(r.started_at, r.finished_at))}</td>
                      <td className="num">{fmtInt(s?.requests)}</td>
                      <td className="num">{fmtRps(s?.rps)}</td>
                      <td className="num">{fmtMs(s?.p95_ms)}</td>
                      <td className="num">{fmtMs(s?.p99_ms)}</td>
                      <td className={`num ${s && s.error_rate > 0.01 ? 'bad' : ''}`}>{fmtPct(s?.error_rate)}</td>
                      <td className="num">{r.workers || 1}</td>
                      <td className="actions">
                        {s && (
                          <Link
                            className="btn sm ghost"
                            to={`/compare?b=${r.id}`}
                            onClick={(e) => e.stopPropagation()}
                          >
                            Compare
                          </Link>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
