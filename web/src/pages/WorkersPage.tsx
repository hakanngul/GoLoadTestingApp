import { Link } from 'react-router-dom';
import { api } from '../api/client';
import { ErrorAlert, Icon, Loading, WorkerStateBadge } from '../components/ui';
import { fmtDateTime, shortId, timeAgo } from '../lib/format';
import { useAsync, useNow } from '../lib/hooks';

function CpuBar({ v }: { v: number }) {
  const pct = Math.round(Math.max(0, Math.min(1, v)) * 100);
  const color = v > 0.9 ? 'var(--bad)' : v > 0.75 ? 'var(--warn)' : 'var(--accent)';
  return (
    <div className="row" style={{ gap: 8, justifyContent: 'flex-end' }}>
      <div className="progress" style={{ width: 80 }}>
        <div style={{ width: `${pct}%`, background: color }} />
      </div>
      <span className="num" style={{ width: 36 }}>
        {pct}%
      </span>
    </div>
  );
}

export function WorkersPage() {
  const workers = useAsync(() => api.listWorkers(), [], 5000);
  const now = useNow(5000);
  const list = workers.data ?? [];
  const counts = list.reduce<Record<string, number>>((acc, w) => ({ ...acc, [w.state]: (acc[w.state] ?? 0) + 1 }), {});

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Workers</h1>
          <div className="page-sub">
            Load generators connected to this master · auto-refresh every 5s
            {list.length > 0 &&
              ` · ${list.length} total, ${counts.idle ?? 0} idle, ${counts.busy ?? 0} busy, ${counts.lost ?? 0} lost`}
          </div>
        </div>
        <span className="spacer" />
        <button className="btn" onClick={workers.reload}>
          <Icon name="refresh" size={14} /> Refresh
        </button>
      </div>

      {workers.error ? <ErrorAlert error={workers.error} title="Failed to load workers" /> : null}

      <div className="card">
        {workers.loading && !workers.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <div className="empty">
            <strong>No workers connected</strong>
            Runs execute on the built-in local generator. Distributed workers arrive in Phase 4.
          </div>
        ) : (
          <table className="t">
            <thead>
              <tr>
                <th>ID</th>
                <th>Address</th>
                <th>State</th>
                <th>Run</th>
                <th className="num">CPU</th>
                <th>Version</th>
                <th>Last seen</th>
              </tr>
            </thead>
            <tbody>
              {list.map((w) => (
                <tr key={w.id}>
                  <td className="mono" style={{ fontWeight: 600 }}>
                    {w.id}
                  </td>
                  <td className="mono text-2">{w.addr}</td>
                  <td>
                    <WorkerStateBadge state={w.state} />
                  </td>
                  <td>
                    {w.run_id ? (
                      <Link className="mono" to={`/runs/${w.run_id}`}>
                        {shortId(w.run_id)}
                      </Link>
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                  <td className="num">
                    <CpuBar v={w.cpu} />
                  </td>
                  <td className="mono text-2">{w.version || '—'}</td>
                  <td className={w.state === 'lost' ? 'bad' : 'text-2'} title={fmtDateTime(w.last_seen)}>
                    {timeAgo(w.last_seen, now)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
