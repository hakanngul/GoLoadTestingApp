import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { api } from '../api/client';
import { SuiteDialog } from '../components/SuiteDialog';
import { ConfirmDialog, ErrorAlert, Icon, Loading, StatusBadge } from '../components/ui';
import { fmtDateTime, fmtSeconds, runDurationS } from '../lib/format';
import { useAsync } from '../lib/hooks';
import { describeLoad } from '../lib/schedule';
import { describeMode, isActive, progress } from '../lib/suite';

/** One suite: its test cases, a Run button and its run history. */
export function SuitePage() {
  const { id = '' } = useParams();
  const nav = useNavigate();
  const suite = useAsync(() => api.getSuite(id), [id]);
  const runs = useAsync(() => api.listSuiteRuns(id), [id], 5000);
  const tests = useAsync(() => api.listTests(), []);
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [label, setLabel] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [delErr, setDelErr] = useState<unknown>(null);

  if (suite.error && !suite.data) return <ErrorAlert error={suite.error} title="Failed to load the suite" />;
  if (!suite.data) return <Loading />;
  const s = suite.data;
  const history = runs.data ?? [];
  const active = history.find((r) => isActive(r));

  const start = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.startSuite(s.id, label.trim() ? { label: label.trim() } : {});
      nav(`/suite-runs/${r.id}`);
    } catch (e) {
      setErr(e);
      setBusy(false);
      runs.reload();
    }
  };

  const doDelete = async () => {
    setBusy(true);
    setDelErr(null);
    try {
      await api.deleteSuite(s.id);
      nav('/suites');
    } catch (e) {
      setDelErr(e);
      setBusy(false);
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <div className="breadcrumb">
            <Link to="/suites">Test suites</Link> /
          </div>
          <h1>{s.name}</h1>
          <div className="page-sub">
            {describeMode(s)}
            {s.description ? ` — ${s.description}` : ''}
          </div>
        </div>
        <span className="spacer" />
        <button className="btn ghost" onClick={() => setEditing(true)}>
          <Icon name="edit" size={13} /> Edit
        </button>
        <button
          className="btn ghost danger"
          onClick={() => {
            setDelErr(null);
            setDeleting(true);
          }}
        >
          <Icon name="trash" size={13} /> Delete
        </button>
      </div>

      <ErrorAlert error={err} title="Could not start the suite" />

      <div className="suite-top">
        <div className="card">
          <div className="card-head">
            <h2>Test cases</h2>
          </div>
          <div className="card-body">
            <ol className="suite-items readonly">
              {s.items.map((it, i) => (
                <li key={`${it.test_id}-${i}`} className="suite-item">
                  <span className="suite-item-no">{i + 1}</span>
                  {it.test_name ? (
                    <Link className="suite-item-name" to={`/tests/${it.test_id}`}>
                      {it.test_name}
                    </Link>
                  ) : (
                    <span className="suite-item-name muted" title="This test was deleted; the suite skips it">
                      deleted test
                    </span>
                  )}
                  {it.load && (
                    <span className="suite-load-tag" title="Load override for this test in the suite">
                      {describeLoad(it.load)}
                    </span>
                  )}
                  {it.workers > 1 && <span className="text-2 small-text">{it.workers} workers</span>}
                </li>
              ))}
            </ol>
          </div>
        </div>
        <div className="card suite-run-card">
          <div className="card-head">
            <h2>Run the suite</h2>
          </div>
          {active ? (
            <div className="card-body stack" style={{ gap: 10 }}>
              <div className="text-2">This suite is running now.</div>
              <Link className="btn primary" to={`/suite-runs/${active.id}`}>
                View live
              </Link>
            </div>
          ) : (
            <div className="card-body stack" style={{ gap: 10 }}>
              <div className="field">
                <label htmlFor="suite-label">Label (optional)</label>
                <input
                  id="suite-label"
                  className="input"
                  maxLength={100}
                  placeholder="e.g. v1.4.2"
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                />
              </div>
              <button className="btn primary" onClick={start} disabled={busy || s.items.length === 0}>
                <Icon name="play" size={12} /> {busy ? 'Starting…' : `Run ${s.items.length} test${s.items.length === 1 ? '' : 's'}`}
              </button>
              <p className="hint" style={{ margin: 0 }}>
                Tests use their saved load unless the suite overrides it (tagged above); every run also appears under Runs.
              </p>
            </div>
          )}
        </div>
      </div>

      <div className="card" style={{ marginTop: 16 }}>
        <div className="card-head">
          <h2>Run history</h2>
        </div>
        {runs.error && !runs.data ? <ErrorAlert error={runs.error} /> : null}
        {history.length === 0 ? (
          <div className="empty">Not run yet.</div>
        ) : (
          <div className="table-wrap">
            <table className="t">
              <thead>
                <tr>
                  <th>Started</th>
                  <th>Status</th>
                  <th>Result</th>
                  <th>Duration</th>
                  <th>Label</th>
                </tr>
              </thead>
              <tbody>
                {history.map((r) => {
                  const p = progress(r);
                  return (
                    <tr key={r.id} style={{ cursor: 'pointer' }} onClick={() => nav(`/suite-runs/${r.id}`)}>
                      <td>
                        <Link to={`/suite-runs/${r.id}`} onClick={(e) => e.stopPropagation()}>
                          {fmtDateTime(r.started_at)}
                        </Link>
                      </td>
                      <td>
                        <StatusBadge status={r.status} />
                      </td>
                      <td className="text-2">
                        {isActive(r)
                          ? `${p.done}/${p.total} done`
                          : `${p.passed} passed${p.failed ? ` · ${p.failed} failed` : ''}${p.skipped ? ` · ${p.skipped} skipped` : ''}`}
                      </td>
                      <td className="num-left">{fmtSeconds(runDurationS(r.started_at, r.finished_at))}</td>
                      <td className="text-2">{r.label || '—'}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {editing && (
        <SuiteDialog
          tests={tests.data ?? []}
          suite={s}
          onClose={() => setEditing(false)}
          onSaved={(up) => {
            setEditing(false);
            suite.setData(up);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete suite?"
          message={
            <>
              <strong>{s.name}</strong> and its run history will be deleted. The tests and their runs are kept.
            </>
          }
          busy={busy}
          error={delErr}
          onConfirm={doDelete}
          onCancel={() => setDeleting(false)}
        />
      )}
    </>
  );
}
