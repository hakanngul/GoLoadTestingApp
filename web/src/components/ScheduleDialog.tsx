import { useEffect, useMemo, useState } from 'react';
import { APIError, api } from '../api/client';
import type { OverlapPolicy, Schedule, ScheduleKind, SchedulePreview, ScheduleRequest, Test } from '../api/types';
import { fmtSeconds } from '../lib/format';
import { useAsync, useDebounced, useNow } from '../lib/hooks';
import { summarizeYaml, unitFor } from '../lib/scenario';
import {
  browserTimezone,
  buildCron,
  buildLoadOverride,
  cronToRecurrence,
  DAY_SHORT,
  defaultOnce,
  errorsFor,
  fieldErrors,
  fmtInZone,
  fmtRelative,
  loadFormFromOverride,
  overrideProfile,
  targetFields,
  timezoneList,
  WEEK_ORDER,
  type LoadForm,
  type LoadMode,
  type Preset,
  type Recurrence,
  type ScheduleTarget,
  unitForModel,
} from '../lib/schedule';
import { ProfileChart } from './charts';
import { ErrorAlert, Icon, Modal } from './ui';

const PRESETS: { id: Preset; label: string }[] = [
  { id: 'daily', label: 'Daily' },
  { id: 'weekdays', label: 'Weekdays' },
  { id: 'weekly', label: 'Weekly' },
  { id: 'cron', label: 'Cron' },
];

const LOAD_MODES: { id: LoadMode; label: string }[] = [
  { id: 'keep', label: 'Scenario load' },
  { id: 'scale', label: 'Scale peak' },
  { id: 'profile', label: 'Ramp & hold' },
];

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

/**
 * Creates or edits a schedule. `test` fixes the test (Schedule button on a
 * test); otherwise `tests` offers a picker and a Test | Suite switch
 * (Schedules page); a suite schedule picks a suite and has no load/workers.
 */
export function ScheduleDialog({
  test,
  tests,
  schedule,
  onClose,
  onSaved,
}: {
  test?: Test;
  tests?: Test[];
  schedule?: Schedule;
  onClose: () => void;
  onSaved: (s: Schedule) => void;
}) {
  const now = useNow(30000);
  const initialTz = schedule?.timezone || browserTimezone();
  const initialOnce = schedule?.kind === 'once' && schedule.at ? schedule.at.split('T') : null;
  const fallbackOnce = defaultOnce(initialTz, Date.now());

  const [target, setTarget] = useState<ScheduleTarget>(schedule?.suite_id ? 'suite' : 'test');
  const isSuite = !test && target === 'suite';
  const suites = useAsync(async () => (test ? [] : await api.listSuites()), []);
  const [suiteId, setSuiteId] = useState(schedule?.suite_id ?? '');
  useEffect(() => {
    // Preselect the first suite once the list is there.
    if (!suiteId && suites.data && suites.data.length > 0) setSuiteId(suites.data[0].id);
  }, [suiteId, suites.data]);
  const chosenSuite = suites.data?.find((su) => su.id === suiteId);
  const [testId, setTestId] = useState(schedule?.test_id ?? test?.id ?? tests?.[0]?.id ?? '');
  const [name, setName] = useState(schedule?.name ?? (test ? `${test.name} nightly` : ''));
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [kind, setKind] = useState<ScheduleKind>(schedule?.kind ?? 'recurring');
  const [date, setDate] = useState(initialOnce?.[0] ?? fallbackOnce.date);
  const [time, setTime] = useState(initialOnce?.[1] ?? fallbackOnce.time);
  const [rec, setRec] = useState<Recurrence>(() =>
    schedule?.kind === 'recurring' && schedule.cron
      ? cronToRecurrence(schedule.cron)
      : { preset: 'weekdays', time: '03:00', days: [1, 3, 5], cron: '0 3 * * 1-5' },
  );
  const [tz, setTz] = useState(initialTz);
  const [workers, setWorkers] = useState(String(Math.max(1, schedule?.workers ?? 1)));
  const [overlap, setOverlap] = useState<OverlapPolicy>(schedule?.overlap ?? 'skip');
  const [queueMin, setQueueMin] = useState(String(Math.round((schedule?.queue_timeout_ms ?? 1_800_000) / 60_000)));
  const [label, setLabel] = useState(schedule?.label ?? '');
  const [notes, setNotes] = useState(schedule?.notes ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [fe, setFe] = useState<Record<string, string[]>>({});

  const chosen = test ?? tests?.find((t) => t.id === testId);
  const [testYaml, setTestYaml] = useState(chosen?.yaml ?? '');
  useEffect(() => {
    if (chosen?.yaml) {
      setTestYaml(chosen.yaml);
      return;
    }
    if (!testId) return;
    let alive = true;
    api
      .getTest(testId)
      .then((t) => alive && setTestYaml(t.yaml))
      .catch(() => alive && setTestYaml(''));
    return () => {
      alive = false;
    };
  }, [testId, chosen?.yaml]);

  const sum = useMemo(() => summarizeYaml(testYaml), [testYaml]);
  const model = sum?.model ?? 'closed';
  const [load, setLoad] = useState<LoadForm>(() => loadFormFromOverride(schedule?.load, model, sum?.peak ?? 10));
  // Follow the scenario's model until the user picks an override.
  useEffect(() => {
    if (!sum || schedule?.load) return;
    setLoad((l) => (l.mode === 'keep' ? { ...l, unit: unitForModel(sum.model), target: String(sum.peak) } : l));
  }, [sum, schedule?.load]);
  const zones = useMemo(() => timezoneList(tz, browserTimezone()), [tz]);

  const at = `${date}T${time}`;
  const cron = buildCron(rec);
  const previewKey = useDebounced(JSON.stringify({ kind, at, cron, tz }), 300);
  const [preview, setPreview] = useState<SchedulePreview | null>(null);
  useEffect(() => {
    const req = JSON.parse(previewKey) as { kind: ScheduleKind; at: string; cron: string; tz: string };
    let alive = true;
    api
      .previewSchedule({
        kind: req.kind,
        timezone: req.tz,
        count: 5,
        ...(req.kind === 'once' ? { at: req.at } : { cron: req.cron }),
      })
      .then((p) => alive && setPreview(p))
      .catch(() => alive && setPreview(null));
    return () => {
      alive = false;
    };
  }, [previewKey]);

  const local = useMemo(() => overrideProfile(load, sum ? { model: sum.model, stages: sum.stages } : null), [load, sum]);
  const built = isSuite ? { load: undefined, errors: {} as Record<string, string> } : buildLoadOverride(load, model);
  const allErrors: Record<string, string[]> = { ...fe };
  for (const [k, v] of Object.entries(built.errors)) allErrors[k] = [v];
  const nWorkers = Number(workers);
  const workersOk = isSuite || (Number.isInteger(nWorkers) && nWorkers >= 1 && nWorkers <= 1000);
  const nQueue = Number(queueMin);
  const queueOk = overlap === 'skip' || (Number.isFinite(nQueue) && nQueue >= 1 && nQueue <= 1440);
  const canSave =
    !busy &&
    (isSuite ? !!suiteId : !!testId) &&
    name.trim() !== '' &&
    workersOk &&
    queueOk &&
    Object.keys(built.errors).length === 0 &&
    (kind === 'once' ? !!date && !!time : cron !== '');

  const save = async () => {
    setBusy(true);
    setError(null);
    setFe({});
    const req: ScheduleRequest = {
      ...targetFields(isSuite ? 'suite' : 'test', testId, suiteId),
      name: name.trim(),
      enabled,
      kind,
      timezone: tz,
      workers: isSuite ? 0 : nWorkers,
      label: label.trim(),
      notes,
      overlap,
      ...(kind === 'once' ? { at } : { cron }),
      ...(built.load ? { load: built.load } : {}),
      ...(overlap === 'queue' ? { queue_timeout_ms: Math.round(nQueue * 60_000) } : {}),
    };
    try {
      const saved = schedule ? await api.updateSchedule(schedule.id, req) : await api.createSchedule(req);
      onSaved(saved);
    } catch (e) {
      if (e instanceof APIError && e.details.length > 0) setFe(fieldErrors(e.details));
      setError(e);
      setBusy(false);
    }
  };

  const unkeyed = fe[''] ?? [];
  const generalError =
    error instanceof APIError && error.details.length > 0
      ? unkeyed.length
        ? new APIError(error.status, error.message, unkeyed)
        : new APIError(error.status, `${error.message} — see the highlighted fields`)
      : error;
  const unit = (u: 'users' | 'rate') => (u === 'users' ? 'VUs' : 'it/s');
  const tzDiffers = tz !== browserTimezone();

  return (
    <Modal
      title={
        <span className="row" style={{ gap: 8 }}>
          <Icon name="calendar" size={15} />
          {schedule ? 'Edit schedule' : isSuite ? 'Schedule a suite' : 'Schedule a test'}
          {(isSuite ? chosenSuite : chosen) && (
            <span className="mono text-2" style={{ fontSize: 13, fontWeight: 500 }}>
              {(isSuite ? chosenSuite : chosen)?.name}
            </span>
          )}
        </span>
      }
      onClose={onClose}
      width={900}
      footer={
        <>
          <label className="checkbox" style={{ marginRight: 'auto' }}>
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Enabled
          </label>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={save} disabled={!canSave}>
            <Icon name="check" size={13} /> {busy ? 'Saving…' : schedule ? 'Save changes' : 'Create schedule'}
          </button>
        </>
      }
    >
      <div className="sched-layout">
        <div className="stack" style={{ gap: 16, minWidth: 0 }}>
          {/* ---------- what ---------- */}
          <div className="sched-grid">
            {!test && (
              <div className="field">
                <div className="row" style={{ justifyContent: 'space-between' }}>
                  <label htmlFor={isSuite ? 'sch-suite' : 'sch-test'}>{isSuite ? 'Suite' : 'Test'}</label>
                  <div className="segmented" role="tablist" aria-label="Schedule target">
                    <button className={!isSuite ? 'on' : ''} onClick={() => setTarget('test')} role="tab" aria-selected={!isSuite}>
                      Test
                    </button>
                    <button className={isSuite ? 'on' : ''} onClick={() => setTarget('suite')} role="tab" aria-selected={isSuite}>
                      Suite
                    </button>
                  </div>
                </div>
                {isSuite ? (
                  <>
                    <select id="sch-suite" className="input" value={suiteId} onChange={(e) => setSuiteId(e.target.value)}>
                      {(suites.data ?? []).map((su) => (
                        <option key={su.id} value={su.id}>
                          {su.name}
                        </option>
                      ))}
                    </select>
                    {suites.data && suites.data.length === 0 && <div className="hint">No suites yet — create one on the Suites page.</div>}
                    {suites.error ? <ErrorAlert error={suites.error} title="Failed to load suites" /> : null}
                    <FieldErrors msgs={errorsFor(fe, 'suite_id', 'items/suite_id', 'items')} />
                  </>
                ) : (
                  <>
                    <select
                      id="sch-test"
                      className="input"
                      value={testId}
                      onChange={(e) => setTestId(e.target.value)}
                      disabled={!!schedule && !tests}
                    >
                      {(tests ?? []).map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.name}
                        </option>
                      ))}
                    </select>
                    <FieldErrors msgs={errorsFor(fe, 'test_id')} />
                  </>
                )}
              </div>
            )}
            <div className="field" style={test ? { gridColumn: 'span 2' } : undefined}>
              <label htmlFor="sch-name">Name</label>
              <input
                id="sch-name"
                className={`input ${errorsFor(fe, 'name').length ? 'invalid' : ''}`}
                maxLength={200}
                placeholder="e.g. Nightly regression"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
              <FieldErrors msgs={errorsFor(fe, 'name')} />
            </div>
          </div>

          {/* ---------- when ---------- */}
          <section className="sched-section">
            <div className="sched-section-title">When</div>
            <div className="row wrap" style={{ gap: 10 }}>
              <div className="segmented" role="tablist" aria-label="Schedule type">
                <button className={kind === 'once' ? 'on' : ''} onClick={() => setKind('once')} role="tab" aria-selected={kind === 'once'}>
                  Once
                </button>
                <button
                  className={kind === 'recurring' ? 'on' : ''}
                  onClick={() => setKind('recurring')}
                  role="tab"
                  aria-selected={kind === 'recurring'}
                >
                  Recurring
                </button>
              </div>
              {kind === 'recurring' && (
                <div className="segmented" aria-label="Repeat">
                  {PRESETS.map((p) => (
                    <button
                      key={p.id}
                      className={rec.preset === p.id ? 'on' : ''}
                      onClick={() =>
                        setRec((r) => ({
                          ...r,
                          preset: p.id,
                          cron: p.id === 'cron' && r.preset !== 'cron' ? buildCron(r) || r.cron : r.cron,
                        }))
                      }
                    >
                      {p.label}
                    </button>
                  ))}
                </div>
              )}
            </div>

            <div className="sched-grid" style={{ marginTop: 12 }}>
              {kind === 'once' ? (
                <>
                  <div className="field">
                    <label htmlFor="sch-date">Date</label>
                    <input
                      id="sch-date"
                      type="date"
                      className={`input ${errorsFor(fe, 'at').length ? 'invalid' : ''}`}
                      value={date}
                      onChange={(e) => setDate(e.target.value)}
                    />
                    <FieldErrors msgs={errorsFor(fe, 'at')} />
                  </div>
                  <div className="field">
                    <label htmlFor="sch-time">Time</label>
                    <input id="sch-time" type="time" className="input" value={time} onChange={(e) => setTime(e.target.value)} />
                  </div>
                </>
              ) : rec.preset === 'cron' ? (
                <div className="field" style={{ gridColumn: 'span 2' }}>
                  <label htmlFor="sch-cron">Cron expression</label>
                  <input
                    id="sch-cron"
                    className={`input mono ${errorsFor(fe, 'cron').length || (preview && !preview.valid) ? 'invalid' : ''}`}
                    placeholder="minute hour day-of-month month day-of-week"
                    value={rec.cron}
                    onChange={(e) => setRec((r) => ({ ...r, cron: e.target.value }))}
                  />
                  <div className="hint">
                    5 fields: <code>minute hour day month weekday</code>, e.g. <code>*/30 8-18 * * 1-5</code> (every 30 min, 08–18 h,
                    Mon–Fri). <code>@daily</code>, <code>@hourly</code> also work.
                  </div>
                </div>
              ) : (
                <>
                  <div className="field">
                    <label htmlFor="sch-rtime">Time</label>
                    <input
                      id="sch-rtime"
                      type="time"
                      className="input"
                      value={rec.time}
                      onChange={(e) => setRec((r) => ({ ...r, time: e.target.value }))}
                    />
                  </div>
                  {rec.preset === 'weekly' ? (
                    <div className="field">
                      <span className="label">Days</span>
                      <div className="day-chips" role="group" aria-label="Days of the week">
                        {WEEK_ORDER.map((d) => {
                          const on = rec.days.includes(d);
                          return (
                            <button
                              key={d}
                              className={`day-chip ${on ? 'on' : ''}`}
                              aria-pressed={on}
                              onClick={() => setRec((r) => ({ ...r, days: on ? r.days.filter((x) => x !== d) : [...r.days, d] }))}
                            >
                              {DAY_SHORT[d].slice(0, 2)}
                            </button>
                          );
                        })}
                      </div>
                    </div>
                  ) : (
                    <div className="field">
                      <span className="label">Repeats</span>
                      <div className="hint" style={{ paddingTop: 7 }}>
                        {rec.preset === 'daily' ? 'Every day' : 'Monday to Friday'}
                      </div>
                    </div>
                  )}
                </>
              )}
              <div className="field" style={{ gridColumn: 'span 2' }}>
                <label htmlFor="sch-tz">Time zone</label>
                <select
                  id="sch-tz"
                  className={`input ${errorsFor(fe, 'timezone').length ? 'invalid' : ''}`}
                  value={tz}
                  onChange={(e) => setTz(e.target.value)}
                >
                  {zones.map((z) => (
                    <option key={z} value={z}>
                      {z === browserTimezone() ? `${z} (this browser)` : z}
                    </option>
                  ))}
                </select>
                <FieldErrors msgs={errorsFor(fe, ...(kind === 'once' ? ['timezone', 'kind'] : ['at', 'cron', 'timezone', 'kind']))} />
              </div>
            </div>
          </section>

          {/* ---------- load ---------- */}
          {isSuite ? (
            <section className="sched-section">
              <div className="sched-section-title">Load</div>
              <div className="hint">Each test in the suite uses its own load (set in the suite).</div>
            </section>
          ) : (
            <section className="sched-section">
              <div className="sched-section-title">Load</div>
              <div className="segmented" aria-label="Load">
                {LOAD_MODES.map((m) => (
                  <button key={m.id} className={load.mode === m.id ? 'on' : ''} onClick={() => setLoad((l) => ({ ...l, mode: m.id }))}>
                    {m.label}
                  </button>
                ))}
              </div>
              <div className="hint" style={{ marginTop: 8 }}>
                {load.mode === 'keep' &&
                  (sum
                    ? `Runs the scenario as saved: ${sum.model} model, peak ${sum.peak} ${unitFor(sum.model)}, ${fmtSeconds(sum.total)}.`
                    : 'Runs the scenario as saved.')}
                {load.mode === 'scale' &&
                  'Keeps the stage shape and duration; every stage target is scaled so the peak becomes the new value.'}
                {load.mode === 'profile' &&
                  'Replaces the stages with ramp-up → hold → optional ramp-down. Virtual users run the closed model, it/s the open model.'}{' '}
                The saved test is never changed.
              </div>
              {load.mode !== 'keep' && (
                <div className="sched-grid" style={{ marginTop: 12 }}>
                  <div className="field">
                    <label htmlFor="sch-target">{load.mode === 'scale' ? 'New peak' : 'Target'}</label>
                    <div className="input-unit">
                      <input
                        id="sch-target"
                        className={`input num ${errorsFor(allErrors, 'load.users', 'load.rate').length ? 'invalid' : ''}`}
                        inputMode="numeric"
                        value={load.target}
                        onChange={(e) => setLoad((l) => ({ ...l, target: e.target.value }))}
                      />
                      <select
                        className="input"
                        aria-label="Unit"
                        value={load.unit}
                        onChange={(e) => setLoad((l) => ({ ...l, unit: e.target.value as 'users' | 'rate' }))}
                      >
                        <option value="users">VUs</option>
                        <option value="rate">it/s</option>
                      </select>
                    </div>
                  </div>
                  {load.mode === 'profile' ? (
                    <div className="field">
                      <span className="label">Ramp-up · Hold · Ramp-down</span>
                      <div className="dur-triple">
                        <input
                          aria-label="Ramp-up"
                          className={`input mono ${allErrors['load.ramp_up_ms'] ? 'invalid' : ''}`}
                          placeholder="30s"
                          value={load.rampUp}
                          onChange={(e) => setLoad((l) => ({ ...l, rampUp: e.target.value }))}
                        />
                        <input
                          aria-label="Hold"
                          className={`input mono ${allErrors['load.hold_ms'] ? 'invalid' : ''}`}
                          placeholder="10m"
                          value={load.hold}
                          onChange={(e) => setLoad((l) => ({ ...l, hold: e.target.value }))}
                        />
                        <input
                          aria-label="Ramp-down"
                          className={`input mono ${allErrors['load.ramp_down_ms'] ? 'invalid' : ''}`}
                          placeholder="0s"
                          value={load.rampDown}
                          onChange={(e) => setLoad((l) => ({ ...l, rampDown: e.target.value }))}
                        />
                      </div>
                    </div>
                  ) : (
                    <div className="field">
                      <span className="label">Scenario peak</span>
                      <div className="hint" style={{ paddingTop: 7 }}>
                        {sum ? `${sum.peak} ${unitFor(sum.model)} → ${load.target || '?'} ${unit(load.unit)}` : '—'}
                      </div>
                    </div>
                  )}
                  <div style={{ gridColumn: 'span 2' }}>
                    <FieldErrors msgs={errorsFor(allErrors, 'load')} />
                  </div>
                </div>
              )}
            </section>
          )}

          {/* ---------- run options ---------- */}
          <section className="sched-section">
            <div className="sched-section-title">Run options</div>
            <div className="sched-grid three">
              {!isSuite && (
                <div className="field">
                  <label htmlFor="sch-workers">Workers</label>
                  <input
                    id="sch-workers"
                    className={`input num ${workersOk ? '' : 'invalid'}`}
                    type="number"
                    min={1}
                    max={1000}
                    value={workers}
                    onChange={(e) => setWorkers(e.target.value)}
                  />
                </div>
              )}
              <div className="field">
                <label htmlFor="sch-overlap">If a run is active</label>
                <select id="sch-overlap" className="input" value={overlap} onChange={(e) => setOverlap(e.target.value as OverlapPolicy)}>
                  <option value="skip">Skip this run</option>
                  <option value="queue">Wait, then run</option>
                </select>
              </div>
              <div className="field">
                <label htmlFor="sch-queue">Wait at most</label>
                <div className="input-unit">
                  <input
                    id="sch-queue"
                    className={`input num ${queueOk ? '' : 'invalid'}`}
                    inputMode="numeric"
                    value={queueMin}
                    disabled={overlap !== 'queue'}
                    onChange={(e) => setQueueMin(e.target.value)}
                  />
                  <span className="unit-addon">min</span>
                </div>
              </div>
              <div className="field">
                <label htmlFor="sch-label">
                  Run label <span className="label-opt">optional</span>
                </label>
                <input
                  id="sch-label"
                  className={`input mono ${errorsFor(fe, 'label').length ? 'invalid' : ''}`}
                  maxLength={100}
                  placeholder={`scheduled: ${name.trim() || '…'}`}
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                />
              </div>
              <div className="field" style={{ gridColumn: 'span 2' }}>
                <label htmlFor="sch-notes">
                  Notes <span className="label-opt">optional, copied to each run</span>
                </label>
                <input id="sch-notes" className="input" maxLength={10000} value={notes} onChange={(e) => setNotes(e.target.value)} />
              </div>
              <div style={{ gridColumn: '1 / -1' }}>
                <FieldErrors msgs={errorsFor(fe, 'workers', 'overlap', 'queue_timeout_ms', 'label', 'notes')} />
              </div>
            </div>
          </section>
          <ErrorAlert error={generalError} title={schedule ? 'Could not save' : 'Could not create schedule'} />
        </div>

        {/* ---------- aside ---------- */}
        <aside className="sched-aside">
          <div className="sched-card">
            <div className="sched-card-title">
              <Icon name="clock" size={13} /> Next runs
            </div>
            {!preview ? (
              <div className="hint">…</div>
            ) : !preview.valid ? (
              <div className="stack" style={{ gap: 4 }}>
                {preview.errors.map((e, i) => (
                  <div key={i} className="field-err">
                    {e}
                  </div>
                ))}
              </div>
            ) : (
              <ol className="next-runs">
                {preview.times.map((t) => (
                  <li key={t}>
                    <span className="nr-when">{fmtInZone(t, tz)}</span>
                    <span className="nr-rel">{fmtRelative(t, now)}</span>
                    {tzDiffers && <span className="nr-local">{fmtInZone(t, browserTimezone())} here</span>}
                  </li>
                ))}
              </ol>
            )}
            <div className="hint sched-note">
              The server must be running at these times. Runs it misses while stopped are recorded as <b>missed</b>, not run late.
            </div>
          </div>
          {!isSuite && (
            <div className="sched-card">
              <div className="sched-card-title">
                <Icon name="runs" size={13} /> Load of each run
              </div>
              {local ? (
                <>
                  <div className="hint" style={{ marginBottom: 4 }}>
                    {local.model} · peak {local.peak} {unitFor(local.model)} · {fmtSeconds(local.total)}
                  </div>
                  <ProfileChart points={local.points} unit={unitFor(local.model)} height={130} />
                </>
              ) : (
                <div className="hint">Enter a valid load to see the profile.</div>
              )}
            </div>
          )}
        </aside>
      </div>
    </Modal>
  );
}
