import { Link } from 'react-router-dom';
import { api } from '../api/client';
import { ErrorAlert, Icon, Loading, WorkerStateBadge } from '../components/ui';
import { fmtDateTime, shortId, timeAgo } from '../lib/format';
import { useAsync, useNow } from '../lib/hooks';
import type { Worker } from '../api/types';
import { isMasterWorker } from '../lib/workers';

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

const stateOrder: Record<string, number> = { busy: 0, idle: 1, lost: 2 };

function WorkerRow({ w, now, master }: { w: Worker; now: number; master?: boolean }) {
  const lost = w.state === 'lost';
  return (
    <tr className={master ? 'row-master' : lost ? 'row-lost' : undefined}>
      <td style={{ fontWeight: 600 }}>
        {master ? (
          <span>
            master <span className="text-2" style={{ fontWeight: 400 }}>(in-process)</span>
          </span>
        ) : (
          <span className="mono">{w.id}</span>
        )}
      </td>
      <td className="mono text-2">{master ? <span className="muted">same process</span> : w.addr}</td>
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
      <td className="num">{lost ? <span className="muted">—</span> : <CpuBar v={w.cpu} />}</td>
      <td className="mono text-2">{w.version || '—'}</td>
      <td className="text-2" title={fmtDateTime(w.last_seen)}>
        {timeAgo(w.last_seen, now)}
      </td>
    </tr>
  );
}

export function WorkersPage() {
  const workers = useAsync(() => api.listWorkers(), [], 5000);
  const now = useNow(5000);
  const list = workers.data ?? [];
  const master = list.filter(isMasterWorker);
  const remote = list
    .filter((w) => !isMasterWorker(w))
    .sort((a, b) => (stateOrder[a.state] ?? 3) - (stateOrder[b.state] ?? 3) || a.id.localeCompare(b.id));
  const counts = remote.reduce<Record<string, number>>((acc, w) => ({ ...acc, [w.state]: (acc[w.state] ?? 0) + 1 }), {});

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Workers</h1>
          <div className="page-sub">
            Load generators connected to this master · auto-refresh every 5s
            {remote.length > 0 &&
              ` · ${remote.length} remote: ${counts.idle ?? 0} idle, ${counts.busy ?? 0} busy, ${counts.lost ?? 0} lost`}
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
            <strong>No workers reported</strong>
            The master did not report any load generators.
          </div>
        ) : (
          <table className="t">
            <thead>
              <tr>
                <th>Worker</th>
                <th>Address</th>
                <th>State</th>
                <th>Run</th>
                <th className="num">CPU</th>
                <th>Version</th>
                <th>Last seen</th>
              </tr>
            </thead>
            <tbody>
              {master.length > 0 && (
                <>
                  <tr className="row-section">
                    <td colSpan={7}>Master · runs with 1 worker and no remote workers execute here</td>
                  </tr>
                  {master.map((w) => (
                    <WorkerRow key={w.id} w={w} now={now} master />
                  ))}
                </>
              )}
              <tr className="row-section">
                <td colSpan={7}>
                  Remote workers · {remote.length - (counts.lost ?? 0)} connected
                  {counts.lost ? `, ${counts.lost} lost` : ''}
                </td>
              </tr>
              {remote.length === 0 ? (
                <tr>
                  <td colSpan={7} className="muted" style={{ padding: '18px 12px' }}>
                    No remote workers connected. Start one with <span className="mono">goloadtest worker</span>, or
                    configure a cloud provisioner on the master to launch spot instances on demand.
                  </td>
                </tr>
              ) : (
                remote.map((w) => <WorkerRow key={w.id} w={w} now={now} />)
              )}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
