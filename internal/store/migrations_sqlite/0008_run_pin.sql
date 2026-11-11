-- 0008_run_pin (SQLite): user-pinned runs are never removed by retention.
-- Mirrors migrations/0008_run_pin.sql.

ALTER TABLE runs ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
