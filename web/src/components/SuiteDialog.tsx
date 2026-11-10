import { useMemo, useState } from 'react';
import { APIError, api } from '../api/client';
import type { Suite, Test } from '../api/types';
import { fmtSeconds } from '../lib/format';
import { summarizeYaml, unitFor } from '../lib/scenario';
import { buildLoadOverride, describeLoad, errorsFor, fieldErrors, unitForModel, type LoadForm } from '../lib/schedule';
import { emptySuiteForm, formToRequest, moveItem, newFormItem, suiteToForm, type SuiteForm, type TestLoadInfo } from '../lib/suite';
import { ErrorAlert, Icon, Modal } from './ui';

function FieldErrors({ msgs }: { msgs: string[] }) {
  if (msgs.length === 0) return null;
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

/** Creates or edits a test suite: name, how it runs and its ordered tests. */
export function SuiteDialog({
  tests,
  suite,
  onClose,
  onSaved,
}: {
  tests: Test[];
  suite?: Suite;
  onClose: () => void;
  onSaved: (s: Suite) => void;
}) {
  // Load model, peak and duration of each test's saved scenario.
  const infos = useMemo(() => {
    const m = new Map<string, TestLoadInfo & { total: number }>();
    for (const t of tests) {
      const sum = summarizeYaml(t.yaml);
      if (sum) m.set(t.id, { model: sum.model, peak: sum.peak, total: sum.total });
    }
    return m;
  }, [tests]);
  const infoOf = (id: string) => infos.get(id);
  const [form, setForm] = useState<SuiteForm>(() => (suite ? suiteToForm(suite, infoOf) : emptySuiteForm()));
  const [openLoad, setOpenLoad] = useState('');
  const [pick, setPick] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [fe, setFe] = useState<Record<string, string[]>>({});

  const names = useMemo(() => new Map(tests.map((t) => [t.id, t.name])), [tests]);
  const sorted = useMemo(() => [...tests].sort((a, b) => a.name.localeCompare(b.name)), [tests]);
  const set = (patch: Partial<SuiteForm>) => setForm((f) => ({ ...f, ...patch }));
  const setItems = (fn: (items: SuiteForm['items']) => SuiteForm['items']) => setForm((f) => ({ ...f, items: fn(f.items) }));

  const add = (testId: string) => {
    if (!testId) return;
    setItems((items) => [...items, newFormItem(testId, infoOf(testId))]);
    setPick('');
  };

  const seqMode = form.mode === 'sequential';
  const canSave = !busy && form.name.trim() !== '' && form.items.length > 0;

  const save = async () => {
    setBusy(true);
    setError(null);
    setFe({});
    try {
      const { req, errors } = formToRequest(form, infoOf);
      if (Object.keys(errors).length > 0) {
        setFe(Object.fromEntries(Object.entries(errors).map(([k, v]) => [k, [v]])));
        const bad = /^items\[(\d+)\]/.exec(Object.keys(errors)[0]);
        if (bad) setOpenLoad(form.items[Number(bad[1])]?.key ?? '');
        setBusy(false);
        return;
      }
      const saved = suite ? await api.updateSuite(suite.id, req) : await api.createSuite(req);
      onSaved(saved);
    } catch (e) {
      if (e instanceof APIError && e.details.length > 0) setFe(fieldErrors(e.details));
      setError(e);
      setBusy(false);
    }
  };

  return (
    <Modal
      title={
        <span className="row" style={{ gap: 8 }}>
          <Icon name="suite" size={15} />
          {suite ? 'Edit suite' : 'New test suite'}
        </span>
      }
      onClose={onClose}
      width={720}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={save} disabled={!canSave}>
            <Icon name="check" size={13} /> {busy ? 'Saving…' : suite ? 'Save changes' : 'Create suite'}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 16 }}>
        <div className="field">
          <label htmlFor="suite-name">Name</label>
          <input
            id="suite-name"
            className={`input ${errorsFor(fe, 'name').length ? 'invalid' : ''}`}
            maxLength={100}
            placeholder="e.g. Release smoke"
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
            autoFocus
          />
          <FieldErrors msgs={errorsFor(fe, 'name')} />
        </div>
        <div className="field">
          <label htmlFor="suite-desc">Description (optional)</label>
          <textarea
            id="suite-desc"
            className="input"
            rows={2}
            maxLength={2000}
            value={form.description}
            onChange={(e) => set({ description: e.target.value })}
          />
        </div>

        <section className="sched-section">
          <div className="sched-section-title">How it runs</div>
          <div className="row wrap" style={{ gap: 12 }}>
            <div className="segmented" role="tablist" aria-label="Run mode">
              <button className={seqMode ? 'on' : ''} onClick={() => set({ mode: 'sequential' })} role="tab" aria-selected={seqMode}>
                One after another
              </button>
              <button className={!seqMode ? 'on' : ''} onClick={() => set({ mode: 'parallel' })} role="tab" aria-selected={!seqMode}>
                All at once
              </button>
            </div>
            {seqMode && (
              <>
                <label className="checkbox">
                  <input type="checkbox" checked={form.stop_on_failure} onChange={(e) => set({ stop_on_failure: e.target.checked })} /> Stop
                  on first failure
                </label>
                <label className="row" style={{ gap: 6 }}>
                  <span className="text-2">Pause between tests</span>
                  <input
                    className="input num"
                    style={{ width: 80 }}
                    type="number"
                    min={0}
                    max={3600}
                    value={form.gap_s}
                    onChange={(e) => set({ gap_s: e.target.valueAsNumber })}
                    aria-label="Pause between tests in seconds"
                  />
                  <span className="text-2">s</span>
                </label>
              </>
            )}
          </div>
          <FieldErrors msgs={errorsFor(fe, 'mode', 'gap_ms')} />
          <p className="hint" style={{ margin: '8px 0 0' }}>
            {seqMode
              ? 'Each test starts when the previous one has finished — results stay clean and comparable.'
              : 'Every test starts at the same time and shares this machine’s CPU and network. Use workers or sequential mode when exact numbers matter.'}
          </p>
        </section>

        <section className="sched-section">
          <div className="sched-section-title">Test cases ({form.items.length})</div>
          {form.items.length === 0 ? (
            <div className="muted" style={{ padding: '6px 0 10px' }}>
              No tests yet — add the first one below.
            </div>
          ) : (
            <ol className="suite-items">
              {form.items.map((it, i) => {
                const name = names.get(it.test_id);
                return (
                  <li key={it.key} className="suite-item">
                    <span className="suite-item-no">{i + 1}</span>
                    <span className={`suite-item-name ${name ? '' : 'muted'}`}>{name ?? 'deleted test'}</span>
                    <label className="row" style={{ gap: 6 }} title="Remote workers for this test (1 = run on this server)">
                      <span className="text-2 small-text">workers</span>
                      <input
                        className="input num"
                        style={{ width: 64 }}
                        type="number"
                        min={1}
                        max={1000}
                        value={Math.max(1, it.workers)}
                        onChange={(e) => {
                          const v = e.target.valueAsNumber;
                          setItems((items) =>
                            items.map((x) =>
                              x.key === it.key
                                ? {
                                    ...x,
                                    workers: Number.isFinite(v) && v > 1 ? v : 0,
                                  }
                                : x,
                            ),
                          );
                        }}
                        aria-label={`Workers for test ${i + 1}`}
                      />
                    </label>
                    <button
                      className={`btn sm load-btn ${it.load.mode === 'keep' ? 'ghost' : 'on'} ${
                        errorsFor(fe, `items[${i}].load`).length ? 'invalid' : ''
                      }`}
                      onClick={() => setOpenLoad((k) => (k === it.key ? '' : it.key))}
                      aria-expanded={openLoad === it.key}
                      title="Load for this test in the suite"
                    >
                      {loadLabel(it.load, infoOf(it.test_id)?.model ?? 'closed')} <Icon name="chevron" size={12} />
                    </button>
                    <button
                      className="btn sm ghost icon"
                      onClick={() => setItems((items) => moveItem(items, i, -1))}
                      disabled={i === 0}
                      aria-label={`Move test ${i + 1} up`}
                    >
                      <Icon name="up" size={13} />
                    </button>
                    <button
                      className="btn sm ghost icon"
                      onClick={() => setItems((items) => moveItem(items, i, 1))}
                      disabled={i === form.items.length - 1}
                      aria-label={`Move test ${i + 1} down`}
                    >
                      <Icon name="down" size={13} />
                    </button>
                    <button
                      className="btn sm ghost icon danger"
                      onClick={() => setItems((items) => items.filter((x) => x.key !== it.key))}
                      aria-label={`Remove test ${i + 1}`}
                    >
                      <Icon name="x" size={13} />
                    </button>
                    <FieldErrors msgs={errorsFor(fe, `items[${i}].test_id`, `items[${i}].workers`)} />
                    {openLoad === it.key && (
                      <ItemLoadEditor
                        load={it.load}
                        info={infos.get(it.test_id)}
                        errors={errorsFor(fe, `items[${i}].load`)}
                        onChange={(load) => {
                          setItems((items) => items.map((x) => (x.key === it.key ? { ...x, load } : x)));
                          // Clear this item's stale load errors as the user fixes them.
                          setFe((cur) => Object.fromEntries(Object.entries(cur).filter(([k]) => !k.startsWith(`items[${i}].load`))));
                        }}
                      />
                    )}
                  </li>
                );
              })}
            </ol>
          )}
          <div className="row" style={{ gap: 8 }}>
            <select className="input" value={pick} onChange={(e) => add(e.target.value)} aria-label="Add a test" style={{ maxWidth: 360 }}>
              <option value="">+ Add a test…</option>
              {sorted.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </div>
          <FieldErrors msgs={errorsFor(fe, 'items')} />
        </section>

        <ErrorAlert error={error} title={suite ? 'Could not save the suite' : 'Could not create the suite'} />
      </div>
    </Modal>
  );
}

/** Short label of an item's load for its row button. */
function loadLabel(f: LoadForm, model: 'open' | 'closed'): string {
  if (f.mode === 'keep') return "Test's load";
  const built = buildLoadOverride(f, model);
  return built.load ? describeLoad(built.load) : 'Load: check values';
}

const ITEM_LOAD_MODES: { id: LoadForm['mode']; label: string }[] = [
  { id: 'keep', label: "Test's own" },
  { id: 'scale', label: 'Scale peak' },
  { id: 'profile', label: 'Ramp & hold' },
];

/** Inline editor of one item's load override (same semantics as schedules). */
function ItemLoadEditor({
  load,
  info,
  errors,
  onChange,
}: {
  load: LoadForm;
  info: (TestLoadInfo & { total: number }) | undefined;
  errors: string[];
  onChange: (l: LoadForm) => void;
}) {
  const set = (patch: Partial<LoadForm>) => onChange({ ...load, ...patch });
  return (
    <div className="suite-load">
      <div className="row wrap" style={{ gap: 10 }}>
        <div className="segmented" aria-label="Load">
          {ITEM_LOAD_MODES.map((m) => (
            <button
              key={m.id}
              className={load.mode === m.id ? 'on' : ''}
              onClick={() =>
                set(
                  m.id === 'keep' || load.mode !== 'keep'
                    ? { mode: m.id }
                    : { mode: m.id, unit: unitForModel(info?.model ?? 'closed'), target: String(info?.peak || 10) },
                )
              }
            >
              {m.label}
            </button>
          ))}
        </div>
        {load.mode !== 'keep' && (
          <div className="input-unit" style={{ width: 170 }}>
            <input
              className={`input num ${errors.length ? 'invalid' : ''}`}
              inputMode="numeric"
              aria-label={load.mode === 'scale' ? 'New peak' : 'Target'}
              value={load.target}
              onChange={(e) => set({ target: e.target.value })}
            />
            <select
              className="input"
              aria-label="Unit"
              value={load.unit}
              onChange={(e) => set({ unit: e.target.value as LoadForm['unit'] })}
            >
              <option value="users">VUs</option>
              <option value="rate">it/s</option>
            </select>
          </div>
        )}
        {load.mode === 'profile' && (
          <div className="dur-triple" style={{ width: 260 }}>
            <input
              aria-label="Ramp-up"
              className="input mono"
              placeholder="30s"
              value={load.rampUp}
              onChange={(e) => set({ rampUp: e.target.value })}
            />
            <input
              aria-label="Hold"
              className="input mono"
              placeholder="5m"
              value={load.hold}
              onChange={(e) => set({ hold: e.target.value })}
            />
            <input
              aria-label="Ramp-down"
              className="input mono"
              placeholder="0s"
              value={load.rampDown}
              onChange={(e) => set({ rampDown: e.target.value })}
            />
          </div>
        )}
      </div>
      <div className="hint" style={{ marginTop: 6 }}>
        {load.mode === 'keep' &&
          (info
            ? `Runs the test as saved: ${info.model} model, peak ${info.peak} ${unitFor(info.model)}, ${fmtSeconds(info.total)}.`
            : 'Runs the test as saved.')}
        {load.mode === 'scale' && 'Keeps the stage shape and duration; scales every stage so the peak becomes this value.'}
        {load.mode === 'profile' && 'Ramp-up → hold → ramp-down. VUs run the closed model, it/s the open model.'} The saved test is not
        changed; the run is marked as a load override.
      </div>
      <FieldErrors msgs={errors} />
    </div>
  );
}
