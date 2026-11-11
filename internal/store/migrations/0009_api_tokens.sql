-- 0009_api_tokens: scoped API tokens (model.APIToken) and "who started
-- this run" auditing (runs.started_by, suite_runs.started_by). Only the
-- sha256 of a token secret is stored.

CREATE TABLE api_tokens (
    id           uuid        PRIMARY KEY,
    name         text        NOT NULL UNIQUE,
    scope        text        NOT NULL,
    prefix       text        NOT NULL,
    hash         text        NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL,
    last_used_at timestamptz NULL,
    expires_at   timestamptz NULL
);

ALTER TABLE runs       ADD COLUMN started_by text NOT NULL DEFAULT '';
ALTER TABLE suite_runs ADD COLUMN started_by text NOT NULL DEFAULT '';
