import { useState } from 'react';
import { APIError, api } from '../api/client';
import type { APIToken } from '../api/types';
import { TokenDialog } from '../components/TokenDialog';
import { ConfirmDialog, ErrorAlert, Icon, Loading } from '../components/ui';
import { fmtDateTime, timeAgo } from '../lib/format';
import { useAsync } from '../lib/hooks';
import { useMe } from '../lib/me';
import { SCOPE_INFO, expiryLabel, fmtPrefix, isExpired, lastUsedLabel, scopeAllows } from '../lib/tokens';

function AuthDisabledCard() {
  return (
    <div className="card">
      <div className="empty">
        <strong>API tokens are off</strong>
        Start the server with <code>-token</code> to enable API tokens. Without it the API is open to anyone who can reach it, so there is
        nobody to issue tokens to.
      </div>
    </div>
  );
}

function ScopeBadge({ scope }: { scope: APIToken['scope'] }) {
  const tone = scope === 'admin' ? 'bad' : scope === 'write' ? 'warn' : scope === 'run' ? 'info' : '';
  return (
    <span className={`badge plain ${tone}`} title={SCOPE_INFO[scope].description}>
      {scope}
    </span>
  );
}

export function TokensPage() {
  const { me, loading: meLoading } = useMe();
  if (meLoading) return <Loading />;
  if (me && me.kind === 'none') {
    return (
      <>
        <div className="page-head">
          <h1>API tokens</h1>
        </div>
        <AuthDisabledCard />
      </>
    );
  }
  if (me && !scopeAllows(me.scope, 'admin')) {
    return (
      <>
        <div className="page-head">
          <h1>API tokens</h1>
        </div>
        <div className="card">
          <div className="empty">
            <strong>Not allowed</strong>
            Managing API tokens needs admin access. You are signed in with <b>{me.scope}</b> access.
          </div>
        </div>
      </>
    );
  }
  return <TokensList />;
}

function TokensList() {
  const tokens = useAsync(() => api.listTokens(), []);
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<APIToken | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const list = tokens.data ?? [];
  const authOff = tokens.error instanceof APIError && tokens.error.status === 409;

  const revoke = async () => {
    if (!revoking) return;
    setBusy(true);
    setErr(null);
    try {
      await api.deleteToken(revoking.id);
      setRevoking(null);
      tokens.reload();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>API tokens</h1>
          <div className="page-sub">Scoped credentials for AI agents, CI pipelines and read-only dashboards</div>
        </div>
        <span className="spacer" />
        {!authOff && (
          <button className="btn primary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={14} /> New token
          </button>
        )}
      </div>

      {authOff ? (
        <AuthDisabledCard />
      ) : (
        <>
          {tokens.error && !tokens.data ? <ErrorAlert error={tokens.error} title="Failed to load API tokens" /> : null}
          <div className="card">
            {tokens.loading && !tokens.data ? (
              <Loading />
            ) : list.length === 0 ? (
              <div className="empty">
                <strong>No API tokens yet</strong>
                Create one for an AI agent or a CI pipeline so it does not need the master token.{' '}
                <button className="linklike" onClick={() => setCreating(true)}>
                  New token
                </button>
              </div>
            ) : (
              <div className="table-wrap">
                <table className="t">
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Scope</th>
                      <th>Token</th>
                      <th>Created</th>
                      <th>Last used</th>
                      <th>Expires</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {list.map((t) => {
                      const expired = isExpired(t.expires_at);
                      return (
                        <tr key={t.id}>
                          <td className="name-cell">{t.name}</td>
                          <td>
                            <ScopeBadge scope={t.scope} />
                          </td>
                          <td>
                            <code>{fmtPrefix(t.prefix)}</code>
                          </td>
                          <td className="text-2" title={fmtDateTime(t.created_at)}>
                            {timeAgo(t.created_at)}
                          </td>
                          <td className="text-2" title={t.last_used_at ? fmtDateTime(t.last_used_at) : undefined}>
                            {lastUsedLabel(t.last_used_at, (iso) => timeAgo(iso))}
                          </td>
                          <td className={expired ? 'bad' : 'text-2'} title={t.expires_at ? fmtDateTime(t.expires_at) : undefined}>
                            {expiryLabel(t.expires_at)}
                          </td>
                          <td className="actions">
                            <button
                              className="btn sm ghost danger"
                              onClick={() => {
                                setErr(null);
                                setRevoking(t);
                              }}
                              aria-label={`Revoke ${t.name}`}
                            >
                              <Icon name="trash" size={14} /> Revoke
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
            Secrets are shown once, when the token is created. Only a short prefix is kept here so you can recognise a token.
          </p>
        </>
      )}

      {creating && (
        <TokenDialog
          onClose={() => setCreating(false)}
          onCreated={() => {
            tokens.reload();
          }}
        />
      )}
      {revoking && (
        <ConfirmDialog
          title="Revoke API token?"
          message={
            <>
              <strong>{revoking.name}</strong> stops working immediately. Anything still using it will get 401 errors.
            </>
          }
          busy={busy}
          error={err}
          onConfirm={revoke}
          onCancel={() => setRevoking(null)}
        />
      )}
    </>
  );
}
