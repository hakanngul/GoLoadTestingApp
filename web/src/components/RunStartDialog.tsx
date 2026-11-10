import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api/client';
import type { Test } from '../api/types';
import { fmtSeconds } from '../lib/format';
import { stagesToProfile, summarizeYaml, unitFor } from '../lib/scenario';
import { ProfileChart } from './charts';
import { ErrorAlert, Icon, Modal } from './ui';

/** Dialog that starts a run of a saved test and navigates to its live view. */
export function RunStartDialog({ test, onClose }: { test: Test; onClose: () => void }) {
  const nav = useNavigate();
  const [workers, setWorkers] = useState('1');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [full, setFull] = useState<Test>(test);

  // The list endpoint may omit YAML in the future; make sure we have it.
  useEffect(() => {
    if (!test.yaml) {
      api.getTest(test.id).then(setFull).catch(setError);
    }
  }, [test]);

  const sum = summarizeYaml(full.yaml);
  const profile = sum ? stagesToProfile(sum.stages) : null;
  const unit = sum ? unitFor(sum.model) : 'rps';
  const n = Number(workers);
  const workersValid = Number.isInteger(n) && n >= 1 && n <= 64;

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const run = await api.startRun(test.id, { workers: n });
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
        <div className="field" style={{ maxWidth: 200 }}>
          <label htmlFor="workers">Workers</label>
          <input
            id="workers"
            className={`input num ${workersValid ? '' : 'invalid'}`}
            type="number"
            min={1}
            max={64}
            value={workers}
            onChange={(e) => setWorkers(e.target.value)}
          />
        </div>
        <div className="hint" style={{ marginTop: -8 }}>
          1 = local load generator. Distributed runs across several workers arrive in Phase 4.
        </div>
        <ErrorAlert error={error} title="Could not start run" />
      </div>
    </Modal>
  );
}
