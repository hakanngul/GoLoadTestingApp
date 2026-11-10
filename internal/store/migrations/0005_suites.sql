-- 0005_suites: test suites and their runs (see model.Suite and
-- model.SuiteRun). Items are JSON arrays; suite items reference tests by
-- ID without a foreign key (a deleted test stays listed and is skipped).

CREATE TABLE suites (
    id              uuid        PRIMARY KEY,
    name            text        NOT NULL UNIQUE,
    description     text        NOT NULL DEFAULT '',
    mode            text        NOT NULL DEFAULT 'sequential',
    stop_on_failure boolean     NOT NULL DEFAULT false,
    gap_ms          bigint      NOT NULL DEFAULT 0,
    items           jsonb       NOT NULL DEFAULT '[]',
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);

CREATE TABLE suite_runs (
    id              uuid        PRIMARY KEY,
    suite_id        uuid        NOT NULL REFERENCES suites (id) ON DELETE CASCADE,
    suite_name      text        NOT NULL,
    status          text        NOT NULL,
    mode            text        NOT NULL,
    stop_on_failure boolean     NOT NULL DEFAULT false,
    gap_ms          bigint      NOT NULL DEFAULT 0,
    label           text        NOT NULL DEFAULT '',
    started_at      timestamptz NOT NULL,
    finished_at     timestamptz NULL,
    items           jsonb       NOT NULL DEFAULT '[]',
    error           text        NOT NULL DEFAULT ''
);
CREATE INDEX suite_runs_suite_idx ON suite_runs (suite_id, started_at);
CREATE INDEX suite_runs_started_idx ON suite_runs (started_at);
