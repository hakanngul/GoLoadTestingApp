import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import { ErrorAlert, Icon, Kpi, Loading, StatusBadge } from "../components/ui";
import {
  fmtDateTime,
  fmtMs,
  fmtPct,
  fmtRps,
  fmtSeconds,
  runDurationS,
} from "../lib/format";
import { useAsync, useNow } from "../lib/hooks";
import {
  describeMode,
  isActive,
  itemDurationS,
  itemTone,
  progress,
} from "../lib/suite";

/** One suite run: live progress of each test case and its key numbers. */
export function SuiteRunPage() {
  const { id = "" } = useParams();
  const [poll, setPoll] = useState(2000);
  const run = useAsync(
    () =>
      api.getSuiteRun(id).then((r) => {
        if (!isActive(r)) setPoll(0);
        return r;
      }),
    [id],
    poll || undefined,
  );
  const now = useNow(1000);
  const [stopping, setStopping] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  if (run.error && !run.data)
    return (
      <ErrorAlert error={run.error} title="Failed to load the suite run" />
    );
  if (!run.data) return <Loading />;
  const r = run.data;
  const active = isActive(r);
  const p = progress(r);

  const stop = async () => {
    setStopping(true);
    setErr(null);
    try {
      await api.stopSuiteRun(r.id);
      run.reload();
    } catch (e) {
      setErr(e);
    } finally {
      setStopping(false);
    }
  };

  const elapsed = r.finished_at
    ? runDurationS(r.started_at, r.finished_at)
    : Math.max(0, (now - Date.parse(r.started_at)) / 1000);

  return (
    <>
      <div className="page-head">
        <div>
          <div className="breadcrumb">
            <Link to="/suites">Test suites</Link> /{" "}
            <Link to={`/suites/${r.suite_id}`}>{r.suite_name}</Link> /
          </div>
          <h1 className="row" style={{ gap: 10 }}>
            Suite run <StatusBadge status={r.status} />
          </h1>
          <div className="page-sub">
            {fmtDateTime(r.started_at)} · {describeMode(r)}
            {r.label ? ` · ${r.label}` : ""}
          </div>
        </div>
        <span className="spacer" />
        {active && (
          <button className="btn danger" onClick={stop} disabled={stopping}>
            <Icon name="stop" size={11} />{" "}
            {stopping ? "Stopping…" : "Stop suite"}
          </button>
        )}
      </div>

      <ErrorAlert error={err} title="Could not stop the suite" />
      {r.error && <div className="alert bad">{r.error}</div>}

      <div className="kpis">
        <Kpi
          label="Progress"
          value={`${p.done}/${p.total}`}
          sub={active ? "tests finished" : "tests"}
        />
        <Kpi label="Passed" value={p.passed} />
        <Kpi
          label="Failed"
          value={p.failed}
          tone={p.failed ? "bad" : undefined}
        />
        <Kpi
          label="Skipped"
          value={p.skipped}
          tone={p.skipped ? "warn" : undefined}
        />
        <Kpi label="Duration" value={fmtSeconds(elapsed)} />
      </div>

      <div className="card" style={{ marginTop: 16 }}>
        <div className="table-wrap">
          <table className="t">
            <thead>
              <tr>
                <th>#</th>
                <th>Test</th>
                <th>Status</th>
                <th className="num">Duration</th>
                <th className="num">Requests</th>
                <th className="num">RPS</th>
                <th className="num">p95</th>
                <th className="num">Errors</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {r.items.map((it, i) => {
                const d = itemDurationS(it, now);
                const sum = it.summary;
                return (
                  <tr key={i}>
                    <td className="muted">{i + 1}</td>
                    <td className="name-cell">
                      {it.test_name || (
                        <span className="muted">deleted test</span>
                      )}
                      {it.error && <div className="sub bad">{it.error}</div>}
                    </td>
                    <td>
                      <span className={`badge ${itemTone(it.status)}`}>
                        {it.status}
                      </span>
                    </td>
                    <td className="num">
                      {d === undefined ? "—" : fmtSeconds(d)}
                    </td>
                    <td className="num">
                      {sum ? sum.requests.toLocaleString() : "—"}
                    </td>
                    <td className="num">{sum ? fmtRps(sum.rps) : "—"}</td>
                    <td className="num">{sum ? fmtMs(sum.p95_ms) : "—"}</td>
                    <td
                      className={`num ${sum && sum.error_rate > 0 ? "bad" : ""}`}
                    >
                      {sum ? fmtPct(sum.error_rate) : "—"}
                    </td>
                    <td className="actions">
                      {it.run_id && (
                        <Link className="btn sm" to={`/runs/${it.run_id}`}>
                          {it.status === "running" || it.status === "queued"
                            ? "Live"
                            : "Report"}
                        </Link>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
      {active && (
        <p className="hint" style={{ marginTop: 10 }}>
          Updates every 2 seconds. Open a test&apos;s live view for per-second
          charts.
        </p>
      )}
    </>
  );
}
