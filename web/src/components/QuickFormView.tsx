import type { ReactNode } from 'react';
import type { FileInfo } from '../api/types';
import { fmtBytes, fmtSeconds } from '../lib/format';
import {
  DUR_UNITS,
  QUICK_METHODS,
  TOKEN_PATH_PICKS,
  durSeconds,
  quickIssues,
  splitUrl,
  templateVars,
  type CredMode,
  type Dur,
  type DurUnit,
  type QuickAuthType,
  type QuickForm,
  type QuickMethod,
} from '../lib/quick';
import { stagesToProfile, type ScenarioForm } from '../lib/scenario';
import { ProfileChart } from './charts';
import { KVEditor } from './KVEditor';
import { Icon } from './ui';

type QMutator = (fn: (q: QuickForm) => void) => void;

function Card({ title, sub, extra, children }: { title: string; sub?: string; extra?: ReactNode; children: ReactNode }) {
  return (
    <div className="card quick-card">
      <div className="card-head">
        <h2>{title}</h2>
        {sub && <span className="muted" style={{ fontSize: 12 }}>{sub}</span>}
        <span className="spacer" />
        {extra}
      </div>
      <div className="card-body">{children}</div>
    </div>
  );
}

function DurInput({
  value,
  onChange,
  placeholder,
  disabled,
  ariaLabel,
}: {
  value: Dur;
  onChange: (d: Dur) => void;
  placeholder?: string;
  disabled?: boolean;
  ariaLabel: string;
}) {
  const bad = value.value.trim() !== '' && !(Number(value.value) >= 0);
  return (
    <div className="dur-input">
      <input
        className={`input num ${bad ? 'invalid' : ''}`}
        type="number"
        min={0}
        step="any"
        aria-label={ariaLabel}
        placeholder={placeholder}
        disabled={disabled}
        value={value.value}
        onChange={(e) => onChange({ ...value, value: e.target.value })}
      />
      <select
        className="input"
        aria-label={`${ariaLabel} unit`}
        disabled={disabled}
        value={value.unit}
        onChange={(e) => onChange({ ...value, unit: e.target.value as DurUnit })}
      >
        {DUR_UNITS.map((u) => (
          <option key={u} value={u}>
            {u === 's' ? 'sec' : u === 'm' ? 'min' : 'hours'}
          </option>
        ))}
      </select>
    </div>
  );
}

function FieldErr({ msg }: { msg?: string }) {
  return msg ? <div className="field-err">{msg}</div> : null;
}

/** Shows what a login response must look like for a dot path, e.g. data.token. */
function exampleResponse(path: string): string {
  const parts = path.trim().split('.').filter(Boolean);
  if (parts.length === 0 || parts.some((p) => !/^[\w-]+$/.test(p))) return '';
  let out = '"eyJhbGciOi…"';
  for (let i = parts.length - 1; i >= 0; i--) out = `{"${parts[i]}": ${out}}`;
  return out;
}

export function QuickFormView({
  quick,
  set,
  form,
  files,
  saved,
  onShowFiles,
}: {
  quick: QuickForm;
  set: QMutator;
  /** The generated full form (for the profile preview). */
  form: ScenarioForm;
  files: FileInfo[];
  /** False for a new, unsaved test (files can be attached after saving). */
  saved: boolean;
  onShowFiles: () => void;
}) {
  const issues = quickIssues(quick);
  const split = splitUrl(quick.url, form.target.base_url);
  const unit = quick.mode === 'vus' ? 'VUs' : 'req/s';
  const profile = stagesToProfile(form.load.stages);
  const a = quick.auth;
  const csvVars = a.type === 'login' && a.credMode === 'csv' ? templateVars(a.bodyTemplate) : [];
  const needsCsv = csvVars.length > 0;
  const csvFile = files.find((f) => f.name === a.csvFile.trim());
  const host = split.ok ? split.base.replace(/\/+$/, '') : 'https://your-host';
  const totalS = [quick.rampUp, quick.hold, ...(quick.rampDownOn ? [quick.rampDown] : [])]
    .map(durSeconds)
    .reduce((x, y) => x + (Number.isFinite(y) ? y : 0), 0);

  return (
    <div className="stack quick-form" style={{ gap: 12 }}>
      {/* ---------------- request ---------------- */}
      <Card title="Request" sub="The endpoint to load test">
        <div className="form-grid">
          <div className="field span-12">
            <label htmlFor="q-name">Test name</label>
            <input
              id="q-name"
              className="input"
              value={quick.name}
              placeholder="e.g. product-list"
              onChange={(e) => set((q) => void (q.name = e.target.value))}
            />
          </div>
          <div className="field span-2">
            <label htmlFor="q-method">Method</label>
            <select
              id="q-method"
              className="input mono"
              value={quick.method}
              onChange={(e) => set((q) => void (q.method = e.target.value as QuickMethod))}
            >
              {QUICK_METHODS.map((m) => (
                <option key={m}>{m}</option>
              ))}
            </select>
          </div>
          <div className="field span-10">
            <label htmlFor="q-url">URL</label>
            <input
              id="q-url"
              className={`input mono ${issues.url ? 'invalid' : ''}`}
              placeholder="https://api.example.com/v1/products?page=1"
              value={quick.url}
              autoFocus={!quick.url}
              onChange={(e) => set((q) => void (q.url = e.target.value))}
            />
            {issues.url ? (
              <FieldErr msg={issues.url} />
            ) : split.ok && quick.url.trim() ? (
              <div className="hint url-split">
                host <code>{split.base}</code> · path <code>{split.path}</code>
              </div>
            ) : (
              <div className="hint">Paste the full address. {'{{variables}}'} from a CSV file are allowed.</div>
            )}
          </div>
          {quick.method !== 'GET' && (
            <div className="field span-12">
              <label htmlFor="q-body">
                JSON body <span className="label-opt">optional</span>
              </label>
              <textarea
                id="q-body"
                className={`input mono ${issues.body ? 'invalid' : ''}`}
                rows={Math.min(8, Math.max(3, quick.body.split('\n').length))}
                placeholder='{"name": "Widget", "price": 9.5}'
                value={quick.body}
                onChange={(e) => set((q) => void (q.body = e.target.value))}
              />
              {issues.body ? (
                <FieldErr msg={issues.body} />
              ) : (
                <div className="hint">
                  Sent with <code>Content-Type: application/json</code> (set automatically).
                </div>
              )}
            </div>
          )}
        </div>
        <details className="quick-more" open={quick.headers.length > 0 ? true : undefined}>
          <summary>
            Headers <span className="muted">({quick.headers.length})</span>
            <span className="hint" style={{ fontWeight: 400 }}>
              — sent with every request, including login
            </span>
          </summary>
          <div style={{ paddingTop: 8 }}>
            <KVEditor rows={quick.headers} onChange={(rows) => set((q) => void (q.headers = rows))} />
          </div>
        </details>
      </Card>

      {/* ---------------- load ---------------- */}
      <Card
        title="Load"
        extra={
          <div className="segmented" role="radiogroup" aria-label="Load type">
            {(
              [
                ['vus', 'Virtual users'],
                ['rps', 'Requests per second'],
              ] as const
            ).map(([m, label]) => (
              <button
                key={m}
                role="radio"
                aria-checked={quick.mode === m}
                className={quick.mode === m ? 'on' : ''}
                onClick={() => set((q) => void (q.mode = m))}
              >
                {label}
              </button>
            ))}
          </div>
        }
      >
        <div className="quick-load">
          <div className="quick-load-fields">
            <div className="field">
              <label htmlFor="q-target">{quick.mode === 'vus' ? 'Virtual users' : 'Requests per second'}</label>
              <div className="row" style={{ gap: 6 }}>
                <input
                  id="q-target"
                  className={`input num ${issues.target ? 'invalid' : ''}`}
                  type="number"
                  min={1}
                  style={{ width: 110 }}
                  value={quick.target}
                  onChange={(e) => set((q) => void (q.target = e.target.value))}
                />
                <span className="muted">{unit}</span>
              </div>
              <div className="hint">
                {quick.mode === 'vus'
                  ? 'Users that each send requests back to back.'
                  : 'New requests started every second, however slow the server gets.'}
              </div>
            </div>
            <div className="field">
              <label>Ramp-up</label>
              <DurInput ariaLabel="Ramp-up" value={quick.rampUp} onChange={(d) => set((q) => void (q.rampUp = d))} />
            </div>
            <div className="field">
              <label>Hold</label>
              <DurInput ariaLabel="Hold" value={quick.hold} onChange={(d) => set((q) => void (q.hold = d))} />
            </div>
            <div className="field">
              <label className="checkbox label-check">
                <input
                  type="checkbox"
                  checked={quick.rampDownOn}
                  onChange={(e) => set((q) => void (q.rampDownOn = e.target.checked))}
                />
                Ramp-down
              </label>
              <DurInput
                ariaLabel="Ramp-down"
                disabled={!quick.rampDownOn}
                value={quick.rampDown}
                onChange={(d) => set((q) => void (q.rampDown = d))}
              />
            </div>
          </div>
          <div className="quick-preview">
            <div className="row" style={{ justifyContent: 'space-between', marginBottom: 2 }}>
              <span className="label" style={{ margin: 0 }}>
                Preview
              </span>
              <span className="muted num" style={{ fontSize: 12 }}>
                peak {profile.peak} {unit} · {fmtSeconds(totalS)}
              </span>
            </div>
            <ProfileChart points={profile.points} unit={unit} height={150} />
          </div>
        </div>
      </Card>

      {/* ---------------- pass criteria ---------------- */}
      <Card title="Pass criteria" sub="The run fails when a checked rule is broken">
        <div className="criteria">
          <label className="criterion">
            <input type="checkbox" checked={quick.checkStatus} onChange={(e) => set((q) => void (q.checkStatus = e.target.checked))} />
            <span>Response status is</span>
            <input
              className="input num"
              style={{ width: 80 }}
              disabled={!quick.checkStatus}
              value={quick.status}
              aria-label="Expected status"
              onChange={(e) => set((q) => void (q.status = e.target.value))}
            />
            <span className="hint">other statuses count as errors</span>
          </label>
          <label className="criterion">
            <input type="checkbox" checked={quick.checkP95} onChange={(e) => set((q) => void (q.checkP95 = e.target.checked))} />
            <span>95% of requests faster than</span>
            <input
              className="input num"
              type="number"
              min={0}
              style={{ width: 90 }}
              disabled={!quick.checkP95}
              value={quick.p95Ms}
              aria-label="p95 limit in milliseconds"
              onChange={(e) => set((q) => void (q.p95Ms = e.target.value))}
            />
            <span className="muted">ms</span>
          </label>
          <label className="criterion">
            <input type="checkbox" checked={quick.checkErrors} onChange={(e) => set((q) => void (q.checkErrors = e.target.checked))} />
            <span>Error rate below</span>
            <input
              className="input num"
              type="number"
              min={0}
              max={100}
              step="any"
              style={{ width: 90 }}
              disabled={!quick.checkErrors}
              value={quick.errorPct}
              aria-label="Error rate limit in percent"
              onChange={(e) => set((q) => void (q.errorPct = e.target.value))}
            />
            <span className="muted">%</span>
          </label>
        </div>
      </Card>

      {/* ---------------- auth ---------------- */}
      <Card
        title="Authentication"
        sub={a.type === 'none' ? 'optional' : undefined}
        extra={
          <div className="segmented" role="radiogroup" aria-label="Authentication">
            {(
              [
                ['none', 'None'],
                ['bearer', 'Bearer token'],
                ['login', 'Login request'],
              ] as [QuickAuthType, string][]
            ).map(([t, label]) => (
              <button
                key={t}
                role="radio"
                aria-checked={a.type === t}
                className={a.type === t ? 'on' : ''}
                onClick={() => set((q) => void (q.auth.type = t))}
              >
                {label}
              </button>
            ))}
          </div>
        }
      >
        {a.type === 'none' && (
          <div className="hint">
            The endpoint is public. Pick <strong>Bearer token</strong> if you already have a token, or{' '}
            <strong>Login request</strong> to log in first and reuse the token.
          </div>
        )}

        {a.type === 'bearer' && (
          <div className="stack" style={{ gap: 10 }}>
            <div className="field">
              <label htmlFor="q-token">Token</label>
              <input
                id="q-token"
                className="input mono"
                placeholder="eyJhbGciOiJIUzI1NiIs…"
                value={a.token}
                onChange={(e) => set((q) => void (q.auth.token = e.target.value))}
              />
            </div>
            <HeaderPreview value={`Bearer ${a.token ? (a.token.length > 24 ? a.token.slice(0, 24) + '…' : a.token) : '<token>'}`} />
          </div>
        )}

        {a.type === 'login' && (
          <div className="stack" style={{ gap: 14 }}>
            <div className="form-grid">
              <div className="field span-2">
                <label htmlFor="q-login-method">Method</label>
                <select
                  id="q-login-method"
                  className="input mono"
                  value={a.loginMethod}
                  onChange={(e) => set((q) => void (q.auth.loginMethod = e.target.value as 'POST' | 'PUT'))}
                >
                  <option>POST</option>
                  <option>PUT</option>
                </select>
              </div>
              <div className="field span-10">
                <label htmlFor="q-login-path">Login URL</label>
                <div className={`input-addon ${issues.loginPath ? 'invalid' : ''}`}>
                  <span className="addon mono" title="Same host as the request">
                    {host}
                  </span>
                  <input
                    id="q-login-path"
                    className="input mono"
                    placeholder="/api/login"
                    value={a.loginPath}
                    onChange={(e) => set((q) => void (q.auth.loginPath = e.target.value))}
                  />
                </div>
                <FieldErr msg={issues.loginPath} />
              </div>
            </div>

            <div className="field">
              <label>Credentials</label>
              <div className="choice-row">
                {(
                  [
                    ['csv', 'Users from a CSV file', 'Each virtual user logs in with its own account'],
                    ['fixed', 'One fixed account', 'Everyone uses the same username and password'],
                  ] as [CredMode, string, string][]
                ).map(([m, title, desc]) => (
                  <button
                    key={m}
                    className={`choice ${a.credMode === m ? 'on' : ''}`}
                    role="radio"
                    aria-checked={a.credMode === m}
                    onClick={() =>
                      set((q) => {
                        q.auth.credMode = m;
                        q.auth.scope = m === 'fixed' ? 'shared' : 'per_user';
                      })
                    }
                  >
                    <span className="choice-dot" />
                    <span>
                      <strong>{title}</strong>
                      <span className="hint">{desc}</span>
                    </span>
                  </button>
                ))}
              </div>
            </div>

            {a.credMode === 'csv' ? (
              <div className="form-grid">
                <div className="field span-7">
                  <label htmlFor="q-login-body">Login body (JSON template)</label>
                  <textarea
                    id="q-login-body"
                    className={`input mono ${issues.loginBody ? 'invalid' : ''}`}
                    rows={3}
                    value={a.bodyTemplate}
                    onChange={(e) => set((q) => void (q.auth.bodyTemplate = e.target.value))}
                  />
                  {issues.loginBody ? (
                    <FieldErr msg={issues.loginBody} />
                  ) : (
                    <div className="hint">
                      {csvVars.length ? (
                        <>
                          {csvVars.map((v, i) => (
                            <span key={v}>
                              {i > 0 && ', '}
                              <code>{`{{${v}}}`}</code>
                            </span>
                          ))}{' '}
                          {csvVars.length === 1 ? 'is' : 'are'} filled from the CSV columns of the same name.
                        </>
                      ) : (
                        'No {{variables}} — every user sends this exact body.'
                      )}
                    </div>
                  )}
                </div>
                <div className="field span-5">
                  <label htmlFor="q-csv">Users file</label>
                  <input
                    id="q-csv"
                    className={`input mono ${issues.csv ? 'invalid' : ''}`}
                    value={a.csvFile}
                    disabled={!needsCsv}
                    placeholder="users.csv"
                    onChange={(e) => set((q) => void (q.auth.csvFile = e.target.value))}
                  />
                  <FieldErr msg={issues.csv} />
                  {needsCsv && (
                    <div className={`csv-hint ${csvFile ? 'ok' : ''}`}>
                      <Icon name={csvFile ? 'check' : 'file'} size={14} />
                      <div>
                        {csvFile ? (
                          <>
                            <strong className="mono">{csvFile.name}</strong> attached ({fmtBytes(csvFile.size)})
                          </>
                        ) : saved ? (
                          <>
                            Upload <strong className="mono">{a.csvFile || 'users.csv'}</strong> in{' '}
                            <button className="linklike" onClick={onShowFiles}>
                              Data files →
                            </button>
                          </>
                        ) : (
                          <>
                            Create the test, then upload <strong className="mono">{a.csvFile || 'users.csv'}</strong> in{' '}
                            <button className="linklike" onClick={onShowFiles}>
                              Data files →
                            </button>
                          </>
                        )}
                        <div className="hint">
                          Header row with columns <code>{csvVars.join(',')}</code>, one account per line. Rows are used
                          in order.
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            ) : (
              <div className="form-grid">
                <div className="field span-3">
                  <label htmlFor="q-ufield">Username field</label>
                  <input
                    id="q-ufield"
                    className="input mono"
                    value={a.userField}
                    onChange={(e) => set((q) => void (q.auth.userField = e.target.value))}
                  />
                </div>
                <div className="field span-3">
                  <label htmlFor="q-user">Username</label>
                  <input
                    id="q-user"
                    className="input mono"
                    value={a.username}
                    onChange={(e) => set((q) => void (q.auth.username = e.target.value))}
                  />
                </div>
                <div className="field span-3">
                  <label htmlFor="q-pfield">Password field</label>
                  <input
                    id="q-pfield"
                    className="input mono"
                    value={a.passField}
                    onChange={(e) => set((q) => void (q.auth.passField = e.target.value))}
                  />
                </div>
                <div className="field span-3">
                  <label htmlFor="q-pass">Password</label>
                  <input
                    id="q-pass"
                    className="input mono"
                    type="password"
                    autoComplete="off"
                    value={a.password}
                    onChange={(e) => set((q) => void (q.auth.password = e.target.value))}
                  />
                </div>
                <div className="hint span-12">
                  Stored in the scenario YAML as plain text — use a dedicated test account.
                </div>
              </div>
            )}

            <div className="form-grid">
              <div className="field span-6">
                <label htmlFor="q-token-path">Token in the login response</label>
                <input
                  id="q-token-path"
                  className="input mono"
                  value={a.tokenPath}
                  placeholder="token"
                  onChange={(e) => set((q) => void (q.auth.tokenPath = e.target.value))}
                />
                <div className="chips">
                  {TOKEN_PATH_PICKS.map((p) => (
                    <button
                      key={p}
                      className={`chip ${a.tokenPath.trim() === p ? 'on' : ''}`}
                      onClick={() => set((q) => void (q.auth.tokenPath = p))}
                    >
                      {p}
                    </button>
                  ))}
                </div>
                {exampleResponse(a.tokenPath) && (
                  <div className="hint">
                    Expects a JSON response like <code>{exampleResponse(a.tokenPath)}</code>
                  </div>
                )}
              </div>
              <div className="field span-6">
                <label>Log in</label>
                <select
                  className="input"
                  value={a.scope}
                  onChange={(e) => set((q) => void (q.auth.scope = e.target.value as 'per_user' | 'shared'))}
                >
                  <option value="per_user">Once per user (each account logs in once)</option>
                  <option value="shared">Once in total (one shared login)</option>
                </select>
                <div className="row" style={{ gap: 8, marginTop: 6 }}>
                  <span className="hint" style={{ whiteSpace: 'nowrap' }}>
                    Log in again after
                  </span>
                  <DurInput
                    ariaLabel="Re-login after"
                    placeholder="never"
                    value={a.refresh}
                    onChange={(d) => set((q) => void (q.auth.refresh = d))}
                  />
                </div>
              </div>
            </div>
            <HeaderPreview value="Bearer <token>" />
          </div>
        )}
      </Card>
    </div>
  );
}

function HeaderPreview({ value }: { value: string }) {
  return (
    <div className="auth-preview">
      <span className="label" style={{ margin: 0 }}>
        Added to every request
      </span>
      <code>Authorization: {value}</code>
    </div>
  );
}
