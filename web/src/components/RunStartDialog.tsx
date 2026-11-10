import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { APIError, api } from '../api/client';
import type { StartRunRequest, Test } from '../api/types';
import { useAsync } from '../lib/hooks';
import { idleRemoteCount, remoteWorkers } from '../lib/workers';
import { fmtSeconds } from '../lib/format';
import { stagesToProfile, summarizeYaml, unitFor } from '../lib/scenario';
import { ProfileChart } from './charts';
import { ErrorAlert, Icon, Modal } from './ui';

/** Dialog that starts a run of a saved test and navigates to its live view. */
export function RunStartDialog({ test, onClose }: { test: Test; onClose: () => void }) {
  const nav = useNavigate();
  const [workers, setWorkers] = useState('1');
  const [label, setLabel] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [full, setFull] = useState<Test>(test);
  const fleet = useAsync(() => api.listWorkers(), [], 5000);

  // The list endpoint may omit YAML in the future; make sure we have it.
  useEffect(() => {
    if (!test.yaml) {
      api.getTest(test.id).then(setFull).catch(setError);
    }
  }, [test]);

  const sum = summarizeYaml(full.yaml);
  const profile = sum ? stagesToProfile(sum.stages) : null;
  const unit = sum ? unitFor(sum.model) : 'it/s';
  const n = Number(workers);
  const workersValid = Number.isInteger(n) && n >= 1 && n <= 64;
  const remote = remoteWorkers(fleet.data ?? []);
  const connected = remote.filter((w) => w.state !== 'lost').length;
  const idle = idleRemoteCount(fleet.data ?? []);
  const notEnough = error instanceof APIError && error.status === 409 && /idle workers/i.test(error.message);

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const req: StartRunRequest = { workers: n };
      if (label.trim()) req.label = label.trim();
      if (notes.trim()) req.notes = notes;
      const run = await api.startRun(test.id, req);
      nav(`/runs/${run.id}`);
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  };

  return (
    <Modal
      title={
        <>
          Run <span className="mono" style={{ fontSize: 14 }}>{test.name}</span>
        </>
      }
      onClose={onClose}
      width={500}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" onClick={start} disabled={busy || !workersValid} autoFocus>
            <Icon name="play" size={12} /> {busy ? 'Starting…' : 'Start run'}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 14 }}>
        {sum ? (
          <>
            <dl className="dl">
              <dt>Load model</dt>
              <dd>
                {sum.model === 'open' ? 'Open (arrival rate)' : 'Closed (virtual users)'}
              </dd>
              <dt>Duration</dt>
              <dd>
                {fmtSeconds(sum.total)} · {sum.stages.length} stage{sum.stages.length === 1 ? '' : 's'}
              </dd>
              <dt>Peak target</dt>
              <dd>
                {sum.peak} {unit}
                {sum.model === 'open' && sum.maxVus ? <span className="muted"> · max_vus {sum.maxVus}</span> : null}
              </dd>
              <dt>Steps</dt>
              <dd>{sum.steps}</dd>
            </dl>
            {profile && (
              <div className="card" style={{ padding: '6px 4px 0 0' }}>
                <ProfileChart points={profile.points} unit={unit} height={120} />
              </div>
            )}
          </>
        ) : (
          <div className="muted">Scenario summary unavailable.</div>
        )}
        <div className="row" style={{ gap: 14, alignItems: 'flex-start' }}>
          <div className="field" style={{ width: 110, flex: 'none' }}>
            <label htmlFor="workers">Workers</label>
            <input
              id="workers"
              className={`input num ${workersValid ? '' : 'invalid'}`}
              type="number"
              min={1}
              max={64}
              value={workers}
              onChange={(e) => {
                setWorkers(e.target.value);
                if (notEnough) setError(null);
              }}
            />
          </div>
          <div className="grow" style={{ minWidth: 0, paddingTop: 20 }}>
            <WorkerHint loading={fleet.loading && !fleet.data} n={workersValid ? n : 1} idle={idle} connected={connected} />
          </div>
        </div>
        <div className="row" style={{ gap: 14, alignItems: 'flex-start' }}>
          <div className="field" style={{ width: 160, flex: 'none' }}>
            <label htmlFor="run-label">
              Label <span className="label-opt">optional</span>
            </label>
            <input
              id="run-label"
              className="input mono"
              placeholder="e.g. v1.4.2"
              maxLength={100}
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
          </div>
          <div className="field grow">
            <label htmlFor="run-notes">
              Notes <span className="label-opt">optional</span>
            </label>
            <textarea
              id="run-notes"
              className="input"
              rows={2}
              maxLength={10000}
              placeholder="What changed in this build or environment"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
            />
          </div>
        </div>
        {notEnough ? (
          <NotEnoughWorkers error={error as APIError} requested={n} idle={idle} />
        ) : (
          <ErrorAlert error={error} title="Could not start run" />
        )}
      </div>
    </Modal>
  );
}

function WorkerHint({ loading, n, idle, connected }: { loading: boolean; n: number; idle: number; connected: number }) {
  if (loading) return <div className="hint">Checking connected workers…</div>;
  const head = (
    <div style={{ fontWeight: 600, color: 'var(--text)' }}>
      <span className={`dot ${idle > 0 ? 'ok' : ''}`} style={{ marginRight: 6, verticalAlign: 1 }} />
      {idle} idle remote worker{idle === 1 ? '' : 's'} connected
      {connected > idle && <span className="muted" style={{ fontWeight: 400 }}> · {connected - idle} busy</span>}
    </div>
  );
  let body: string;
  if (connected === 0 && n <= 1) {
    body = 'The run executes inside the master (in-process load generator).';
  } else if (n <= idle) {
    body = `The run is split across ${n} of the idle remote workers.`;
  } else {
    const missing = n - idle;
    body =
      `${missing} more worker${missing === 1 ? '' : 's'} needed. If the master has a cloud provisioner, it launches ` +
      `${missing} spot instance${missing === 1 ? '' : 's'} first (can take a few minutes); otherwise the run is rejected.`;
  }
  return (
    <div className="hint" style={{ lineHeight: 1.5 }}>
      {head}
      {body}
    </div>
  );
}

function NotEnoughWorkers({ error, requested, idle }: { error: APIError; requested: number; idle: number }) {
  // details look like "cluster: not enough idle workers: 3 requested, 1 idle"
  const m = error.details.join(' ').match(/(\d+) requested, (\d+) idle/);
  const req = m ? Number(m[1]) : requested;
  const have = m ? Number(m[2]) : idle;
  return (
    <div className="alert warn" role="alert">
      <div className="alert-title">Not enough idle workers</div>
      <div style={{ marginTop: 4, lineHeight: 1.5 }}>
        This run needs <strong>{req}</strong> worker{req === 1 ? '' : 's'} but only <strong>{have}</strong>{' '}
        {have === 1 ? 'is' : 'are'} idle, and the master has no cloud provisioner to launch more. Lower the worker
        count, wait for busy workers to finish, or start more workers.
      </div>
      {error.details.length > 0 && (
        <div className="mono alert-details text-2" style={{ fontSize: 11.5 }}>
          {error.details.join('; ')}
        </div>
      )}
    </div>
  );
}
