-- 0004_schedules (SQLite): planned runs of a test and the schedule that
-- started a run. Mirrors migrations/0004_schedules.sql.

CREATE TABLE schedules (
    id               TEXT    PRIMARY KEY,
    test_id          TEXT    NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
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
CREATE INDEX schedules_test_idx ON schedules (test_id);
CREATE INDEX schedules_created_idx ON schedules (created_at, id);

ALTER TABLE runs ADD COLUMN schedule_id TEXT NOT NULL DEFAULT '';
