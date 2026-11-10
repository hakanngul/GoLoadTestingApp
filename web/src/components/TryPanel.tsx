import { useEffect, useState, type ReactNode } from 'react';
import type { TryFix, TryHeader, TryHint, TryResult, TryStep } from '../api/types';
import { fmtBytes } from '../lib/format';
import { fmtMs, headerValue, prettyBody, redirectLines, sortHints, stepBadge, summary } from '../lib/trial';
import { ErrorAlert, Icon } from './ui';

/**
 * Drawer showing the result of a "Test request" (one iteration of the
 * scenario): hints with one-click fixes at the top, then one expandable row
 * per step.
 */
export function TryPanel({
  result,
  loading,
  error,
  stale,
  onClose,
  onRerun,
  onApply,
}: {
  result: TryResult | null;
  loading: boolean;
  error: unknown;
  /** The scenario changed since this result was produced. */
  stale: boolean;
  onClose: () => void;
  onRerun: () => void;
  /** Applies a fix to the editor; returns false when it no longer applies. */
  onApply: (fix: TryFix) => boolean;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div className="drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="drawer" role="dialog" aria-modal="true" aria-labelledby="try-title">
        <div className="drawer-head">
          <div>
            <h2 id="try-title">Test request</h2>
            <div className="hint">One iteration: login (if any) and each step once, exactly as a run sends them.</div>
          </div>
          <span className="grow" />
          <button className="btn sm" onClick={onRerun} disabled={loading}>
            <Icon name="refresh" size={13} /> {loading ? 'Sending…' : 'Send again'}
          </button>
          <button className="btn ghost icon sm" onClick={onClose} aria-label="Close">
            <Icon name="x" />
          </button>
        </div>
        <div className="drawer-body">
          {loading && !result ? (
            <div className="try-loading">
              <span className="spinner lg" />
              <span>Sending one iteration…</span>
            </div>
          ) : error ? (
            <ErrorAlert error={error} title="Test request failed" />
          ) : result ? (
            <TryResultView result={result} stale={stale} loading={loading} onApply={onApply} />
          ) : null}
        </div>
      </aside>
    </div>
  );
}

function TryResultView({
  result,
  stale,
  loading,
  onApply,
}: {
  result: TryResult;
  stale: boolean;
  loading: boolean;
  onApply: (fix: TryFix) => boolean;
}) {
  const sum = summary(result);
  const firstFailed = result.steps.findIndex((s) => s.state === 'failed');
  const [open, setOpen] = useState<Set<number>>(() => new Set(firstFailed >= 0 ? [firstFailed] : []));
  const [applied, setApplied] = useState<Set<number>>(new Set());
  const hints = sortHints(result.hints);
  const dataKeys = Object.keys(result.data ?? {}).sort((a, b) => a.localeCompare(b));

  const toggle = (i: number) =>
    setOpen((o) => {
      const n = new Set(o);
      if (n.has(i)) n.delete(i);
      else n.add(i);
      return n;
    });

  return (
    <div className={`stack ${loading ? 'try-refreshing' : ''}`} style={{ gap: 14 }}>
      <div className={`try-summary ${sum.tone}`}>
        <span className="try-summary-icon">
          <Icon name={sum.tone === 'good' ? 'check' : 'alert'} size={16} />
        </span>
        <strong>{sum.text}</strong>
        <span className="grow" />
        <span className="muted num">{fmtMs(result.duration_ms)} total</span>
      </div>
      {stale && (
        <div className="hint try-stale">
          <Icon name="alert" size={12} /> The scenario changed since this result — send again to re-check.
        </div>
      )}
      {dataKeys.length > 0 && (
        <div className="try-data">
          <span className="label" style={{ margin: 0 }}>
            CSV row 1
          </span>
          {dataKeys.map((k) => (
            <span className="try-var mono" key={k}>
              {k}={result.data[k]}
            </span>
          ))}
        </div>
      )}

      {hints.length > 0 && (
        <section className="stack" style={{ gap: 6 }} aria-label="Hints">
          {hints.map((h, i) => (
            <HintRow
              key={i}
              hint={h}
              applied={applied.has(i)}
              onApply={(fix) => {
                if (onApply(fix)) setApplied((a) => new Set(a).add(i));
              }}
            />
          ))}
        </section>
      )}

      <section className="try-steps" aria-label="Steps">
        {result.steps.map((s, i) => (
          <StepRow key={`${s.name}-${i}`} n={i + 1} step={s} open={open.has(i)} onToggle={() => toggle(i)} />
        ))}
      </section>
    </div>
  );
}

function HintRow({ hint, applied, onApply }: { hint: TryHint; applied: boolean; onApply: (f: TryFix) => void }) {
  const icon = hint.level === 'info' ? 'flag' : 'alert';
  return (
    <div className={`try-hint ${hint.level}`}>
      <span className="try-hint-icon">
        <Icon name={icon} size={14} />
      </span>
      <div className="try-hint-text">
        {hint.step && <span className="try-hint-step">{hint.step}</span>}
        <span>{hint.message}</span>
        {hint.fix && (
          <div className="try-fix">
            <code className="mono">{hint.fix.value}</code>
            <button className="btn sm primary" disabled={applied} onClick={() => onApply(hint.fix!)}>
              {applied ? (
                <>
                  <Icon name="check" size={12} /> Applied
                </>
              ) : (
                'Apply'
              )}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

function StepRow({ n, step, open, onToggle }: { n: number; step: TryStep; open: boolean; onToggle: () => void }) {
  const b = stepBadge(step);
  const notRun = step.state === 'not_run';
  return (
    <div className={`try-step ${step.state} ${open ? 'open' : ''}`}>
      <button className="try-step-row" onClick={onToggle} aria-expanded={open} disabled={notRun && !step.url}>
        <span className="try-step-n num">{n}</span>
        <span className={`badge plain ${b.tone}`}>{b.label}</span>
        <span className="try-step-name">{step.name}</span>
        <span className="try-step-url mono" title={step.url}>
          {step.kind === 'ws' ? 'WS' : step.method} {step.url}
        </span>
        <span className="muted num try-step-time">{notRun ? '' : fmtMs(step.duration_ms)}</span>
        <span className={`try-chev ${open ? 'on' : ''}`}>
          <Icon name="chevron" size={14} />
        </span>
      </button>
      {open && <StepDetails step={step} />}
    </div>
  );
}

function StepDetails({ step }: { step: TryStep }) {
  if (step.state === 'not_run') {
    return <div className="try-details hint">Not run: an earlier step failed or the trial stopped.</div>;
  }
  const ct = headerValue(step.response_headers, 'content-type');
  const hops = redirectLines(step);
  const cookiesSent = step.cookies_sent ?? [];
  const cookiesSet = step.cookies_set ?? [];
  return (
    <div className="try-details">
      {step.error && <div className="alert bad mono try-error">{step.error}</div>}

      {step.assertions.length > 0 && (
        <Block title="Assertions">
          <ul className="try-checks">
            {step.assertions.map((a, i) => (
              <li key={i} className={a.passed ? 'good' : 'bad'}>
                <Icon name={a.passed ? 'check' : 'x'} size={12} />
                <span className="mono">{a.assertion}</span>
                {a.message && <span className="muted"> — {a.message}</span>}
              </li>
            ))}
          </ul>
        </Block>
      )}

      {step.extracted.length > 0 && (
        <Block title="Extracted variables">
          <table className="try-kv">
            <tbody>
              {step.extracted.map((x, i) => (
                <tr key={i}>
                  <td className="mono">{`{{${x.var}}}`}</td>
                  <td className={x.ok ? 'mono' : 'bad'}>{x.ok ? x.value || <span className="muted">(empty)</span> : x.error}</td>
                  <td className="muted mono">
                    {x.source} {x.expr}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Block>
      )}

      {step.messages && step.messages.length > 0 && (
        <Block title="Messages">
          <ul className="try-checks">
            {step.messages.map((m, i) => (
              <li key={i} className={m.error ? 'bad' : 'good'}>
                <Icon name={m.error ? 'x' : 'check'} size={12} />
                <span className="mono">{m.name}</span>
                <span className="muted"> {fmtMs(m.duration_ms)}</span>
                {m.error && <span className="muted"> — {m.error}</span>}
              </li>
            ))}
          </ul>
        </Block>
      )}

      {hops.length > 0 && (
        <Block
          title={
            <>
              Redirects followed <span className="muted num">· {hops.length}</span>
            </>
          }
        >
          <ul className="try-checks">
            {hops.map((h, i) => (
              <li key={i}>
                <span className="badge plain warn">{h.status}</span>
                <span className="mono">{h.text}</span>
                {h.cookies.length > 0 && <span className="muted"> — sets cookie {h.cookies.join(', ')}</span>}
              </li>
            ))}
          </ul>
          {step.final_url && (
            <div className="hint">
              The response below is from <span className="mono">{step.final_url}</span>; the step's time covers the whole
              chain.
            </div>
          )}
        </Block>
      )}

      <Block title="Request">
        {step.url && (
          <div className="mono try-url">
            <strong>{step.kind === 'ws' ? 'WS' : step.method}</strong> {step.url}
          </div>
        )}
        {cookiesSent.length > 0 && (
          <div className="hint">
            Cookies sent{hops.length > 0 ? ' with the final request' : ''} (names only):{' '}
            <span className="mono">{cookiesSent.join(', ')}</span>
          </div>
        )}
        <Headers h={step.request_headers} />
        {step.request_body && <pre className="try-body">{prettyBody(step.request_body)}</pre>}
      </Block>

      {step.status > 0 && (
        <Block
          title={
            <>
              Response <span className="muted num">· {step.status} · {fmtBytes(step.bytes)}</span>
            </>
          }
        >
          {cookiesSet.length > 0 && (
            <div className="hint">
              Cookies stored for this virtual user (names only): <span className="mono">{cookiesSet.join(', ')}</span>
            </div>
          )}
          <Headers h={step.response_headers} />
          {step.body_binary ? (
            <div className="hint">Binary body ({fmtBytes(step.bytes)}) — not shown.</div>
          ) : step.body ? (
            <>
              <pre className="try-body">{prettyBody(step.body, ct)}</pre>
              {step.body_truncated && <div className="hint">Showing the start of the body only.</div>}
            </>
          ) : (
            <div className="hint">Empty body.</div>
          )}
        </Block>
      )}
    </div>
  );
}

function Block({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <div className="try-block">
      <div className="try-block-title">{title}</div>
      {children}
    </div>
  );
}

function Headers({ h }: { h: TryHeader[] }) {
  if (h.length === 0) return null;
  return (
    <table className="try-kv">
      <tbody>
        {h.map((x, i) => (
          <tr key={i}>
            <td className="mono muted">{x.name}</td>
            <td className="mono">{x.value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
