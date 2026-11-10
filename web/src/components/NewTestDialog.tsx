import { useNavigate } from 'react-router-dom';
import { Modal } from './ui';

/** "New test" chooser: Quick (single endpoint) or Advanced (full scenario). */
export function NewTestDialog({ onClose }: { onClose: () => void }) {
  const nav = useNavigate();
  const go = (mode: 'quick' | 'advanced') => nav(`/tests/new?mode=${mode}`);
  return (
    <Modal title="Create a test" onClose={onClose} width={600}>
      <div className="mode-choices">
        <button className="mode-choice recommended" onClick={() => go('quick')} autoFocus>
          <span className="mode-choice-icon" aria-hidden="true">
            <svg width="22" height="22" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
              <path d="M9 1.5L3.5 9H8l-1 5.5L12.5 7H8z" />
            </svg>
          </span>
          <span className="mode-choice-text">
            <span className="row" style={{ gap: 8 }}>
              <strong>Quick test</strong>
              <span className="badge info plain">Recommended</span>
            </span>
            <span className="text-2">Load test a single endpoint in one screen.</span>
            <ul>
              <li>Paste a URL, pick users or requests per second</li>
              <li>Ramp-up, hold and pass criteria with sensible defaults</li>
              <li>Optional bearer token or login request</li>
            </ul>
          </span>
        </button>
        <button className="mode-choice" onClick={() => go('advanced')}>
          <span className="mode-choice-icon" aria-hidden="true">
            <svg width="22" height="22" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
              <path d="M2.5 4h11M2.5 8h11M2.5 12h11" />
              <circle cx="5" cy="4" r="1.3" fill="var(--surface)" />
              <circle cx="10.5" cy="8" r="1.3" fill="var(--surface)" />
              <circle cx="7" cy="12" r="1.3" fill="var(--surface)" />
            </svg>
          </span>
          <span className="mode-choice-text">
            <strong>Advanced</strong>
            <span className="text-2">The full scenario editor and YAML.</span>
            <ul>
              <li>Multi-step flows with extracted variables</li>
              <li>CSV data files, WebSocket steps, custom stages</li>
              <li>Every auth option and threshold</li>
            </ul>
          </span>
        </button>
      </div>
      <div className="hint" style={{ marginTop: 12 }}>
        You can switch a test from Quick to Advanced at any time; both edit the same YAML.
      </div>
    </Modal>
  );
}
