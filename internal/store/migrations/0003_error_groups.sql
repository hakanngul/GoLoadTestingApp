-- 0003_error_groups: final error groups of a run (every failed request
-- grouped by step, status and message; see model.ErrorGroup), written once
-- when the run ends. NULL = none stored.

ALTER TABLE runs ADD COLUMN error_groups jsonb NULL;
