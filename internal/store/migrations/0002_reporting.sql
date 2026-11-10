-- 0002_reporting: run metadata (label, notes) and a per-test baseline run.

ALTER TABLE runs ADD COLUMN label text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN notes text NOT NULL DEFAULT '';

-- The reference run new runs of a test are compared against. Deleting that
-- run clears it.
ALTER TABLE tests ADD COLUMN baseline_run_id uuid NULL REFERENCES runs (id) ON DELETE SET NULL;
