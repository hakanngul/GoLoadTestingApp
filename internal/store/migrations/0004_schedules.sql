-- 0004_schedules: planned runs of a test (see model.Schedule) and the
-- schedule that started a run. runs.schedule_id has no foreign key: a run
-- keeps the reference after its schedule is deleted.

CREATE TABLE schedules (
    id               uuid        PRIMARY KEY,
    test_id          uuid        NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    name             text        NOT NULL,
    enabled          boolean     NOT NULL DEFAULT true,
    kind             text        NOT NULL,
    run_at_local     text        NOT NULL DEFAULT '',
    cron             text        NOT NULL DEFAULT '',
    timezone         text        NOT NULL DEFAULT 'UTC',
    load_override    jsonb       NULL,
    workers          integer     NOT NULL DEFAULT 0,
    label            text        NOT NULL DEFAULT '',
    notes            text        NOT NULL DEFAULT '',
    overlap          text        NOT NULL DEFAULT 'skip',
    queue_timeout_ms bigint      NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    next_run_at      timestamptz NULL,
    last_run_at      timestamptz NULL,
    last_run_id      text        NOT NULL DEFAULT '',
    last_status      text        NOT NULL DEFAULT '',
    last_message     text        NOT NULL DEFAULT ''
);
CREATE INDEX schedules_test_idx ON schedules (test_id);
CREATE INDEX schedules_created_idx ON schedules (created_at, id);

ALTER TABLE runs ADD COLUMN schedule_id text NOT NULL DEFAULT '';
