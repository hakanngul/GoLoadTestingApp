import { useState } from 'react';
import { api, errorText } from '../api/client';
import type { NotificationChannel } from '../api/types';
import { NotificationDialog } from '../components/NotificationDialog';
import { ConfirmDialog, ErrorAlert, Icon, Loading } from '../components/ui';
import { useAsync } from '../lib/hooks';
import { KIND_LABEL, describeEvents, toggleRequest } from '../lib/notifications';

/** Outcome of the last "Send test" click per channel. */
type TestState = { busy: true } | { busy: false; ok: boolean; message: string };

export function NotificationsPage() {
  const channels = useAsync(() => api.listNotifications(), []);
  const [editing, setEditing] = useState<NotificationChannel | 'new' | null>(null);
  const [deleting, setDeleting] = useState<NotificationChannel | null>(null);
  const [delBusy, setDelBusy] = useState(false);
  const [delErr, setDelErr] = useState<unknown>(null);
  const [busyId, setBusyId] = useState('');
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [tests, setTests] = useState<Record<string, TestState>>({});

  const list = channels.data ?? [];

  const toggle = async (c: NotificationChannel) => {
    setBusyId(c.id);
    setActionErr(null);
    try {
      const up = await api.updateNotification(c.id, toggleRequest(c));
      channels.setData(list.map((x) => (x.id === c.id ? up : x)));
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusyId('');
    }
  };

  const sendTest = async (c: NotificationChannel) => {
    setTests((t) => ({ ...t, [c.id]: { busy: true } }));
    let state: TestState;
    try {
      const r = await api.testNotification(c.id);
      state = r.ok
        ? { busy: false, ok: true, message: 'Test message sent' }
        : { busy: false, ok: false, message: r.error || 'Delivery failed' };
    } catch (e) {
      state = { busy: false, ok: false, message: errorText(e).message };
    }
    setTests((t) => ({ ...t, [c.id]: state }));
  };

  const doDelete = async () => {
    if (!deleting) return;
    setDelBusy(true);
    setDelErr(null);
    try {
      await api.deleteNotification(deleting.id);
      setDeleting(null);
      channels.reload();
    } catch (e) {
      setDelErr(e);
    } finally {
      setDelBusy(false);
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Notifications</h1>
          <div className="page-sub">Post to Slack or a webhook when a run or a suite run finishes</div>
        </div>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setEditing('new')}>
          <Icon name="plus" size={14} /> New channel
        </button>
      </div>

      {channels.error && !channels.data ? <ErrorAlert error={channels.error} title="Failed to load notification channels" /> : null}
      <ErrorAlert error={actionErr} title="Could not update the channel" />

      <div className="card">
        {channels.loading && !channels.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <div className="empty">
            <strong>No notification channels yet</strong>
            Get a message in Slack (or any webhook) when a load test finishes or fails.{' '}
            <button className="linklike" onClick={() => setEditing('new')}>
              New channel
            </button>
          </div>
        ) : (
          <div className="table-wrap">
            <table className="t">
              <thead>
                <tr>
                  <th>Channel</th>
                  <th>Events</th>
                  <th>Only failures</th>
                  <th>Enabled</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((c) => {
                  const t = tests[c.id];
                  return (
                    <tr key={c.id}>
                      <td className="name-cell">
                        {c.name}
                        <div className="sub muted">
                          {KIND_LABEL[c.kind]} · <code>{c.url_masked}</code>
                        </div>
                      </td>
                      <td className="text-2">{describeEvents(c.events)}</td>
                      <td className="text-2">{c.only_failures ? 'Yes' : 'No'}</td>
                      <td>
                        <button
                          className={`switch ${c.enabled ? 'on' : ''}`}
                          role="switch"
                          aria-checked={c.enabled}
                          aria-label={`${c.enabled ? 'Disable' : 'Enable'} ${c.name}`}
                          disabled={busyId === c.id}
                          onClick={() => toggle(c)}
                        >
                          <span className="switch-knob" />
                        </button>
                      </td>
                      <td className="actions">
                        {t && !t.busy && (
                          <span className={`small-text ${t.ok ? 'good' : 'bad'}`} role="status" style={{ marginRight: 8 }}>
                            {t.message}
                          </span>
                        )}
                        <button
                          className="btn sm"
                          onClick={() => sendTest(c)}
                          disabled={t?.busy === true}
                          title="Send a test message to this channel"
                        >
                          {t?.busy ? 'Sending…' : 'Send test'}
                        </button>
                        <button className="btn sm ghost icon" onClick={() => setEditing(c)} title="Edit" aria-label={`Edit ${c.name}`}>
                          <Icon name="edit" size={14} />
                        </button>
                        <button
                          className="btn sm ghost icon danger"
                          onClick={() => {
                            setDelErr(null);
                            setDeleting(c);
                          }}
                          title="Delete"
                          aria-label={`Delete ${c.name}`}
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
      <p className="hint" style={{ marginTop: 10 }}>
        Webhook URLs are secrets and are only shown masked. Links to runs in messages need the server to be started with{' '}
        <code>-public-url</code>. Runs started by a suite also send a <b>run finished</b> notification.
      </p>

      {editing && (
        <NotificationDialog
          channel={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            channels.reload();
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete notification channel?"
          message={
            <>
              <strong>{deleting.name}</strong> will stop receiving notifications.
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
