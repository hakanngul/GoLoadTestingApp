import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../api/client";
import type { Suite } from "../api/types";
import { SuiteDialog } from "../components/SuiteDialog";
import { ErrorAlert, Icon, Loading, StatusBadge } from "../components/ui";
import { timeAgo } from "../lib/format";
import { useAsync } from "../lib/hooks";
import { describeMode, isActive, progress } from "../lib/suite";

export function SuitesPage() {
  const nav = useNavigate();
  const suites = useAsync(() => api.listSuites(), [], 5000);
  const tests = useAsync(() => api.listTests(), []);
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState("");
  const [actionErr, setActionErr] = useState<unknown>(null);

  const list = suites.data ?? [];
  const noTests = tests.data !== undefined && tests.data.length === 0;

  const run = async (s: Suite) => {
    setBusyId(s.id);
    setActionErr(null);
    try {
      const r = await api.startSuite(s.id);
      nav(`/suite-runs/${r.id}`);
    } catch (e) {
      setActionErr(e);
      setBusyId("");
      suites.reload();
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Test suites</h1>
          <div className="page-sub">
            Groups of tests that run together — one after another or all at once
          </div>
        </div>
        <span className="spacer" />
        <button
          className="btn primary"
          onClick={() => setCreating(true)}
          disabled={noTests || !tests.data}
        >
          <Icon name="plus" size={14} /> New suite
        </button>
      </div>

      {suites.error && !suites.data ? (
        <ErrorAlert error={suites.error} title="Failed to load suites" />
      ) : null}
      <ErrorAlert error={actionErr} title="Could not start the suite" />

      <div className="card">
        {suites.loading && !suites.data ? (
          <Loading />
        ) : list.length === 0 ? (
          <div className="empty">
            <strong>No test suites yet</strong>
            {noTests ? (
              <>
                Create a test first, then group tests into a suite.{" "}
                <Link to="/">Tests</Link>
              </>
            ) : (
              <>
                Group test cases (e.g. login, search, checkout) and run them
                with one click.{" "}
                <button className="linklike" onClick={() => setCreating(true)}>
                  New suite
                </button>
              </>
            )}
          </div>
        ) : (
          <div className="table-wrap">
            <table className="t">
              <thead>
                <tr>
                  <th>Suite</th>
                  <th>Test cases</th>
                  <th>Mode</th>
                  <th>Last run</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((s) => {
                  const last = s.last_run;
                  const active = isActive(last);
                  const p = last ? progress(last) : undefined;
                  return (
                    <tr
                      key={s.id}
                      style={{ cursor: "pointer" }}
                      onClick={() => nav(`/suites/${s.id}`)}
                    >
                      <td className="name-cell">
                        <Link
                          to={`/suites/${s.id}`}
                          onClick={(e) => e.stopPropagation()}
                        >
                          {s.name}
                        </Link>
                        {s.description && (
                          <div className="sub muted">{s.description}</div>
                        )}
                      </td>
                      <td className="text-2">
                        {s.items.length}
                        <div className="sub muted suite-names">
                          {s.items
                            .slice(0, 4)
                            .map((it) => it.test_name || "deleted test")
                            .join(" → ")}
                          {s.items.length > 4 ? " …" : ""}
                        </div>
                      </td>
                      <td className="text-2">{describeMode(s)}</td>
                      <td>
                        {last && p ? (
                          <Link
                            to={`/suite-runs/${last.id}`}
                            onClick={(e) => e.stopPropagation()}
                            className="row"
                            style={{ gap: 6 }}
                          >
                            <StatusBadge status={last.status} />
                            <span className="text-2 small-text">
                              {active
                                ? `${p.done}/${p.total}`
                                : `${p.passed}/${p.total} passed`}{" "}
                              · {timeAgo(last.started_at)}
                            </span>
                          </Link>
                        ) : (
                          <span className="muted">never run</span>
                        )}
                      </td>
                      <td
                        className="actions"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {active ? (
                          <Link
                            className="btn sm"
                            to={`/suite-runs/${last!.id}`}
                          >
                            View live
                          </Link>
                        ) : (
                          <button
                            className="btn sm"
                            onClick={() => run(s)}
                            disabled={busyId === s.id}
                            title="Run every test of this suite"
                          >
                            <Icon name="play" size={11} /> Run
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {creating && (
        <SuiteDialog
          tests={tests.data ?? []}
          onClose={() => setCreating(false)}
          onSaved={(s) => {
            setCreating(false);
            nav(`/suites/${s.id}`);
          }}
        />
      )}
    </>
  );
}
