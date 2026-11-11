-- 0006_suite_schedules: a schedule may start a test suite instead of a
-- single test (model.ScheduleSpec.SuiteID). Exactly one of test_id and
-- suite_id is set (enforced by the scheduler, not by the schema).

ALTER TABLE schedules ALTER COLUMN test_id DROP NOT NULL;
ALTER TABLE schedules ADD COLUMN suite_id uuid NULL REFERENCES suites (id) ON DELETE CASCADE;
CREATE INDEX schedules_suite_idx ON schedules (suite_id);
