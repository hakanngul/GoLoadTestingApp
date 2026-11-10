-- 0001_init (SQLite): tests, attached files, runs and their per-second
-- metrics. Mirrors migrations/0001_init.sql for PostgreSQL:
--   uuid        -> TEXT (canonical lowercase form, as the store hands out)
--   timestamptz -> INTEGER microseconds since the Unix epoch, UTC
--                  (the precision PostgreSQL keeps; sorts numerically)
--   jsonb       -> TEXT holding JSON
--   bytea       -> BLOB
-- Foreign keys need PRAGMA foreign_keys=ON, which the store sets on every
-- connection.

CREATE TABLE tests (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    yaml       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX tests_updated_at_idx ON tests (updated_at DESC, id DESC);

CREATE TABLE test_files (
    test_id TEXT    NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    name    TEXT    NOT NULL,
    content BLOB    NOT NULL,
    size    INTEGER NOT NULL,
    PRIMARY KEY (test_id, name)
);

CREATE TABLE runs (
    id          TEXT    PRIMARY KEY,
    test_id     TEXT    NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    test_name   TEXT    NOT NULL,
    status      TEXT    NOT NULL,
    yaml        TEXT    NOT NULL DEFAULT '',
    workers     INTEGER NOT NULL DEFAULT 0,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    summary     TEXT,
    thresholds  TEXT    NOT NULL DEFAULT '[]',
    peak_cpu    REAL    NOT NULL DEFAULT 0,
    warnings    TEXT    NOT NULL DEFAULT '[]',
    error       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX runs_test_started_idx ON runs (test_id, started_at DESC, id DESC);
CREATE INDEX runs_started_idx ON runs (started_at DESC, id DESC);
-- MarkInterrupted runs on every master start; keep it cheap.
CREATE INDEX runs_active_idx ON runs (status) WHERE status IN ('queued', 'running');

CREATE TABLE run_metrics (
    run_id   TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    t        INTEGER NOT NULL,
    point    TEXT    NOT NULL,
    snapshot BLOB,
    PRIMARY KEY (run_id, t)
);

CREATE TABLE run_errors (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    t       INTEGER NOT NULL,
    step    TEXT    NOT NULL DEFAULT '',
    status  INTEGER NOT NULL DEFAULT 0,
    message TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX run_errors_run_idx ON run_errors (run_id, t, id);
