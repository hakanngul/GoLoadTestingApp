import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { api } from '../api/client';
import type { FileInfo, Test, ValidateResult } from '../api/types';
import { ProfileChart } from '../components/charts';
import { RunStartDialog } from '../components/RunStartDialog';
import { ScenarioFormView } from '../components/ScenarioFormView';
import { ErrorAlert, Icon, Loading } from '../components/ui';
import { YamlEditor } from '../components/YamlEditor';
import { fmtBytes, fmtSeconds } from '../lib/format';
import { useDebounced } from '../lib/hooks';
import {
  defaultForm,
  formToYaml,
  stagesToProfile,
  unitFor,
  yamlToForm,
  type ScenarioForm,
} from '../lib/scenario';

type Tab = 'form' | 'yaml';

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
  const isNew = !id;

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

  // ----- load -----
  useEffect(() => {
    setLoadErr(null);
    setSaveErr(null);
    if (isNew) {
      const f = defaultForm();
      const y = formToYaml(f);
      setTest(null);
      setForm(f);
      setYaml(y);
      setSavedYaml(null);
      setParseErr(null);
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
      })
      .catch((e) => alive && setLoadErr(e));
    return () => {
      alive = false;
    };
  }, [id, isNew]);

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
    if (p.form) setForm(p.form);
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

  // ----- live validation (debounced 400ms) -----
  const debouncedYaml = useDebounced(yaml, 400);
  useEffect(() => {
    if (!debouncedYaml.trim()) return;
    let alive = true;
    setValidating(true);
    api
      .validate(debouncedYaml)
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
  }, [debouncedYaml]);

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
            {isNew ? 'New test' : name}
            {dirty && !isNew && <span className="badge warn plain">unsaved</span>}
          </h1>
        </div>
        <span className="spacer" />
        {!isNew && (
          <Link className="btn ghost" to={`/runs?test=${id}`}>
            History
          </Link>
        )}
        <button
          className="btn"
          disabled={isNew || dirty}
          title={isNew || dirty ? 'Save changes before running' : 'Start a run'}
          onClick={() => setRunOpen(true)}
        >
          <Icon name="play" size={11} /> Run
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
              Form
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

          <FilesPanel
            testId={isNew ? null : id}
            files={files}
            referenced={form?.data.enabled ? form.data.file : ''}
            onChanged={(fs) => test && setTest({ ...test, files: fs })}
            onToast={setToast}
          />
        </aside>
      </div>

      {runOpen && test && <RunStartDialog test={{ ...test, yaml }} onClose={() => setRunOpen(false)} />}
      {toast && <div className="toast">{toast}</div>}
    </>
  );
}

function FilesPanel({
  testId,
  files,
  referenced,
  onChanged,
  onToast,
}: {
  testId: string | null;
  files: FileInfo[];
  referenced: string;
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
    <div className="card">
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
            Save the test to attach CSV files.
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
