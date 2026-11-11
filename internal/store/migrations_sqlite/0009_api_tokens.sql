-- 0009_api_tokens (SQLite): mirrors migrations/0009_api_tokens.sql.
-- Timestamps are INTEGER microseconds since the Unix epoch.

CREATE TABLE api_tokens (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    scope        TEXT    NOT NULL,
    prefix       TEXT    NOT NULL,
    hash         TEXT    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NULL,
    expires_at   INTEGER NULL
);

ALTER TABLE runs       ADD COLUMN started_by TEXT NOT NULL DEFAULT '';
ALTER TABLE suite_runs ADD COLUMN started_by TEXT NOT NULL DEFAULT '';
