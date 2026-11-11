-- 0007_notifications: destinations notified when runs and suite runs finish
-- (see model.NotificationChannel). url is a secret; events is a JSON array.

CREATE TABLE notification_channels (
    id            uuid        PRIMARY KEY,
    name          text        NOT NULL,
    kind          text        NOT NULL,
    url           text        NOT NULL,
    events        text        NOT NULL DEFAULT '[]',
    only_failures boolean     NOT NULL DEFAULT false,
    enabled       boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);
CREATE INDEX notification_channels_created_idx ON notification_channels (created_at, id);
