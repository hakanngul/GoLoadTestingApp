import { useEffect, useState, type ReactNode } from 'react';
import type { FileInfo } from '../api/types';
import {
  ASSERT_OPS,
  ASSERT_TYPES,
  DATA_MODES,
  EXTRACT_SOURCES,
  METHODS,
  REGRESSION_KINDS,
  REGRESSION_METRICS,
  THRESHOLD_METRICS,
  THRESHOLD_OPS,
  OP_SYMBOL,
  authHeaderPreview,
  defaultLoginStep,
  emptyStep,
  parseDuration,
  unitFor,
  type AssertType,
  type DataMode,
  type AuthType,
  type ExtractSource,
  type FormAuth,
  type FormStep,
  type LoadModel,
  type RegressionKind,
  type ScenarioForm,
} from '../lib/scenario';
import { KVEditor } from './KVEditor';
import { Icon } from './ui';

type Mutator = (fn: (f: ScenarioForm) => void) => void;

function Section({
  id,
  title,
  extra,
  children,
}: {
  id?: string;
  title: ReactNode;
  extra?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="card form-section" id={id}>
      <div className="card-head">
        <h2>{title}</h2>
        <span className="spacer" />
        {extra}
      </div>
      <div className="card-body">{children}</div>
    </div>
  );
}

function durOk(s: string, allowEmpty = true): boolean {
  if (!s.trim()) return allowEmpty;
  return Number.isFinite(parseDuration(s));
}

export function ScenarioFormView({
  form,
  mutate,
  files,
}: {
  form: ScenarioForm;
  mutate: Mutator;
  files: FileInfo[];
}) {
  const unit = unitFor(form.load.model);
  const fileOptions = files.map((f) => f.name);
  if (form.data.file && !fileOptions.includes(form.data.file)) fileOptions.unshift(form.data.file);

  const authSummary =
    form.auth.type === 'none'
      ? 'none'
      : form.auth.type === 'login'
        ? `login · ${form.auth.scope || 'per_user'}`
        : form.auth.type;
  const toc: { id: string; label: string; meta?: string }[] = [
    { id: 'sec-target', label: 'Target' },
    { id: 'sec-auth', label: 'Auth', meta: authSummary },
    { id: 'sec-load', label: 'Load', meta: `${form.load.model} · ${form.load.stages.length}` },
    { id: 'sec-data', label: 'Data', meta: form.data.enabled ? form.data.file || '—' : 'off' },
    { id: 'sec-steps', label: 'Steps', meta: String(form.steps.length) },
    { id: 'sec-thresholds', label: 'Thresholds', meta: String(form.thresholds.length) },
    { id: 'sec-regression', label: 'Regression', meta: String(form.regression.length) },
  ];

  return (
    <div className="stack" style={{ gap: 12 }}>
      <SectionNav items={toc} />
      <Section id="sec-target" title="General & target">
        <div className="form-grid">
          <div className="field span-4">
            <label>Name</label>
            <input className="input" value={form.name} onChange={(e) => mutate((f) => void (f.name = e.target.value))} />
          </div>
          <div className="field span-6">
            <label>Base URL</label>
            <input
              className="input mono"
              placeholder="https://api.example.com"
              value={form.target.base_url}
              onChange={(e) => mutate((f) => void (f.target.base_url = e.target.value))}
            />
          </div>
          <div className="field span-2">
            <label>Timeout</label>
            <input
              className={`input mono ${durOk(form.target.timeout) ? '' : 'invalid'}`}
              placeholder="30s"
              value={form.target.timeout}
              onChange={(e) => mutate((f) => void (f.target.timeout = e.target.value))}
            />
          </div>
          <div className="field span-12">
            <label>Default headers</label>
            <KVEditor rows={form.target.headers} onChange={(rows) => mutate((f) => void (f.target.headers = rows))} />
          </div>
          <div className="span-12">
            <label className="checkbox">
              <input
                type="checkbox"
                checked={form.target.insecure_skip_verify}
                onChange={(e) => mutate((f) => void (f.target.insecure_skip_verify = e.target.checked))}
              />
              Skip TLS certificate verification
            </label>
          </div>
          <div className="span-12">
            <label className="checkbox">
              <input
                type="checkbox"
                checked={form.target.cookies}
                onChange={(e) => mutate((f) => void (f.target.cookies = e.target.checked))}
              />
              Keep cookies per virtual user
            </label>
            <div className="hint">
              Like a browser, Locust or JMeter: cookies a response sets are sent back on later requests. VU model: one
              cookie jar per virtual user for the whole run. Arrival-rate model: every iteration starts as a new visitor
              without cookies.
            </div>
          </div>
          <div className="span-12">
            <label className="checkbox">
              <input
                type="checkbox"
                checked={form.target.follow_redirects}
                onChange={(e) => mutate((f) => void (f.target.follow_redirects = e.target.checked))}
              />
              Follow redirects
            </label>
            <div className="hint">
              A step reports the final response; its latency covers the whole redirect chain. Turn off to measure or
              assert a 3xx itself (a single step can override this in YAML with <code>follow_redirects</code>).
            </div>
          </div>
        </div>
      </Section>

      <AuthSection auth={form.auth} mutate={mutate} dataEnabled={form.data.enabled} />

      <Section
        id="sec-load"
        title="Load profile"
        extra={
          <div className="segmented" role="radiogroup" aria-label="Load model">
            {(['open', 'closed'] as LoadModel[]).map((m) => (
              <button
                key={m}
                className={form.load.model === m ? 'on' : ''}
                onClick={() => mutate((f) => void (f.load.model = m))}
                role="radio"
                aria-checked={form.load.model === m}
              >
                {m === 'open' ? 'Open · arrival rate' : 'Closed · virtual users'}
              </button>
            ))}
          </div>
        }
      >
        <div className="stack" style={{ gap: 10 }}>
          <div className="hint">
            {form.load.model === 'open'
              ? 'Stage target is iterations started per second. Each stage ramps linearly from the previous target.'
              : 'Stage target is the number of concurrent virtual users looping the steps. Ramps linearly between stages.'}
          </div>
          <table className="t editable" style={{ width: 'auto', minWidth: 460 }}>
            <thead>
              <tr>
                <th style={{ width: 40 }}>#</th>
                <th>Duration</th>
                <th>Target</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {form.load.stages.map((s, i) => (
                <tr key={i}>
                  <td className="muted mono">{i + 1}</td>
                  <td>
                    <input
                      className={`input mono ${durOk(s.duration, false) ? '' : 'invalid'}`}
                      style={{ width: 180 }}
                      value={s.duration}
                      placeholder="30s"
                      onChange={(e) => mutate((f) => void (f.load.stages[i].duration = e.target.value))}
                    />
                  </td>
                  <td>
                    <div className="row" style={{ gap: 6 }}>
                      <input
                        className="input num"
                        style={{ width: 140 }}
                        type="number"
                        min={0}
                        value={s.target}
                        onChange={(e) => mutate((f) => void (f.load.stages[i].target = e.target.value))}
                      />
                      <span className="muted" style={{ width: 30 }}>
                        {unit}
                      </span>
                    </div>
                  </td>
                  <td>
                    <button
                      className="btn ghost icon sm"
                      onClick={() => mutate((f) => void f.load.stages.splice(i, 1))}
                      aria-label="Remove stage"
                    >
                      <Icon name="x" size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="row">
            <button
              className="btn sm ghost"
              onClick={() =>
                mutate((f) => {
                  const last = f.load.stages[f.load.stages.length - 1];
                  f.load.stages.push({ duration: '30s', target: last ? last.target : '10' });
                })
              }
            >
              <Icon name="plus" size={12} /> Add stage
            </button>
            <span className="grow" />
            {form.load.model === 'open' && (
              <div className="row" style={{ gap: 6 }}>
                <span className="label" style={{ margin: 0 }}>
                  Max VUs
                </span>
                <input
                  className="input num"
                  style={{ width: 100 }}
                  type="number"
                  min={1}
                  placeholder="1000"
                  value={form.load.max_vus}
                  onChange={(e) => mutate((f) => void (f.load.max_vus = e.target.value))}
                />
              </div>
            )}
          </div>
        </div>
      </Section>

      <Section
        id="sec-data"
        title="Test data"
        extra={
          <label className="checkbox">
            <input
              type="checkbox"
              checked={form.data.enabled}
              onChange={(e) =>
                mutate((f) => {
                  f.data.enabled = e.target.checked;
                  if (e.target.checked && !f.data.file && files[0]) f.data.file = files[0].name;
                })
              }
            />
            Use CSV data
          </label>
        }
      >
        {form.data.enabled ? (
          <div className="form-grid">
            <div className="field span-6">
              <label>File</label>
              <select
                className="input"
                value={form.data.file}
                onChange={(e) => mutate((f) => void (f.data.file = e.target.value))}
              >
                <option value="">— select attached file —</option>
                {fileOptions.map((n) => (
                  <option key={n} value={n}>
                    {n}
                    {files.some((x) => x.name === n) ? '' : ' (not uploaded)'}
                  </option>
                ))}
              </select>
            </div>
            <div className="field span-6">
              <label>Mode</label>
              <select
                className="input"
                value={form.data.mode}
                onChange={(e) => mutate((f) => void (f.data.mode = e.target.value as DataMode))}
              >
                {DATA_MODES.map((m) => (
                  <option key={m} value={m}>
                    {m}
                    {m === 'sequential' ? ' — round-robin' : m === 'random' ? ' — random row' : ' — each row once'}
                  </option>
                ))}
              </select>
            </div>
            <div className="hint span-12">
              CSV columns become template variables, e.g. <code>{'{{username}}'}</code>. Upload files in the panel on the
              right.
            </div>
          </div>
        ) : (
          <div className="hint">No data file. Enable to feed CSV rows into steps as variables.</div>
        )}
      </Section>

      <Section
        id="sec-steps"
        title={`Steps (${form.steps.length})`}
        extra={
          <button className="btn sm" onClick={() => mutate((f) => void f.steps.push(emptyStep(f.steps.length + 1)))}>
            <Icon name="plus" size={12} /> Add step
          </button>
        }
      >
        {form.steps.length === 0 && <div className="hint">At least one step is required.</div>}
        {form.steps.map((s, i) => (
          <StepCard key={i} index={i} step={s} count={form.steps.length} mutate={mutate} />
        ))}
      </Section>

      <Section
        id="sec-thresholds"
        title="Thresholds"
        extra={
          <button
            className="btn sm"
            onClick={() => mutate((f) => void f.thresholds.push({ metric: 'latency.p95', op: 'lt', value: '500' }))}
          >
            <Icon name="plus" size={12} /> Add threshold
          </button>
        }
      >
        {form.thresholds.length === 0 ? (
          <div className="hint">No thresholds — the run will pass whenever it completes.</div>
        ) : (
          <table className="t editable">
            <thead>
              <tr>
                <th>Metric</th>
                <th style={{ width: 110 }}>Operator</th>
                <th style={{ width: 160 }}>Value</th>
                <th>Unit</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {form.thresholds.map((t, i) => {
                const meta = THRESHOLD_METRICS.find((m) => m.value === t.metric);
                return (
                  <tr key={i}>
                    <td>
                      <select
                        className="input mono"
                        value={t.metric}
                        onChange={(e) => mutate((f) => void (f.thresholds[i].metric = e.target.value))}
                      >
                        {!meta && <option value={t.metric}>{t.metric || '—'}</option>}
                        {THRESHOLD_METRICS.map((m) => (
                          <option key={m.value} value={m.value}>
                            {m.label}
                          </option>
                        ))}
                      </select>
                    </td>
                    <td>
                      <select
                        className="input"
                        value={t.op}
                        onChange={(e) => mutate((f) => void (f.thresholds[i].op = e.target.value))}
                      >
                        {THRESHOLD_OPS.map((o) => (
                          <option key={o} value={o}>
                            {OP_SYMBOL[o]} {o}
                          </option>
                        ))}
                      </select>
                    </td>
                    <td>
                      <input
                        className="input num"
                        type="number"
                        step="any"
                        min={0}
                        value={t.value}
                        onChange={(e) => mutate((f) => void (f.thresholds[i].value = e.target.value))}
                      />
                    </td>
                    <td className="muted">{meta?.unit ?? ''}</td>
                    <td>
                      <button
                        className="btn ghost icon sm"
                        onClick={() => mutate((f) => void f.thresholds.splice(i, 1))}
                        aria-label="Remove threshold"
                      >
                        <Icon name="x" size={14} />
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Section>

      <Section
        id="sec-regression"
        title="Regression vs baseline"
        extra={
          <button
            className="btn sm"
            onClick={() => mutate((f) => void f.regression.push({ metric: 'p95_ms', kind: 'max_increase_pct', value: '20' }))}
          >
            <Icon name="plus" size={12} /> Add rule
          </button>
        }
      >
        {form.regression.length === 0 ? (
          <div className="hint">No regression rules — runs are not compared with the test&apos;s baseline run.</div>
        ) : (
          <table className="t editable">
            <thead>
              <tr>
                <th>Metric</th>
                <th>Limit</th>
                <th style={{ width: 160 }}>Value</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {form.regression.map((r, i) => (
                <tr key={i}>
                  <td>
                    <select
                      className="input mono"
                      value={r.metric}
                      onChange={(e) => mutate((f) => void (f.regression[i].metric = e.target.value))}
                    >
                      {!REGRESSION_METRICS.includes(r.metric) && <option value={r.metric}>{r.metric || '—'}</option>}
                      {REGRESSION_METRICS.map((m) => (
                        <option key={m} value={m}>
                          {m}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <select
                      className="input"
                      value={r.kind}
                      onChange={(e) => mutate((f) => void (f.regression[i].kind = e.target.value as RegressionKind))}
                    >
                      {REGRESSION_KINDS.map((k) => (
                        <option key={k.value} value={k.value}>
                          {k.label}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <input
                      className="input num"
                      type="number"
                      step="any"
                      min={0}
                      value={r.value}
                      onChange={(e) => mutate((f) => void (f.regression[i].value = e.target.value))}
                    />
                  </td>
                  <td>
                    <button
                      className="btn ghost icon sm"
                      onClick={() => mutate((f) => void f.regression.splice(i, 1))}
                      aria-label="Remove rule"
                    >
                      <Icon name="x" size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  );
}

function StepCard({ index, step, count, mutate }: { index: number; step: FormStep; count: number; mutate: Mutator }) {
  const m: StepMutator = (fn) => mutate((f) => fn(f.steps[index]));
  const move = (d: number) =>
    mutate((f) => {
      const j = index + d;
      if (j < 0 || j >= f.steps.length) return;
      [f.steps[index], f.steps[j]] = [f.steps[j], f.steps[index]];
    });
  return (
    <StepEditor
      step={step}
      m={m}
      label={`#${index + 1}`}
      controls={
        <>
          <button className="btn ghost icon sm" onClick={() => move(-1)} disabled={index === 0} title="Move up" aria-label="Move up">
            <Icon name="up" size={14} />
          </button>
          <button
            className="btn ghost icon sm"
            onClick={() => move(1)}
            disabled={index === count - 1}
            title="Move down"
            aria-label="Move down"
          >
            <Icon name="down" size={14} />
          </button>
          <button
            className="btn ghost icon sm danger"
            onClick={() => mutate((f) => void f.steps.splice(index, 1))}
            title="Remove step"
            aria-label="Remove step"
          >
            <Icon name="trash" size={14} />
          </button>
        </>
      }
    />
  );
}

type StepMutator = (fn: (s: FormStep) => void) => void;

/**
 * Editor for one step (also used for the auth login request). httpOnly
 * hides the WebSocket option; nameOptional allows an empty name.
 */
export function StepEditor({
  step,
  m,
  label,
  controls,
  httpOnly,
  nameOptional,
}: {
  step: FormStep;
  m: StepMutator;
  label: string;
  controls?: ReactNode;
  httpOnly?: boolean;
  nameOptional?: boolean;
}) {
  const isWS = step.type === 'ws';
  const showBody = !isWS && (!['GET', 'HEAD'].includes(step.method) || step.body !== '');
  const pathOk = /^(\/|\{\{)/.test(step.path) || /^(https?|wss?):\/\//i.test(step.path);
  const extractSources = isWS ? EXTRACT_SOURCES.filter((x) => x !== 'header') : EXTRACT_SOURCES;

  return (
    <div className="step-card">
      <div className="step-head">
        <span className="step-index">{label}</span>
        <input
          className="input"
          style={{ maxWidth: 260, fontWeight: 600 }}
          value={step.name}
          placeholder={nameOptional ? 'name (optional)' : 'step name'}
          onChange={(e) => m((s) => void (s.name = e.target.value))}
        />
        <span className={`method ${isWS ? 'WS' : step.method}`}>{isWS ? 'WS' : step.method}</span>
        <span className="mono muted" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {step.path}
        </span>
        <span className="grow" />
        {controls}
      </div>
      <div className="step-body">
        <div className="form-grid">
          <div className="field span-2">
            <label>Method</label>
            <select
              className="input mono"
              value={isWS ? 'WS' : step.method}
              onChange={(e) =>
                m((s) => {
                  if (e.target.value === 'WS') {
                    s.type = 'ws';
                    s.method = '';
                  } else {
                    s.type = 'http';
                    s.method = e.target.value;
                  }
                })
              }
            >
              {METHODS.map((x) => (
                <option key={x}>{x}</option>
              ))}
              {!httpOnly && <option value="WS">WS (WebSocket)</option>}
            </select>
          </div>
          <div className="field span-8">
            <label>Path</label>
            <input
              className={`input mono ${pathOk ? '' : 'invalid'}`}
              value={step.path}
              placeholder={isWS ? '/ws?token={{token}}' : '/resource/{{id}}'}
              onChange={(e) => m((s) => void (s.path = e.target.value))}
            />
          </div>
          <div className="field span-2">
            <label>Think time</label>
            <input
              className={`input mono ${durOk(step.think_time) ? '' : 'invalid'}`}
              value={step.think_time}
              placeholder="0s"
              onChange={(e) => m((s) => void (s.think_time = e.target.value))}
            />
          </div>
        </div>

        <div className="sub-block">
          <div className="label">Headers</div>
          <KVEditor rows={step.headers} onChange={(rows) => m((s) => void (s.headers = rows))} />
        </div>

        {showBody ? (
          <div className="sub-block">
            <div className="label">Body</div>
            <textarea
              className="input"
              rows={Math.min(8, Math.max(3, step.body.split('\n').length))}
              value={step.body}
              placeholder='{"key": "{{var}}"}'
              onChange={(e) => m((s) => void (s.body = e.target.value))}
            />
          </div>
        ) : null}

        {isWS ? <WSBlock step={step} m={m} /> : null}

        <div className="sub-block">
          <div className="label">
            {isWS ? 'Extract (from the last expected message)' : 'Extract'}
            <button
              className="btn sm ghost"
              onClick={() => m((s) => void s.extract.push({ var: '', from: 'json', expr: '' }))}
            >
              <Icon name="plus" size={12} /> Add
            </button>
          </div>
          {step.extract.map((x, j) => (
            <div className="row" key={j} style={{ marginBottom: 6 }}>
              <input
                className="input mono"
                style={{ flex: '0 0 22%' }}
                placeholder="variable"
                value={x.var}
                onChange={(e) => m((s) => void (s.extract[j].var = e.target.value))}
              />
              <select
                className="input"
                style={{ flex: '0 0 110px' }}
                value={x.from}
                onChange={(e) => m((s) => void (s.extract[j].from = e.target.value as ExtractSource))}
              >
                {extractSources.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
              <input
                className="input mono"
                placeholder={x.from === 'json' ? 'data.items[0].id' : x.from === 'header' ? 'X-Request-Id' : 'token=(\\w+)'}
                value={x.expr}
                onChange={(e) => m((s) => void (s.extract[j].expr = e.target.value))}
              />
              <button className="btn ghost icon sm" onClick={() => m((s) => void s.extract.splice(j, 1))} aria-label="Remove">
                <Icon name="x" size={14} />
              </button>
            </div>
          ))}
        </div>

        {isWS ? null : (
          <div className="sub-block">
            <div className="label">
              Assertions
              <button
                className="btn sm ghost"
                onClick={() => m((s) => void s.assert.push({ type: 'status', expr: '', op: 'eq', value: '200' }))}
              >
                <Icon name="plus" size={12} /> Add
              </button>
            </div>
            {step.assert.map((a, j) => (
              <div className="row" key={j} style={{ marginBottom: 6 }}>
                <select
                  className="input"
                  style={{ flex: '0 0 130px' }}
                  value={a.type}
                  onChange={(e) =>
                    m((s) => {
                      const t = e.target.value as AssertType;
                      s.assert[j].type = t;
                      if (!ASSERT_OPS[t].includes(s.assert[j].op)) s.assert[j].op = ASSERT_OPS[t][0];
                    })
                  }
                >
                  {ASSERT_TYPES.map((o) => (
                    <option key={o}>{o}</option>
                  ))}
                </select>
                {a.type === 'json' ? (
                  <input
                    className="input mono"
                    style={{ flex: '0 0 26%' }}
                    placeholder="json path"
                    value={a.expr}
                    onChange={(e) => m((s) => void (s.assert[j].expr = e.target.value))}
                  />
                ) : null}
                <select
                  className="input"
                  style={{ flex: '0 0 96px' }}
                  value={a.op}
                  onChange={(e) => m((s) => void (s.assert[j].op = e.target.value))}
                >
                  {!ASSERT_OPS[a.type].includes(a.op) && <option value={a.op}>{a.op}</option>}
                  {ASSERT_OPS[a.type].map((o) => (
                    <option key={o} value={o}>
                      {OP_SYMBOL[o]} {o}
                    </option>
                  ))}
                </select>
                <input
                  className="input mono"
                  placeholder={a.type === 'status' ? '200' : a.type === 'duration' ? '200ms' : 'value'}
                  value={a.value}
                  onChange={(e) => m((s) => void (s.assert[j].value = e.target.value))}
                />
                <button className="btn ghost icon sm" onClick={() => m((s) => void s.assert.splice(j, 1))} aria-label="Remove">
                  <Icon name="x" size={14} />
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

/** Settings of a WebSocket step: hold, pings and the message exchange. */
function WSBlock({ step, m }: { step: FormStep; m: StepMutator }) {
  const ws = step.ws;
  return (
    <div className="sub-block">
      <div className="form-grid">
        <div className="field span-3">
          <label>Hold</label>
          <input
            className={`input mono ${durOk(ws.hold) ? '' : 'invalid'}`}
            value={ws.hold}
            placeholder="0s"
            title="Keep the connection open this long after the messages (not counted as latency)"
            onChange={(e) => m((s) => void (s.ws.hold = e.target.value))}
          />
        </div>
        <div className="field span-3">
          <label>Ping every</label>
          <input
            className={`input mono ${durOk(ws.ping_every) ? '' : 'invalid'}`}
            value={ws.ping_every}
            placeholder="off"
            onChange={(e) => m((s) => void (s.ws.ping_every = e.target.value))}
          />
        </div>
        <div className="field span-6">
          <label>Ping message</label>
          <input
            className="input mono"
            value={ws.ping_message}
            placeholder="empty = protocol ping frame"
            onChange={(e) => m((s) => void (s.ws.ping_message = e.target.value))}
          />
        </div>
      </div>
      <div className="label" style={{ marginTop: 8 }}>
        Messages
        <button
          className="btn sm ghost"
          onClick={() => m((s) => void s.ws.messages.push({ send: '', expect: '', timeout: '' }))}
        >
          <Icon name="plus" size={12} /> Add
        </button>
      </div>
      {ws.messages.map((msg, j) => (
        <div className="row" key={j} style={{ marginBottom: 6 }}>
          <input
            className="input mono"
            placeholder='send, e.g. {"type":"join"}'
            value={msg.send}
            onChange={(e) => m((s) => void (s.ws.messages[j].send = e.target.value))}
          />
          <input
            className="input mono"
            style={{ flex: '0 0 26%' }}
            placeholder="expect (substring)"
            value={msg.expect}
            onChange={(e) => m((s) => void (s.ws.messages[j].expect = e.target.value))}
          />
          <input
            className={`input mono ${durOk(msg.timeout) ? '' : 'invalid'}`}
            style={{ flex: '0 0 90px' }}
            placeholder="timeout"
            value={msg.timeout}
            onChange={(e) => m((s) => void (s.ws.messages[j].timeout = e.target.value))}
          />
          <button className="btn ghost icon sm" onClick={() => m((s) => void s.ws.messages.splice(j, 1))} aria-label="Remove">
            <Icon name="x" size={14} />
          </button>
        </div>
      ))}
    </div>
  );
}

/** Sticky in-page navigation for the long advanced form. */
function SectionNav({ items }: { items: { id: string; label: string; meta?: string }[] }) {
  const [active, setActive] = useState(items[0]?.id ?? '');
  useEffect(() => {
    const on = () => {
      let cur = items[0]?.id ?? '';
      for (const it of items) {
        const el = document.getElementById(it.id);
        if (el && el.getBoundingClientRect().top < 140) cur = it.id;
      }
      setActive(cur);
    };
    on();
    window.addEventListener('scroll', on, { passive: true });
    return () => window.removeEventListener('scroll', on);
    // ids are static; only labels change with the form
  }, []);
  return (
    <nav className="section-nav" aria-label="Form sections">
      {items.map((it) => (
        <button
          key={it.id}
          className={active === it.id ? 'on' : ''}
          onClick={() => {
            const el = document.getElementById(it.id);
            if (el) window.scrollTo({ top: el.getBoundingClientRect().top + window.scrollY - 56, behavior: 'smooth' });
          }}
        >
          {it.label}
          {it.meta !== undefined && <span className="sn-meta">{it.meta}</span>}
        </button>
      ))}
    </nav>
  );
}

const AUTH_LABEL: Record<AuthType, string> = {
  none: 'None',
  login: 'Login request',
  bearer: 'Bearer token',
  basic: 'Basic',
};

/** The `auth:` block: how iterations authenticate (types.go Auth). */
function AuthSection({ auth, mutate, dataEnabled }: { auth: FormAuth; mutate: Mutator; dataEnabled: boolean }) {
  const ma = (fn: (a: FormAuth) => void) => mutate((f) => fn(f.auth));
  const preview = authHeaderPreview(auth);
  const loginVars = auth.type === 'login' ? [...auth.login.body.matchAll(/\{\{\s*([\w.-]+)\s*\}\}/g)].map((x) => x[1]) : [];
  return (
    <Section
      id="sec-auth"
      title="Authentication"
      extra={
        <div className="segmented" role="radiogroup" aria-label="Authentication type">
          {(Object.keys(AUTH_LABEL) as AuthType[]).map((t) => (
            <button
              key={t}
              className={auth.type === t ? 'on' : ''}
              role="radio"
              aria-checked={auth.type === t}
              onClick={() =>
                ma((a) => {
                  a.type = t;
                  if (t === 'login' && !a.login.path) a.login = defaultLoginStep();
                })
              }
            >
              {AUTH_LABEL[t]}
            </button>
          ))}
        </div>
      }
    >
      {auth.type === 'none' ? (
        <div className="hint">
          No authentication. Choose <strong>Login request</strong> to log in once per user (or once in total) and reuse
          the token, instead of making login a step of every iteration.
        </div>
      ) : (
        <div className="stack" style={{ gap: 12 }}>
          {auth.type === 'login' && (
            <>
              <div className="form-grid">
                <div className="field span-6">
                  <label>Scope</label>
                  <select
                    className="input"
                    value={auth.scope || 'per_user'}
                    onChange={(e) => ma((a) => void (a.scope = e.target.value as FormAuth['scope']))}
                  >
                    <option value="per_user">per_user — each distinct credential logs in once</option>
                    <option value="shared">shared — one login shared by every iteration</option>
                  </select>
                </div>
                <div className="field span-3">
                  <label>Refresh after</label>
                  <input
                    className={`input mono ${durOk(auth.refresh) ? '' : 'invalid'}`}
                    placeholder="never"
                    value={auth.refresh}
                    title="Log in again when the cached login is older than this (empty = never)"
                    onChange={(e) => ma((a) => void (a.refresh = e.target.value))}
                  />
                </div>
                <div className="hint span-3" style={{ alignSelf: 'end', paddingBottom: 6 }}>
                  A 401 from any step drops the cached login.
                </div>
              </div>
              <StepEditor
                step={auth.login}
                m={(fn) => ma((a) => fn(a.login))}
                label="login"
                httpOnly
                nameOptional
              />
              {auth.login.extract.length === 0 && auth.inject && (
                <div className="alert warn">
                  The login request needs at least one extract (e.g. token from json). For a login that only sets a session cookie, turn
                  off &quot;Sent with every step&quot; below.
                </div>
              )}
              {loginVars.length > 0 && !dataEnabled && (
                <div className="alert warn">
                  The login body uses <code>{`{{${loginVars[0]}}}`}</code> but no CSV data file is enabled (Test data
                  below).
                </div>
              )}
            </>
          )}
          {auth.type === 'bearer' && (
            <div className="field">
              <label>Token</label>
              <input
                className="input mono"
                placeholder="eyJhbGciOi…  or  {{api_key}}"
                value={auth.token}
                onChange={(e) => ma((a) => void (a.token = e.target.value))}
              />
            </div>
          )}
          {auth.type === 'basic' && (
            <div className="form-grid">
              <div className="field span-6">
                <label>Username</label>
                <input
                  className="input mono"
                  placeholder="{{user}}"
                  value={auth.username}
                  onChange={(e) => ma((a) => void (a.username = e.target.value))}
                />
              </div>
              <div className="field span-6">
                <label>Password</label>
                <input
                  className="input mono"
                  placeholder="{{pass}}"
                  value={auth.password}
                  onChange={(e) => ma((a) => void (a.password = e.target.value))}
                />
              </div>
            </div>
          )}
          <div className="form-grid">
            <div className="field span-4">
              <label>Header</label>
              <input
                className="input mono"
                placeholder="Authorization"
                value={auth.header}
                onChange={(e) => ma((a) => void (a.header = e.target.value))}
              />
            </div>
            {auth.type !== 'basic' && (
              <div className="field span-5">
                <label>Value template</label>
                <input
                  className="input mono"
                  placeholder={auth.type === 'login' ? 'Bearer {{token}}' : 'Bearer <token>'}
                  value={auth.value}
                  onChange={(e) => ma((a) => void (a.value = e.target.value))}
                />
              </div>
            )}
            <div className="span-3" style={{ alignSelf: 'end', paddingBottom: 7 }}>
              <label className="checkbox" title="Add the header to every HTTP step and WebSocket handshake">
                <input type="checkbox" checked={auth.inject} onChange={(e) => ma((a) => void (a.inject = e.target.checked))} />
                Add to every request
              </label>
            </div>
          </div>
          {preview && (
            <div className="auth-preview">
              <span className="label" style={{ margin: 0 }}>
                {auth.inject ? 'Sent with every step' : 'Not injected'}
              </span>
              <code>
                {preview.header}: {preview.value}
              </code>
              {!auth.inject && auth.type === 'login' && (
                <span className="hint">Use the extracted variables in your own headers, e.g. {'{{token}}'}.</span>
              )}
            </div>
          )}
        </div>
      )}
    </Section>
  );
}
