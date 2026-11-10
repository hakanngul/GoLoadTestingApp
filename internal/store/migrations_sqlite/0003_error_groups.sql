-- 0003_error_groups (SQLite): final error groups of a run (JSON array of
-- model.ErrorGroup), written once when the run ends. NULL = none stored.
-- Mirrors migrations/0003_error_groups.sql.

ALTER TABLE runs ADD COLUMN error_groups TEXT NULL;
