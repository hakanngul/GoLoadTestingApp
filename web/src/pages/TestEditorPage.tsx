import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { api } from '../api/client';
import type { FileInfo, Test, TryFix, TryResult, ValidateResult } from '../api/types';
import { ProfileChart } from '../components/charts';
import { QuickFormView } from '../components/QuickFormView';
import { RunStartDialog } from '../components/RunStartDialog';
import { ScheduleDialog } from '../components/ScheduleDialog';
import { ScenarioFormView } from '../components/ScenarioFormView';
import { TryPanel } from '../components/TryPanel';
import { ErrorAlert, Icon, Loading } from '../components/ui';
import { YamlEditor } from '../components/YamlEditor';
import { fmtBytes, fmtSeconds } from '../lib/format';
import { useDebounced } from '../lib/hooks';
import { defaultQuick, formToQuick, quickToForm, type QuickForm } from '../lib/quick';
import { applyFix } from '../lib/trial';
import {
  defaultForm,
  formToYaml,
  stagesToProfile,
  unitFor,
  yamlToForm,
  type ScenarioForm,
} from '../lib/scenario';

type Tab = 'form' | 'yaml';
type Mode = 'quick' | 'advanced';

const modeKey = (id: string) => `goload.editorMode.${id}`;
function loadMode(id: string): Mode | null {
  try {
    const v = localStorage.getItem(modeKey(id));
    return v === 'quick' || v === 'advanced' ? v : null;
  } catch {
    return null;
  }
}
function storeMode(id: string, m: Mode) {
  try {
    localStorage.setItem(modeKey(id), m);
  } catch {
    /* storage unavailable: the mode is just not remembered */
  }
}

/** Extracts "line N" references from validation / parse errors. */
function errorLines(msgs: string[]): number[] {
  const out: number[] = [];
  for (const m of msgs) {
    const re = /line (\d+)/g;
    let x: RegExpExecArray | null;
    while ((x = re.exec(m)) !== null) out.push(Number(x[1]));
  }
  return out;
}

export function TestEditorPage() {
  const { id } = useParams<{ id: string }>();
  const nav = useNavigate();
  const [params] = useSearchParams();
  const isNew = !id;
  const newMode: Mode = params.get('mode') === 'advanced' ? 'advanced' : 'quick';

  const [test, setTest] = useState<Test | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [yaml, setYaml] = useState('');
  const [savedYaml, setSavedYaml] = useState<string | null>(null);
  const [form, setForm] = useState<ScenarioForm | null>(null);
  const [parseErr, setParseErr] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>('form');
  const [saving, setSaving] = useState(false);
  const [saveErr, setSaveErr] = useState<unknown>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [validation, setValidation] = useState<ValidateResult | null>(null);
  const [validating, setValidating] = useState(false);
  const [validateErr, setValidateErr] = useState<unknown>(null);
  const [runOpen, setRunOpen] = useState(false);
  const [schedOpen, setSchedOpen] = useState(false);
  const [mode, setModeState] = useState<Mode>('advanced');
  /** Quick-mode state; null while in advanced mode or when the YAML stopped being representable. */
  const [quick, setQuick] = useState<QuickForm | null>(null);
  const [filesFlash, setFilesFlash] = useState(false);
  // ----- test request -----
  const [tryOpen, setTryOpen] = useState(false);
  const [tryBusy, setTryBusy] = useState(false);
  const [tryRes, setTryRes] = useState<TryResult | null>(null);
  const [tryErr, setTryErr] = useState<unknown>(null);
  const [tryYaml, setTryYaml] = useState<string | null>(null);

  // ----- load -----
  useEffect(() => {
    setLoadErr(null);
    setSaveErr(null);
    if (isNew) {
      const q = newMode === 'quick' ? defaultQuick() : null;
      const f = q ? quickToForm(q) : defaultForm();
      const y = formToYaml(f);
      setTest(null);
      setForm(f);
      setYaml(y);
      setSavedYaml(null);
      setParseErr(null);
      setModeState(newMode);
      setQuick(q);
      setTab('form');
      return;
    }
    let alive = true;
    api
      .getTest(id)
      .then((t) => {
        if (!alive) return;
        setTest(t);
        setYaml(t.yaml);
        setSavedYaml(t.yaml);
        const p = yamlToForm(t.yaml);
        setForm(p.form);
        setParseErr(p.error);
        // Quick when representable, unless the user last chose Advanced for this test.
        const q = p.form ? formToQuick(p.form).quick : null;
        const m: Mode = q && loadMode(id) !== 'advanced' ? 'quick' : 'advanced';
        setModeState(m);
        setQuick(m === 'quick' ? q : null);
      })
      .catch((e) => alive && setLoadErr(e));
    return () => {
      alive = false;
    };
  }, [id, isNew, newMode]);

  // ----- toast -----
  useEffect(() => {
    if (!toast) return;
    const h = window.setTimeout(() => setToast(null), 2500);
    return () => window.clearTimeout(h);
  }, [toast]);

  // ----- sync -----
  const onYamlChange = (text: string) => {
    setYaml(text);
    const p = yamlToForm(text);
    setParseErr(p.error);
    if (p.form) {
      setForm(p.form);
      if (mode === 'quick') setQuick(formToQuick(p.form).quick);
    }
  };

  const formRef = useRef(form);
  formRef.current = form;
  const mutate = useCallback((fn: (f: ScenarioForm) => void) => {
    const cur = formRef.current;
    if (!cur) return;
    const next = structuredClone(cur);
    fn(next);
    setForm(next);
    setYaml(formToYaml(next));
    setParseErr(null);
  }, []);

  const quickRef = useRef(quick);
  quickRef.current = quick;
  const mutateQuick = useCallback((fn: (q: QuickForm) => void) => {
    const cur = quickRef.current;
    if (!cur) return;
    const next = structuredClone(cur);
    fn(next);
    const f = quickToForm(next, formRef.current);
    setQuick(next);
    setForm(f);
    setYaml(formToYaml(f));
    setParseErr(null);
  }, []);

  /** Why the current scenario cannot be shown in Quick mode ([] when it can). */
  const quickReasons = useMemo(() => {
    if (parseErr || !form) return ['The YAML cannot be parsed'];
    return formToQuick(form).reasons;
  }, [form, parseErr]);

  const switchMode = (m: Mode) => {
    if (m === mode) return;
    if (m === 'quick') {
      const q = form && !parseErr ? formToQuick(form).quick : null;
      if (!q) return;
      setQuick(q);
    } else setQuick(null);
    setModeState(m);
    if (id) storeMode(id, m);
  };

  const showFiles = () => {
    const el = document.getElementById('data-files');
    el?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    setFilesFlash(true);
    window.setTimeout(() => setFilesFlash(false), 1600);
  };

  // ----- live validation (debounced 400ms) -----
  const debouncedYaml = useDebounced(yaml, 400);
  useEffect(() => {
    if (!debouncedYaml.trim()) return;
    let alive = true;
    setValidating(true);
    api
      .validate(debouncedYaml, isNew ? undefined : id)
      .then((r) => {
        if (!alive) return;
        setValidation(r);
        setValidateErr(null);
      })
      .catch((e) => alive && setValidateErr(e))
      .finally(() => alive && setValidating(false));
    return () => {
      alive = false;
    };
  }, [debouncedYaml, id, isNew]);

  // ----- warn on unload with unsaved changes -----
  const dirty = savedYaml === null ? true : yaml !== savedYaml;
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener('beforeunload', h);
    return () => window.removeEventListener('beforeunload', h);
  }, [dirty]);

  const name = (form?.name ?? test?.name ?? '').trim() || 'untitled';

  const save = async () => {
    setSaving(true);
    setSaveErr(null);
    try {
      if (isNew) {
        const t = await api.createTest(name, yaml);
        storeMode(t.id, mode);
        setSavedYaml(yaml);
        setToast('Test created');
        nav(`/tests/${t.id}`, { replace: true });
      } else {
        const t = await api.updateTest(id, name, yaml);
        setTest(t);
        setSavedYaml(yaml);
        setToast('Saved');
      }
    } catch (e) {
      setSaveErr(e);
    } finally {
      setSaving(false);
    }
  };

  // Ctrl/Cmd+S
  const saveRef = useRef(save);
  saveRef.current = save;
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        void saveRef.current();
      }
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, []);

  const sendTry = async () => {
    setTryOpen(true);
    setTryBusy(true);
    setTryErr(null);
    const sent = yaml;
    try {
      const r = await api.tryRequest(sent, isNew ? undefined : id);
      setTryRes(r);
      setTryYaml(sent);
    } catch (e) {
      setTryErr(e);
      setTryRes(null);
    } finally {
      setTryBusy(false);
    }
  };

  /** Applies a hint's fix to the form (and Quick state); false if it no longer applies. */
  const applyTryFix = (fix: TryFix): boolean => {
    const cur = formRef.current;
    if (!cur || parseErr) return false;
    const next = applyFix(cur, fix);
    if (!next) return false;
    setForm(next);
    setYaml(formToYaml(next));
    setParseErr(null);
    if (mode === 'quick') {
      const q = formToQuick(next).quick;
      if (q) setQuick(q);
    }
    setToast(`Applied: ${fix.label}`);
    return true;
  };

  const profile = useMemo(() => (form ? stagesToProfile(form.load.stages) : null), [form]);
  const lines = useMemo(
    () => errorLines([...(validation?.errors ?? []), ...(parseErr ? [parseErr] : [])]),
    [validation, parseErr],
  );

  if (loadErr) {
    return (
      <>
        <div className="breadcrumb">
          <Link to="/">Tests</Link> /
        </div>
        <ErrorAlert error={loadErr} title="Could not load test" />
      </>
    );
  }
  if (!form && !parseErr) return <Loading />;

  const files: FileInfo[] = test?.files ?? [];
  const unit = form ? unitFor(form.load.model) : 'it/s';

  return (
    <>
      <div className="page-head">
        <div style={{ minWidth: 0 }}>
          <div className="breadcrumb">
            <Link to="/">Tests</Link> / {isNew ? 'new' : name}
          </div>
          <h1 className="row" style={{ gap: 10 }}>
            {isNew ? (mode === 'quick' ? 'New quick test' : 'New test') : name}
            {dirty && !isNew && <span className="badge warn plain">unsaved</span>}
          </h1>
        </div>
        <span className="spacer" />
        <ModeSwitch mode={mode} onChange={switchMode} quickReasons={quickReasons} />
        {!isNew && (
          <Link className="btn ghost" to={`/runs?test=${id}`}>
            History
          </Link>
        )}
        <button
          className="btn"
          onClick={sendTry}
          disabled={tryBusy || !!parseErr || validation?.valid === false}
          title={
            parseErr || validation?.valid === false
              ? 'Fix the validation problems first'
              : 'Send one iteration of the current (unsaved) scenario and explain failures'
          }
        >
          {tryBusy ? <span className="spinner" /> : <Icon name="external" size={12} />} Test request
        </button>
        <button
          className="btn"
          disabled={isNew || dirty}
          title={isNew || dirty ? 'Save changes before running' : 'Start a run'}
          onClick={() => setRunOpen(true)}
        >
          <Icon name="play" size={11} /> Run
        </button>
        <button
          className="btn"
          disabled={isNew || dirty}
          title={isNew || dirty ? 'Save changes before scheduling' : 'Plan runs of this test'}
          onClick={() => setSchedOpen(true)}
        >
          <Icon name="calendar" size={13} /> Schedule
        </button>
        <button className="btn primary" onClick={save} disabled={saving || (!dirty && !isNew)}>
          {saving ? 'Saving…' : isNew ? 'Create test' : 'Save'}
        </button>
      </div>

      {saveErr ? (
        <div style={{ marginBottom: 12 }}>
          <ErrorAlert error={saveErr} title="Save failed" />
        </div>
      ) : null}

      <div className="editor-layout">
        <div style={{ minWidth: 0 }}>
          <div className="tabs" style={{ marginBottom: 12 }}>
            <button className={tab === 'form' ? 'on' : ''} onClick={() => setTab('form')}>
              {mode === 'quick' ? 'Quick form' : 'Form'}
            </button>
            <button className={tab === 'yaml' ? 'on' : ''} onClick={() => setTab('yaml')}>
              YAML
            </button>
          </div>
          {tab === 'form' ? (
            parseErr || !form ? (
              <div className="alert warn">
                <div className="alert-title">The YAML cannot be parsed, so the form is unavailable.</div>
                <div className="mono" style={{ marginTop: 6, whiteSpace: 'pre-wrap', fontSize: 12 }}>
                  {parseErr}
                </div>
                <button className="btn sm" style={{ marginTop: 8 }} onClick={() => setTab('yaml')}>
                  Fix in YAML editor
                </button>
              </div>
            ) : mode === 'quick' ? (
              quick ? (
                <QuickFormView
                  quick={quick}
                  set={mutateQuick}
                  form={form}
                  files={files}
                  saved={!isNew}
                  onShowFiles={showFiles}
                />
              ) : (
                <div className="alert warn">
                  <div className="alert-title">This scenario no longer fits Quick mode.</div>
                  <ul>
                    {quickReasons.map((r) => (
                      <li key={r}>{r}</li>
                    ))}
                  </ul>
                  <button className="btn sm" style={{ marginTop: 8 }} onClick={() => switchMode('advanced')}>
                    Continue in Advanced
                  </button>
                </div>
              )
            ) : (
              <ScenarioFormView form={form} mutate={mutate} files={files} />
            )
          ) : (
            <div className="stack" style={{ gap: 8 }}>
              <YamlEditor value={yaml} onChange={onYamlChange} errorLines={lines} />
              <div className="hint">
                Edits here update the form. Editing in the form regenerates the YAML (comments are not preserved).
              </div>
            </div>
          )}
        </div>

        <aside className="editor-side">
          <div className="card">
            <div className="card-head">
              <h2>Validation</h2>
              <span className="spacer" />
              {validating && <span className="spinner" />}
            </div>
            <div className="card-body stack" style={{ gap: 8 }}>
              {validateErr ? (
                <ErrorAlert error={validateErr} title="Validation unavailable" />
              ) : !validation ? (
                <span className="muted">Validating…</span>
              ) : validation.valid ? (
                <>
                <div className="validation-summary">
                  <span className="good">
                    <Icon name="check" size={14} />
                  </span>
                  <span>
                    <strong>Valid</strong>
                    <span className="muted">
                      {' · '}
                      {validation.model ?? '—'} · {fmtSeconds(validation.duration_s)} · {validation.steps ?? 0} step
                      {validation.steps === 1 ? '' : 's'}
                    </span>
                  </span>
                </div>
                {(validation.warnings ?? []).length > 0 && (
                  <>
                    <div className="validation-summary">
                      <span className="warn">
                        <Icon name="alert" size={14} />
                      </span>
                      <strong>
                        {validation.warnings!.length} warning{validation.warnings!.length === 1 ? '' : 's'}
                      </strong>
                    </div>
                    <ul className="err-list warn-list">
                      {validation.warnings!.map((w, i) => (
                        <li key={i}>{w}</li>
                      ))}
                    </ul>
                  </>
                )}
                </>
              ) : (
                <>
                  <div className="validation-summary">
                    <span className="bad">
                      <Icon name="alert" size={14} />
                    </span>
                    <strong>
                      {validation.errors?.length ?? 0} problem{validation.errors?.length === 1 ? '' : 's'}
                    </strong>
                  </div>
                  <ul className="err-list">
                    {(validation.errors ?? []).map((e, i) => (
                      <li key={i}>{e}</li>
                    ))}
                  </ul>
                </>
              )}
            </div>
          </div>

          {mode === 'advanced' && (
          <div className="card">
            <div className="card-head">
              <h2>Load profile</h2>
              <span className="spacer" />
              {profile && (
                <span className="muted num">
                  peak {profile.peak} {unit} · {fmtSeconds(profile.total)}
                </span>
              )}
            </div>
            <div style={{ padding: '8px 6px 4px 0' }}>
              <ProfileChart points={profile?.points ?? []} unit={unit} height={150} />
            </div>
          </div>
          )}

          <FilesPanel
            testId={isNew ? null : id}
            files={files}
            referenced={form?.data.enabled ? form.data.file : ''}
            flash={filesFlash}
            onChanged={(fs) => test && setTest({ ...test, files: fs })}
            onToast={setToast}
          />
        </aside>
      </div>

      {tryOpen && (
        <TryPanel
          result={tryRes}
          loading={tryBusy}
          error={tryErr}
          stale={tryRes !== null && tryYaml !== yaml}
          onClose={() => setTryOpen(false)}
          onRerun={sendTry}
          onApply={applyTryFix}
        />
      )}
      {runOpen && test && <RunStartDialog test={{ ...test, yaml }} onClose={() => setRunOpen(false)} />}
      {schedOpen && test && (
        <ScheduleDialog
          test={{ ...test, yaml }}
          onClose={() => setSchedOpen(false)}
          onSaved={(sc) => {
            setSchedOpen(false);
            nav(`/schedules?focus=${sc.id}`);
          }}
        />
      )}
      {toast && <div className="toast">{toast}</div>}
    </>
  );
}

function FilesPanel({
  testId,
  files,
  referenced,
  flash,
  onChanged,
  onToast,
}: {
  testId: string | null;
  files: FileInfo[];
  referenced: string;
  flash?: boolean;
  onChanged: (f: FileInfo[]) => void;
  onToast: (s: string) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const refresh = async () => {
    if (!testId) return;
    const t = await api.getTest(testId);
    onChanged(t.files ?? []);
  };

  const upload = async (f: File) => {
    if (!testId) return;
    setBusy(f.name);
    setErr(null);
    try {
      await api.uploadFile(testId, f.name, f);
      await refresh();
      onToast(`Uploaded ${f.name}`);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(null);
      if (input.current) input.current.value = '';
    }
  };

  const remove = async (name: string) => {
    if (!testId || !window.confirm(`Delete ${name}?`)) return;
    setBusy(name);
    setErr(null);
    try {
      await api.deleteFile(testId, name);
      await refresh();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className={`card ${flash ? 'flash' : ''}`} id="data-files">
      <div className="card-head">
        <h2>Data files</h2>
        <span className="spacer" />
        <input
          ref={input}
          type="file"
          accept=".csv,text/csv"
          hidden
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void upload(f);
          }}
        />
        <button
          className="btn sm"
          disabled={!testId || busy !== null}
          title={testId ? 'Upload CSV' : 'Save the test first'}
          onClick={() => input.current?.click()}
        >
          <Icon name="upload" size={13} /> Upload CSV
        </button>
      </div>
      <div className="card-body" style={{ paddingTop: 6, paddingBottom: 6 }}>
        {!testId ? (
          <div className="hint" style={{ padding: '6px 0' }}>
            Create the test first, then attach CSV files here.
          </div>
        ) : files.length === 0 ? (
          <div className="hint" style={{ padding: '6px 0' }}>
            No files attached.
          </div>
        ) : (
          files.map((f) => (
            <div className="file-row" key={f.name}>
              <Icon name="file" size={14} />
              <span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {f.name}
              </span>
              {referenced === f.name && <span className="badge info plain">in use</span>}
              <span className="grow" />
              <span className="muted num">{fmtBytes(f.size)}</span>
              <button
                className="btn ghost icon sm danger"
                onClick={() => remove(f.name)}
                disabled={busy !== null}
                aria-label={`Delete ${f.name}`}
              >
                <Icon name="trash" size={13} />
              </button>
            </div>
          ))
        )}
        {referenced && testId && !files.some((f) => f.name === referenced) && (
          <div className="alert warn" style={{ marginTop: 6 }}>
            <span className="mono">{referenced}</span> is referenced by <code>data.file</code> but not uploaded — runs
            will fail to start.
          </div>
        )}
        {busy && <div className="hint">Working on {busy}…</div>}
        {err ? <ErrorAlert error={err} /> : null}
      </div>
    </div>
  );
}

/** Quick | Advanced. Quick is disabled (with the reasons on hover) when the scenario does not fit it. */
function ModeSwitch({
  mode,
  onChange,
  quickReasons,
}: {
  mode: Mode;
  onChange: (m: Mode) => void;
  quickReasons: string[];
}) {
  const blocked = mode !== 'quick' && quickReasons.length > 0;
  return (
    <div className="mode-switch">
      <span className="label" style={{ margin: 0 }}>
        Editor
      </span>
      <div className="segmented" role="radiogroup" aria-label="Editor mode">
        <span className={`tip-wrap ${blocked ? 'blocked' : ''}`} tabIndex={blocked ? 0 : undefined}>
          <button
            role="radio"
            aria-checked={mode === 'quick'}
            className={mode === 'quick' ? 'on' : ''}
            disabled={blocked}
            aria-describedby={blocked ? 'quick-blocked' : undefined}
            onClick={() => onChange('quick')}
          >
            Quick
          </button>
          {blocked && (
            <span className="tip" role="tooltip" id="quick-blocked">
              <strong>Quick mode is unavailable for this scenario</strong>
              <ul>
                {quickReasons.map((r) => (
                  <li key={r}>{r}</li>
                ))}
              </ul>
              <span className="tip-foot">Quick mode edits one HTTP request with a ramp-up / hold profile.</span>
            </span>
          )}
        </span>
        <button
          role="radio"
          aria-checked={mode === 'advanced'}
          className={mode === 'advanced' ? 'on' : ''}
          onClick={() => onChange('advanced')}
        >
          Advanced
        </button>
      </div>
    </div>
  );
}
