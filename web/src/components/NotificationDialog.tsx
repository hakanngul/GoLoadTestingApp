import { useState } from 'react';
import { APIError, api } from '../api/client';
import type { NotificationChannel } from '../api/types';
import {
  EVENT_OPTIONS,
  channelToForm,
  emptyNotificationForm,
  formToRequest,
  toggleEvent,
  validateForm,
  type NotificationForm,
} from '../lib/notifications';
import { errorsFor, fieldErrors } from '../lib/schedule';
import { ErrorAlert, Icon, Modal } from './ui';

function FieldErrors({ msgs }: { msgs: string[] }) {
  return (
    <>
      {msgs.map((m, i) => (
        <div key={i} className="field-err">
          {m}
        </div>
      ))}
    </>
  );
}

/** Creates or edits a notification channel (Slack incoming webhook or generic webhook). */
export function NotificationDialog({
  channel,
  onClose,
  onSaved,
}: {
  channel?: NotificationChannel;
  onClose: () => void;
  onSaved: (c: NotificationChannel) => void;
}) {
  const editing = channel !== undefined;
  const [form, setForm] = useState<NotificationForm>(() => (channel ? channelToForm(channel) : emptyNotificationForm()));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [fe, setFe] = useState<Record<string, string[]>>({});
  const set = (patch: Partial<NotificationForm>) => setForm((f) => ({ ...f, ...patch }));

  const save = async () => {
    setError(null);
    const errs = validateForm(form, editing);
    if (Object.keys(errs).length > 0) {
      setFe(Object.fromEntries(Object.entries(errs).map(([k, v]) => [k, [v]])));
      return;
    }
    setFe({});
    setBusy(true);
    try {
      const req = formToRequest(form);
      onSaved(channel ? await api.updateNotification(channel.id, req) : await api.createNotification(req));
    } catch (e) {
      if (e instanceof APIError && e.details.length > 0) setFe(fieldErrors(e.details));
      setError(e);
      setBusy(false);
    }
  };

  const slack = form.kind === 'slack';
  return (
    <Modal
      title={
        <span className="row" style={{ gap: 8 }}>
          <Icon name="bell" size={15} />
          {editing ? 'Edit notification channel' : 'New notification channel'}
        </span>
      }
      onClose={onClose}
      width={560}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={save} disabled={busy}>
            <Icon name="check" size={13} /> {busy ? 'Saving…' : editing ? 'Save changes' : 'Add channel'}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 16 }}>
        <div className="field">
          <label htmlFor="notif-name">Name</label>
          <input
            id="notif-name"
            className={`input ${errorsFor(fe, 'name').length ? 'invalid' : ''}`}
            maxLength={100}
            placeholder="e.g. #perf-alerts"
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
            autoFocus
          />
          <FieldErrors msgs={errorsFor(fe, 'name')} />
        </div>

        <div className="field">
          <label>Type</label>
          <div className="segmented" role="tablist" aria-label="Channel type">
            <button className={slack ? 'on' : ''} onClick={() => set({ kind: 'slack' })} role="tab" aria-selected={slack}>
              Slack
            </button>
            <button className={!slack ? 'on' : ''} onClick={() => set({ kind: 'webhook' })} role="tab" aria-selected={!slack}>
              Webhook
            </button>
          </div>
          <div className="hint">
            {slack ? (
              <>Posts a short text message to a Slack incoming webhook.</>
            ) : (
              <>
                Posts JSON <code>{'{"event": "run_finished", "run": {…}}'}</code> to any endpoint.
              </>
            )}
          </div>
        </div>

        <div className="field">
          <label htmlFor="notif-url">{slack ? 'Slack webhook URL' : 'Webhook URL'}</label>
          <input
            id="notif-url"
            className={`input ${errorsFor(fe, 'url').length ? 'invalid' : ''}`}
            type="password"
            autoComplete="off"
            spellCheck={false}
            placeholder={
              editing ? `${channel.url_masked} — leave empty to keep` : slack ? 'https://hooks.slack.com/services/…' : 'https://'
            }
            value={form.url}
            onChange={(e) => set({ url: e.target.value })}
          />
          <FieldErrors msgs={errorsFor(fe, 'url')} />
          <div className="hint">The URL is a secret: it is stored on the server and is never shown again.</div>
        </div>

        <div className="field">
          <label>Notify when</label>
          <div className="stack" style={{ gap: 6 }}>
            {EVENT_OPTIONS.map((o) => (
              <label key={o.value} className="checkbox" title={o.hint}>
                <input
                  type="checkbox"
                  checked={form.events.includes(o.value)}
                  onChange={() => set({ events: toggleEvent(form.events, o.value) })}
                />{' '}
                {o.label}
              </label>
            ))}
          </div>
          <FieldErrors msgs={errorsFor(fe, 'events')} />
        </div>

        <div className="stack" style={{ gap: 6 }}>
          <label className="checkbox">
            <input type="checkbox" checked={form.only_failures} onChange={(e) => set({ only_failures: e.target.checked })} /> Only failures
            (failed or error; a stopped run is not a failure)
          </label>
          <label className="checkbox">
            <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: e.target.checked })} /> Enabled
          </label>
        </div>

        <ErrorAlert error={error} title="Could not save the channel" />
      </div>
    </Modal>
  );
}
