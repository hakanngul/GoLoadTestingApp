-- 0008_run_pin: a user-pinned run is never removed by retention pruning
-- (see model.Run.Pinned and store.Pruner).

ALTER TABLE runs ADD COLUMN pinned boolean NOT NULL DEFAULT false;
