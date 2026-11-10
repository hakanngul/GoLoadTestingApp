import { useState, type FormEvent } from 'react';
import { APIError, api, getToken, setToken } from '../api/client';
import { BrandMark, ErrorAlert, Icon } from '../components/ui';

/**
 * Full-page sign-in shown when the API answers 401. The token is verified
 * with GET /api/tests before it is stored in localStorage.
 */
export function SignInPage({ onSignedIn }: { onSignedIn: () => void }) {
  const [token, setTokenValue] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // A stored token that triggered the 401 means it was rotated or revoked.
  const [hadToken] = useState(() => getToken() !== null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const t = token.trim();
    if (!t) return;
    setBusy(true);
    setError(null);
    try {
      await api.checkToken(t);
      setToken(t);
      onSignedIn();
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <div className="signin">
      <form className="signin-card" onSubmit={submit}>
        <div className="signin-brand">
          <BrandMark size={34} />
          <span>GoLoad</span>
        </div>
        <h1>Sign in</h1>
        <p className="text-2">
          This server requires an access token. Enter the token configured on the master to continue.
        </p>
        {hadToken && !error && (
          <div className="alert warn">Your saved token is no longer accepted. Please sign in again.</div>
        )}
        <div className="field">
          <label htmlFor="access-token">Access token</label>
          <input
            id="access-token"
            className={`input mono ${error ? 'invalid' : ''}`}
            type="password"
            autoComplete="current-password"
            autoFocus
            value={token}
            onChange={(e) => setTokenValue(e.target.value)}
            placeholder="••••••••••••••••"
          />
        </div>
        {error instanceof APIError && error.status === 401 ? (
          <div className="alert bad" role="alert">
            <div className="alert-title">Invalid access token</div>
            <div style={{ marginTop: 2, lineHeight: 1.5 }}>
              The server rejected this token. It must match the master&apos;s <span className="mono">-token</span> flag
              (or <span className="mono">GOLOAD_TOKEN</span>).
            </div>
          </div>
        ) : (
          <ErrorAlert error={error} />
        )}
        <button className="btn primary signin-submit" type="submit" disabled={busy || !token.trim()}>
          {busy ? (
            <>
              <span className="spinner on-accent" /> Verifying…
            </>
          ) : (
            <>
              <Icon name="key" size={14} /> Sign in
            </>
          )}
        </button>
        <div className="hint">The token is kept in this browser&apos;s local storage until you sign out.</div>
      </form>
    </div>
  );
}
