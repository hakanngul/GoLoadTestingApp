-- 0005_suites (SQLite): test suites and their runs. Mirrors
-- migrations/0005_suites.sql. Items are JSON arrays (model.SuiteItem,
-- model.SuiteRunItem).

CREATE TABLE suites (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    description     TEXT    NOT NULL DEFAULT '',
    mode            TEXT    NOT NULL DEFAULT 'sequential',
    stop_on_failure INTEGER NOT NULL DEFAULT 0,
    gap_ms          INTEGER NOT NULL DEFAULT 0,
    items           TEXT    NOT NULL DEFAULT '[]',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE suite_runs (
    id              TEXT    PRIMARY KEY,
    suite_id        TEXT    NOT NULL REFERENCES suites (id) ON DELETE CASCADE,
    suite_name      TEXT    NOT NULL,
    status          TEXT    NOT NULL,
    mode            TEXT    NOT NULL,
    stop_on_failure INTEGER NOT NULL DEFAULT 0,
    gap_ms          INTEGER NOT NULL DEFAULT 0,
    label           TEXT    NOT NULL DEFAULT '',
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NULL,
    items           TEXT    NOT NULL DEFAULT '[]',
    error           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX suite_runs_suite_idx ON suite_runs (suite_id, started_at);
CREATE INDEX suite_runs_started_idx ON suite_runs (started_at);
