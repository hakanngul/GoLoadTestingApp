import type { ReactNode } from 'react';
import type { FileInfo } from '../api/types';
import {
  ASSERT_OPS,
  ASSERT_TYPES,
  DATA_MODES,
  EXTRACT_SOURCES,
  METHODS,
  THRESHOLD_METRICS,
  THRESHOLD_OPS,
  OP_SYMBOL,
  emptyStep,
  parseDuration,
  unitFor,
  type AssertType,
  type DataMode,
  type ExtractSource,
  type FormStep,
  type LoadModel,
  type ScenarioForm,
} from '../lib/scenario';
import { KVEditor } from './KVEditor';
import { Icon } from './ui';

type Mutator = (fn: (f: ScenarioForm) => void) => void;

function Section({ title, extra, children }: { title: string; extra?: ReactNode; children: ReactNode }) {
  return (
    <div className="card">
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

  return (
    <div className="stack" style={{ gap: 12 }}>
      <Section title="General & target">
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
        </div>
      </Section>

      <Section
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
    </div>
  );
}

function StepCard({ index, step, count, mutate }: { index: number; step: FormStep; count: number; mutate: Mutator }) {
  const m: (fn: (s: FormStep) => void) => void = (fn) => mutate((f) => fn(f.steps[index]));
  const showBody = !['GET', 'HEAD'].includes(step.method) || step.body !== '';
  const move = (d: number) =>
    mutate((f) => {
      const j = index + d;
      if (j < 0 || j >= f.steps.length) return;
      [f.steps[index], f.steps[j]] = [f.steps[j], f.steps[index]];
    });

  return (
    <div className="step-card">
      <div className="step-head">
        <span className="step-index">#{index + 1}</span>
        <input
          className="input"
          style={{ maxWidth: 260, fontWeight: 600 }}
          value={step.name}
          placeholder="step name"
          onChange={(e) => m((s) => void (s.name = e.target.value))}
        />
        <span className={`method ${step.method}`}>{step.method}</span>
        <span className="mono muted" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {step.path}
        </span>
        <span className="grow" />
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
      </div>
      <div className="step-body">
        <div className="form-grid">
          <div className="field span-2">
            <label>Method</label>
            <select className="input mono" value={step.method} onChange={(e) => m((s) => void (s.method = e.target.value))}>
              {METHODS.map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
          </div>
          <div className="field span-8">
            <label>Path</label>
            <input
              className={`input mono ${step.path.startsWith('/') || step.path.startsWith('{{') ? '' : 'invalid'}`}
              value={step.path}
              placeholder="/resource/{{id}}"
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

        <div className="sub-block">
          <div className="label">
            Extract
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
                {EXTRACT_SOURCES.map((o) => (
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
      </div>
    </div>
  );
}
