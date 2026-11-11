-- 0007_notifications (SQLite): destinations notified when runs and suite
-- runs finish. Mirrors migrations/0007_notifications.sql.

CREATE TABLE notification_channels (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL,
    kind          TEXT    NOT NULL,
    url           TEXT    NOT NULL,
    events        TEXT    NOT NULL DEFAULT '[]',
    only_failures INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX notification_channels_created_idx ON notification_channels (created_at, id);
