-- 0002_reporting (SQLite): run metadata (label, notes) and a per-test
-- baseline run. Mirrors migrations/0002_reporting.sql.

ALTER TABLE runs ADD COLUMN label TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN notes TEXT NOT NULL DEFAULT '';

-- The reference run new runs of a test are compared against. Deleting that
-- run clears it.
ALTER TABLE tests ADD COLUMN baseline_run_id TEXT NULL REFERENCES runs (id) ON DELETE SET NULL;
-- Lets the ON DELETE SET NULL above find referencing tests without a scan.
CREATE INDEX tests_baseline_run_idx ON tests (baseline_run_id);
