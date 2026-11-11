import { useState } from 'react';
import { APIError, api } from '../api/client';
import type { APIToken } from '../api/types';
import { errorsFor, fieldErrors } from '../lib/schedule';
import {
  EXPIRY_OPTIONS,
  PRESETS,
  SCOPES,
  SCOPE_INFO,
  ciSnippet,
  emptyTokenForm,
  mcpSnippet,
  tokenRequest,
  validateTokenForm,
  type TokenForm,
} from '../lib/tokens';
import { ErrorAlert, Icon, Modal } from './ui';

/** Copies text with the async clipboard API, falling back to execCommand. */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through to the legacy path */
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

function CopyBox({ text, label, mono = true }: { text: string; label: string; mono?: boolean }) {
  const [state, setState] = useState<'idle' | 'ok' | 'fail'>('idle');
  const copy = async () => {
    setState((await copyText(text)) ? 'ok' : 'fail');
    window.setTimeout(() => setState('idle'), 2000);
  };
  return (
    <div className="row" style={{ gap: 8, alignItems: 'stretch', flexWrap: 'nowrap' }}>
      <pre className="code grow" style={{ margin: 0, overflowX: 'auto', userSelect: 'all' }} aria-label={label}>
        {mono ? <code>{text}</code> : text}
      </pre>
      <button className="btn sm" onClick={copy}>
        <Icon name={state === 'ok' ? 'check' : 'copy'} size={13} /> {state === 'ok' ? 'Copied' : state === 'fail' ? 'Press Ctrl+C' : 'Copy'}
      </button>
    </div>
  );
}

/** Creates an API token; after creation shows the secret exactly once. */
export function TokenDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (t: APIToken) => void }) {
  const [form, setForm] = useState<TokenForm>(emptyTokenForm);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [fe, setFe] = useState<Record<string, string[]>>({});
  const [created, setCreated] = useState<{ token: APIToken; secret: string } | null>(null);
  const set = (patch: Partial<TokenForm>) => setForm((f) => ({ ...f, ...patch }));

  const save = async () => {
    setError(null);
    const errs = validateTokenForm(form);
    if (Object.keys(errs).length > 0) {
      setFe(Object.fromEntries(Object.entries(errs).map(([k, v]) => [k, [v]])));
      return;
    }
    setFe({});
    setBusy(true);
    try {
      const r = await api.createToken(tokenRequest(form));
      setCreated(r);
      onCreated(r.token);
    } catch (e) {
      if (e instanceof APIError && e.details.length > 0) setFe(fieldErrors(e.details));
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const origin = window.location.origin;

  if (created) {
    return (
      <Modal
        title={
          <span className="row" style={{ gap: 8 }}>
            <Icon name="key" size={15} /> Token created
          </span>
        }
        onClose={onClose}
        width={620}
        footer={
          <button className="btn primary" onClick={onClose}>
            Done
          </button>
        }
      >
        <div className="stack" style={{ gap: 14 }}>
          <div className="alert warn" role="alert">
            <div className="alert-title">Copy it now — it won't be shown again</div>
            Store the secret in a password manager or a CI secret. If you lose it, revoke the token and create a new one.
          </div>
          <div className="field">
            <div className="label">
              Secret for <b>{created.token.name}</b> ({SCOPE_INFO[created.token.scope].label.toLowerCase()})
            </div>
            <CopyBox text={created.secret} label="Token secret" />
          </div>
          <div className="field">
            <div className="label">MCP server (AI agents: Kiro, Claude Code)</div>
            <CopyBox text={mcpSnippet(origin)} label="MCP command" />
            <div className="hint">
              Set the <code>GOLOAD_TOKEN</code> environment variable to the secret above.
            </div>
          </div>
          <div className="field">
            <div className="label">CI pipeline</div>
            <CopyBox text={ciSnippet(origin)} label="CI snippet" />
            <div className="hint">
              Save the secret as a CI secret named <code>GOLOAD_TOKEN</code>.
            </div>
          </div>
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      title={
        <span className="row" style={{ gap: 8 }}>
          <Icon name="key" size={15} /> New API token
        </span>
      }
      onClose={onClose}
      width={600}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={save} disabled={busy}>
            <Icon name="check" size={13} /> {busy ? 'Creating…' : 'Create token'}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 16 }}>
        <div className="field">
          <label htmlFor="tok-name">Name</label>
          <input
            id="tok-name"
            className={`input ${errorsFor(fe, 'name').length ? 'invalid' : ''}`}
            maxLength={100}
            placeholder="e.g. kiro-hakan or ci-main"
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
          />
          {errorsFor(fe, 'name').map((m, i) => (
            <div key={i} className="field-err">
              {m}
            </div>
          ))}
          <div className="hint">Shown as "started by" on runs, so use something that identifies the owner.</div>
        </div>

        <div className="field">
          <div className="label" id="tok-scope-l">
            Access
          </div>
          <div className="stack" style={{ gap: 8 }} role="radiogroup" aria-labelledby="tok-scope-l">
            {SCOPES.map((s) => (
              <label key={s} htmlFor={`tok-scope-${s}`} className={`scope-card ${form.scope === s ? 'on' : ''}`}>
                <input
                  id={`tok-scope-${s}`}
                  type="radio"
                  name="tok-scope"
                  checked={form.scope === s}
                  onChange={() => set({ scope: s })}
                />{' '}
                {SCOPE_INFO[s].label}
                <span className="small-text text-2" style={{ display: 'block' }}>
                  {SCOPE_INFO[s].description}
                </span>
              </label>
            ))}
          </div>
          {errorsFor(fe, 'scope').map((m, i) => (
            <div key={i} className="field-err">
              {m}
            </div>
          ))}
          <div className="hint">
            Recommended:{' '}
            {PRESETS.map((p, i) => (
              <span key={p.label}>
                {i > 0 ? ' · ' : ''}
                {p.label}: <b>{p.scope}</b>
              </span>
            ))}
          </div>
        </div>

        <div className="field">
          <label htmlFor="tok-exp">Expires</label>
          <select id="tok-exp" className="input" value={form.expiresDays} onChange={(e) => set({ expiresDays: Number(e.target.value) })}>
            {EXPIRY_OPTIONS.map((o) => (
              <option key={o.days} value={o.days}>
                {o.label}
              </option>
            ))}
          </select>
          {errorsFor(fe, 'expires').map((m, i) => (
            <div key={i} className="field-err">
              {m}
            </div>
          ))}
        </div>

        <ErrorAlert error={error} title="Could not create the token" />
      </div>
    </Modal>
  );
}
