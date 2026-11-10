import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '../api/client';
import type { Test } from '../api/types';
import { RunStartDialog } from '../components/RunStartDialog';
import { ConfirmDialog, ErrorAlert, Icon, Loading, StatusBadge } from '../components/ui';
import { fmtMs, fmtPct, fmtSeconds, timeAgo } from '../lib/format';
import { useAsync, useNow } from '../lib/hooks';
import { summarizeYaml, unitFor } from '../lib/scenario';

export function TestsPage() {
  const nav = useNavigate();
  const tests = useAsync(() => api.listTests(), [], 10000);
  const [runFor, setRunFor] = useState<Test | null>(null);
  const [deleting, setDeleting] = useState<Test | null>(null);
  const [delBusy, setDelBusy] = useState(false);
  const [delErr, setDelErr] = useState<unknown>(null);
  const now = useNow(15000);

  const doDelete = async () => {
    if (!deleting) return;
    setDelBusy(true);
    setDelErr(null);
    try {
      await api.deleteTest(deleting.id);
      setDeleting(null);
      tests.reload();
    } catch (e) {
      setDelErr(e);
    } finally {
      setDelBusy(false);
    }
  };

  const list = tests.data ?? [];

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Tests</h1>
          <div className="page-sub">Saved scenarios and their latest results</div>
        </div>
        <span className="spacer" />
        <Link to="/tests/new" className="btn primary">
          <Icon name="plus" size={14} /> New test
        </Link>
      </div>

      {tests.error && !tests.data ? <ErrorAlert error={tests.error} title="Failed to load tests" /> : null}

      <div className="card">
        {tests.loading && !tests.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <div className="empty">
            <strong>No tests yet</strong>
            Create a scenario to start load testing. <Link to="/tests/new">New test</Link>
          </div>
        ) : (
          <div className="table-wrap">
            <table className="t">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Profile</th>
                  <th>Last run</th>
                  <th className="num">p95</th>
                  <th className="num">Error rate</th>
                  <th className="num">RPS</th>
                  <th>When</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((t) => {
                  const sum = summarizeYaml(t.yaml);
                  const lr = t.last_run;
                  const s = lr?.summary;
                  return (
                    <tr key={t.id}>
                      <td className="name-cell">
                        <Link to={`/tests/${t.id}`}>{t.name}</Link>
                        <div className="sub">
                          {sum ? `${sum.steps} step${sum.steps === 1 ? '' : 's'}` : 'invalid YAML'}
                          {t.files && t.files.length > 0 ? ` · ${t.files.length} data file${t.files.length > 1 ? 's' : ''}` : ''}
                        </div>
                      </td>
                      <td className="text-2">
                        {sum ? (
                          <>
                            <span className="tag">{sum.model}</span>{' '}
                            <span className="num">
                              {sum.peak} {unitFor(sum.model)} · {fmtSeconds(sum.total)}
                            </span>
                          </>
                        ) : (
                          '—'
                        )}
                      </td>
                      <td>
                        {lr ? (
                          <Link to={`/runs/${lr.id}`} style={{ textDecoration: 'none' }}>
                            <StatusBadge status={lr.status} />
                          </Link>
                        ) : (
                          <StatusBadge status={undefined} />
                        )}
                      </td>
                      <td className="num">{fmtMs(s?.p95_ms)}</td>
                      <td className={`num ${s && s.error_rate > 0.01 ? 'bad' : ''}`}>{fmtPct(s?.error_rate)}</td>
                      <td className="num">{s ? s.rps.toFixed(1) : '—'}</td>
                      <td className="muted">{lr ? timeAgo(lr.started_at, now) : '—'}</td>
                      <td className="actions">
                        <button className="btn sm" onClick={() => setRunFor(t)} title="Run test">
                          <Icon name="play" size={11} /> Run
                        </button>
                        <button className="btn sm ghost" onClick={() => nav(`/tests/${t.id}`)} title="Edit">
                          <Icon name="edit" size={14} /> Edit
                        </button>
                        <button
                          className="btn sm ghost icon danger"
                          onClick={() => {
                            setDelErr(null);
                            setDeleting(t);
                          }}
                          title="Delete"
                          aria-label={`Delete ${t.name}`}
                        >
                          <Icon name="trash" size={14} />
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {runFor && <RunStartDialog test={runFor} onClose={() => setRunFor(null)} />}
      {deleting && (
        <ConfirmDialog
          title="Delete test?"
          message={
            <>
              <strong>{deleting.name}</strong> and its attached data files will be deleted. This cannot be undone.
            </>
          }
          busy={delBusy}
          error={delErr}
          onConfirm={doDelete}
          onCancel={() => setDeleting(null)}
        />
      )}
    </>
  );
}
