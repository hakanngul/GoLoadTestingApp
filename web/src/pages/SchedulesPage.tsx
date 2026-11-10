import { lazy, Suspense, useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '../api/client';
import type { Schedule, ScheduleRequest } from '../api/types';
import { ConfirmDialog, ErrorAlert, Icon, Loading } from '../components/ui';
import { useAsync, useNow } from '../lib/hooks';
import { describeLoad, describeWhen, fmtInZone, fmtRelative, statusTone } from '../lib/schedule';

// The dialog renders a recharts preview; load it on demand.
const ScheduleDialog = lazy(() => import('../components/ScheduleDialog').then((m) => ({ default: m.ScheduleDialog })));

function toRequest(s: Schedule, enabled: boolean): ScheduleRequest {
  return {
    test_id: s.test_id,
    name: s.name,
    kind: s.kind,
    at: s.at,
    cron: s.cron,
    timezone: s.timezone,
    load: s.load,
    workers: s.workers,
    label: s.label,
    notes: s.notes,
    overlap: s.overlap,
    queue_timeout_ms: s.queue_timeout_ms,
    enabled,
  };
}

export function SchedulesPage() {
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const focus = params.get('focus') ?? '';
  const schedules = useAsync(() => api.listSchedules(), [], 15000);
  const tests = useAsync(() => api.listTests(), []);
  const now = useNow(15000);
  const [editing, setEditing] = useState<Schedule | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Schedule | null>(null);
  const [busyId, setBusyId] = useState('');
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [delBusy, setDelBusy] = useState(false);
  const [delErr, setDelErr] = useState<unknown>(null);

  // Scroll a focused schedule (from a run's "scheduled" badge) into view.
  useEffect(() => {
    if (!focus || !schedules.data) return;
    document.getElementById(`sched-${focus}`)?.scrollIntoView({ block: 'center' });
  }, [focus, schedules.data]);

  const list = schedules.data ?? [];

  const toggle = async (s: Schedule) => {
    setBusyId(s.id);
    setActionErr(null);
    try {
      const up = await api.updateSchedule(s.id, toRequest(s, !s.enabled));
      schedules.setData(list.map((x) => (x.id === s.id ? up : x)));
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusyId('');
    }
  };

  const runNow = async (s: Schedule) => {
    setBusyId(s.id);
    setActionErr(null);
    try {
      const run = await api.runScheduleNow(s.id);
      nav(`/runs/${run.id}`);
    } catch (e) {
      setActionErr(e);
      setBusyId('');
      schedules.reload();
    }
  };

  const doDelete = async () => {
    if (!deleting) return;
    setDelBusy(true);
    setDelErr(null);
    try {
      await api.deleteSchedule(deleting.id);
      setDeleting(null);
      schedules.reload();
    } catch (e) {
      setDelErr(e);
    } finally {
      setDelBusy(false);
    }
  };

  const noTests = tests.data !== undefined && tests.data.length === 0;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Schedules</h1>
          <div className="page-sub">Planned runs — once at a date and time, or on a repeating schedule</div>
        </div>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setEditing('new')} disabled={noTests || !tests.data}>
          <Icon name="plus" size={14} /> New schedule
        </button>
      </div>

      {schedules.error && !schedules.data ? <ErrorAlert error={schedules.error} title="Failed to load schedules" /> : null}
      <ErrorAlert error={actionErr} title="Action failed" />

      <div className="card">
        {schedules.loading && !schedules.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <div className="empty">
            <strong>No schedules yet</strong>
            {noTests ? (
              <>
                Create a test first, then plan when it runs. <Link to="/">Tests</Link>
              </>
            ) : (
              <>
                Plan a test to run at a set time — with its own users, rate and duration.{' '}
                <button className="linklike" onClick={() => setEditing('new')}>
                  New schedule
                </button>
              </>
            )}
          </div>
        ) : (
          <div className="table-wrap">
            <table className="t sched-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>When</th>
                  <th>Next run</th>
                  <th>Last result</th>
                  <th>Load</th>
                  <th>Enabled</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((s) => (
                  <tr key={s.id} id={`sched-${s.id}`} className={s.id === focus ? 'focus-row' : ''}>
                    <td className="name-cell">
                      <button className="linklike" onClick={() => setEditing(s)}>
                        {s.name}
                      </button>
                      <div className="sub">
                        <Link to={`/tests/${s.test_id}`}>{s.test_name}</Link>
                        {s.workers > 1 ? ` · ${s.workers} workers` : ''}
                      </div>
                    </td>
                    <td>
                      <div className="when-cell">{describeWhen(s)}</div>
                      <div className="sub muted">{s.overlap === 'queue' ? 'waits if a run is active' : 'skips if a run is active'}</div>
                    </td>
                    <td>
                      {s.enabled && s.next_run_at ? (
                        <>
                          <div className="num-left">{fmtInZone(s.next_run_at, s.timezone)}</div>
                          <div className="sub accent-text">{fmtRelative(s.next_run_at, now)}</div>
                        </>
                      ) : (
                        <span className="muted">{s.enabled ? '—' : s.kind === 'once' && s.last_status ? 'done' : 'paused'}</span>
                      )}
                    </td>
                    <td>
                      {s.last_status ? (
                        <div title={s.last_message || undefined}>
                          <span className="row" style={{ gap: 6, flexWrap: 'nowrap' }}>
                            <span className={`badge ${statusTone(s.last_status)}`}>{s.last_status}</span>
                            {s.last_run_id && s.last_status === 'started' && (
                              <Link to={`/runs/${s.last_run_id}`} className="small-link">
                                view run
                              </Link>
                            )}
                          </span>
                          <div className="sub muted">
                            {fmtRelative(s.last_run_at, now)}
                            {s.last_message ? ` · ${s.last_message}` : ''}
                          </div>
                        </div>
                      ) : (
                        <span className="muted">not run yet</span>
                      )}
                    </td>
                    <td className="text-2 load-cell">{describeLoad(s.load)}</td>
                    <td>
                      <button
                        className={`switch ${s.enabled ? 'on' : ''}`}
                        role="switch"
                        aria-checked={s.enabled}
                        aria-label={`${s.enabled ? 'Disable' : 'Enable'} ${s.name}`}
                        disabled={busyId === s.id || (!s.enabled && s.kind === 'once')}
                        title={!s.enabled && s.kind === 'once' ? 'Edit the date to enable a one-time schedule again' : undefined}
                        onClick={() => toggle(s)}
                      >
                        <span className="switch-knob" />
                      </button>
                    </td>
                    <td className="actions">
                      <button className="btn sm" onClick={() => runNow(s)} disabled={busyId === s.id} title="Start a run now with this schedule's settings">
                        <Icon name="play" size={11} /> Run
                      </button>
                      <button className="btn sm ghost icon" onClick={() => setEditing(s)} title="Edit" aria-label={`Edit ${s.name}`}>
                        <Icon name="edit" size={14} />
                      </button>
                      <button
                        className="btn sm ghost icon danger"
                        onClick={() => {
                          setDelErr(null);
                          setDeleting(s);
                        }}
                        title="Delete"
                        aria-label={`Delete ${s.name}`}
                      >
                        <Icon name="trash" size={14} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <p className="hint" style={{ marginTop: 10 }}>
        Schedules run inside this server: it must be running at fire time. An occurrence missed while it was stopped is recorded as{' '}
        <b>missed</b> and is not run late. Times are evaluated in each schedule&apos;s time zone (daylight saving aware).
      </p>

      {editing && (
        <Suspense fallback={null}>
          <ScheduleDialog
            tests={tests.data ?? []}
            schedule={editing === 'new' ? undefined : editing}
            onClose={() => setEditing(null)}
            onSaved={(s) => {
              setEditing(null);
              schedules.reload();
              if (focus !== s.id) setParams({ focus: s.id }, { replace: true });
            }}
          />
        </Suspense>
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete schedule?"
          message={
            <>
              <strong>{deleting.name}</strong> will no longer start runs. Runs it already started are kept.
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
