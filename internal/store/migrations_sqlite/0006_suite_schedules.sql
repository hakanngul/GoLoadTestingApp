-- 0006_suite_schedules (SQLite): a schedule may start a test suite
-- instead of a single test. Mirrors migrations/0006_suite_schedules.sql.
-- SQLite cannot drop NOT NULL, so the table is rebuilt: nothing
-- references schedules (runs.schedule_id has no foreign key), so dropping
-- the old table is safe with foreign keys on.

CREATE TABLE schedules_new (
    id               TEXT    PRIMARY KEY,
    test_id          TEXT    NULL REFERENCES tests (id) ON DELETE CASCADE,
    suite_id         TEXT    NULL REFERENCES suites (id) ON DELETE CASCADE,
    name             TEXT    NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    kind             TEXT    NOT NULL,
    run_at_local     TEXT    NOT NULL DEFAULT '',
    cron             TEXT    NOT NULL DEFAULT '',
    timezone         TEXT    NOT NULL DEFAULT 'UTC',
    load_override    TEXT    NULL,
    workers          INTEGER NOT NULL DEFAULT 0,
    label            TEXT    NOT NULL DEFAULT '',
    notes            TEXT    NOT NULL DEFAULT '',
    overlap          TEXT    NOT NULL DEFAULT 'skip',
    queue_timeout_ms INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    next_run_at      INTEGER NULL,
    last_run_at      INTEGER NULL,
    last_run_id      TEXT    NOT NULL DEFAULT '',
    last_status      TEXT    NOT NULL DEFAULT '',
    last_message     TEXT    NOT NULL DEFAULT ''
);

INSERT INTO schedules_new (id, test_id, name, enabled, kind, run_at_local, cron, timezone,
    load_override, workers, label, notes, overlap, queue_timeout_ms, created_at, updated_at,
    next_run_at, last_run_at, last_run_id, last_status, last_message)
SELECT id, test_id, name, enabled, kind, run_at_local, cron, timezone,
    load_override, workers, label, notes, overlap, queue_timeout_ms, created_at, updated_at,
    next_run_at, last_run_at, last_run_id, last_status, last_message
FROM schedules;

DROP TABLE schedules;
ALTER TABLE schedules_new RENAME TO schedules;
CREATE INDEX schedules_test_idx ON schedules (test_id);
CREATE INDEX schedules_suite_idx ON schedules (suite_id);
CREATE INDEX schedules_created_idx ON schedules (created_at, id);
