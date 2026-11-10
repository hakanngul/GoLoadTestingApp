-- 0001_init: tests, attached files, runs and their per-second metrics.
-- Only per-second aggregates are stored (never per-request rows).

CREATE TABLE tests (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL UNIQUE,
    yaml       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX tests_updated_at_idx ON tests (updated_at DESC, id DESC);

CREATE TABLE test_files (
    test_id uuid   NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    name    text   NOT NULL,
    content bytea  NOT NULL,
    size    bigint NOT NULL,
    PRIMARY KEY (test_id, name)
);

CREATE TABLE runs (
    id          uuid             PRIMARY KEY,
    test_id     uuid             NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    test_name   text             NOT NULL,
    status      text             NOT NULL,
    yaml        text             NOT NULL DEFAULT '',
    workers     integer          NOT NULL DEFAULT 0,
    started_at  timestamptz      NOT NULL,
    finished_at timestamptz,
    summary     jsonb,
    thresholds  jsonb            NOT NULL DEFAULT '[]',
    peak_cpu    double precision NOT NULL DEFAULT 0,
    warnings    jsonb            NOT NULL DEFAULT '[]',
    error       text             NOT NULL DEFAULT ''
);
CREATE INDEX runs_test_started_idx ON runs (test_id, started_at DESC, id DESC);
CREATE INDEX runs_started_idx ON runs (started_at DESC, id DESC);
-- MarkInterrupted runs on every master start; keep it cheap.
CREATE INDEX runs_active_idx ON runs (status) WHERE status IN ('queued', 'running');

CREATE TABLE run_metrics (
    run_id   uuid        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    t        timestamptz NOT NULL,
    point    jsonb       NOT NULL,
    snapshot bytea,
    PRIMARY KEY (run_id, t)
);

CREATE TABLE run_errors (
    id      bigserial   PRIMARY KEY,
    run_id  uuid        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    t       timestamptz NOT NULL,
    step    text        NOT NULL DEFAULT '',
    status  integer     NOT NULL DEFAULT 0,
    message text        NOT NULL DEFAULT ''
);
CREATE INDEX run_errors_run_idx ON run_errors (run_id, t, id);
