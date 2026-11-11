package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ ScheduleStore = (*SQLite)(nil)

// Schedule queries. The column list is repeated in each literal so every
// query is a constant (see sqliteScheduleCols and sqlite_sql_test.go).
const (
	sqliteScheduleCols = `s.id, COALESCE(s.test_id, ''), COALESCE(s.suite_id, ''), COALESCE(t.name, ''), COALESCE(su.name, ''),
	s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message`

	sqliteGetScheduleSQL = `SELECT s.id, COALESCE(s.test_id, ''), COALESCE(s.suite_id, ''), COALESCE(t.name, ''), COALESCE(su.name, ''),
	s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message
	FROM schedules s LEFT JOIN tests t ON t.id = s.test_id LEFT JOIN suites su ON su.id = s.suite_id WHERE s.id = ?`

	sqliteListSchedulesSQL = `SELECT s.id, COALESCE(s.test_id, ''), COALESCE(s.suite_id, ''), COALESCE(t.name, ''), COALESCE(su.name, ''),
	s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message
	FROM schedules s LEFT JOIN tests t ON t.id = s.test_id LEFT JOIN suites su ON su.id = s.suite_id
	ORDER BY s.created_at, s.id`

	sqliteListTestSchedulesSQL = `SELECT s.id, COALESCE(s.test_id, ''), COALESCE(s.suite_id, ''), COALESCE(t.name, ''), COALESCE(su.name, ''),
	s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message
	FROM schedules s LEFT JOIN tests t ON t.id = s.test_id LEFT JOIN suites su ON su.id = s.suite_id WHERE s.test_id = ?
	ORDER BY s.created_at, s.id`

	sqliteCreateScheduleSQL = `INSERT INTO schedules (id, test_id, suite_id, name, enabled, kind, run_at_local, cron,
	timezone, load_override, workers, label, notes, overlap, queue_timeout_ms,
	created_at, updated_at, next_run_at)
	VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqliteUpdateScheduleSQL = `UPDATE schedules SET test_id = NULLIF(?, ''), suite_id = NULLIF(?, ''), name = ?, enabled = ?, kind = ?,
	run_at_local = ?, cron = ?, timezone = ?, load_override = ?, workers = ?, label = ?, notes = ?,
	overlap = ?, queue_timeout_ms = ?, updated_at = ?, next_run_at = ?
	WHERE id = ?`

	sqliteAdvanceScheduleSQL = `UPDATE schedules SET next_run_at = ?, enabled = ?,
	last_run_at = COALESCE(?, last_run_at), last_run_id = ?, last_status = ?, last_message = ?
	WHERE id = ? AND enabled = 1 AND next_run_at = ?`

	sqliteSetScheduleResultSQL = `UPDATE schedules SET last_run_at = COALESCE(?, last_run_at),
	last_run_id = ?, last_status = ?, last_message = ?
	WHERE id = ?`

	sqliteDeleteScheduleSQL = `DELETE FROM schedules WHERE id = ?`

	sqliteScheduleExistsSQL = `SELECT count(*) FROM schedules WHERE id = ?`
)

// sqliteScheduleRow receives one schedules row (sqliteScheduleCols).
type sqliteScheduleRow struct {
	s                              model.Schedule
	enabled                        int64
	load                           sql.NullString
	created, updated               int64
	nextRunAt, lastRunAt           sql.NullInt64
	kind, overlap, testNm, suiteNm string
}

func (r *sqliteScheduleRow) dest() []any {
	s := &r.s
	return []any{&s.ID, &s.TestID, &s.SuiteID, &r.testNm, &r.suiteNm, &s.Name, &r.enabled, &r.kind, &s.At, &s.Cron,
		&s.Timezone, &r.load, &s.Workers, &s.Label, &s.Notes, &r.overlap, &s.QueueTimeoutMs,
		&r.created, &r.updated, &r.nextRunAt, &r.lastRunAt, &s.LastRunID, &s.LastStatus, &s.LastMessage}
}

func (r *sqliteScheduleRow) schedule() (model.Schedule, error) {
	out := r.s
	out.TestName = r.testNm
	out.SuiteName = r.suiteNm
	out.Enabled = r.enabled != 0
	out.Kind = model.ScheduleKind(r.kind)
	out.Overlap = model.OverlapPolicy(r.overlap)
	out.CreatedAt = fromMicros(r.created)
	out.UpdatedAt = fromMicros(r.updated)
	if r.nextRunAt.Valid {
		t := fromMicros(r.nextRunAt.Int64)
		out.NextRunAt = &t
	}
	if r.lastRunAt.Valid {
		t := fromMicros(r.lastRunAt.Int64)
		out.LastRunAt = &t
	}
	if r.load.Valid && r.load.String != "" && r.load.String != "null" {
		out.Load = new(model.LoadOverride)
		if err := json.Unmarshal([]byte(r.load.String), out.Load); err != nil {
			return model.Schedule{}, fmt.Errorf("store: decode schedule load: %w", err)
		}
	}
	return out, nil
}

// encodeLoad returns the stored JSON of a load override (nil = NULL).
func encodeLoad(l *model.LoadOverride) (any, error) {
	if l == nil {
		return nil, nil
	}
	b, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("store: encode schedule load: %w", err)
	}
	return string(b), nil
}

// optMicros encodes an optional timestamp (nil = NULL).
func optMicros(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return toMicros(*t)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (s *SQLite) getSchedule(ctx context.Context, q sqliteQuerier, id string) (model.Schedule, error) {
	var r sqliteScheduleRow
	if err := q.QueryRowContext(ctx, sqliteGetScheduleSQL, id).Scan(r.dest()...); err != nil {
		return model.Schedule{}, sqliteErr(err)
	}
	return r.schedule()
}

// ListSchedules returns schedules oldest created first (one test's when
// testID != "").
func (s *SQLite) ListSchedules(ctx context.Context, testID string) ([]model.Schedule, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case testID == "":
		rows, err = s.r.QueryContext(ctx, sqliteListSchedulesSQL)
	case !isUUID(testID):
		return []model.Schedule{}, nil
	default:
		rows, err = s.r.QueryContext(ctx, sqliteListTestSchedulesSQL, testID)
	}
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	out := []model.Schedule{}
	for rows.Next() {
		var r sqliteScheduleRow
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, sqliteErr(err)
		}
		sc, err := r.schedule()
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, sqliteErr(err)
	}
	return out, nil
}

// GetSchedule returns one schedule.
func (s *SQLite) GetSchedule(ctx context.Context, id string) (model.Schedule, error) {
	if !isUUID(id) {
		return model.Schedule{}, ErrNotFound
	}
	return s.getSchedule(ctx, s.r, id)
}

// CreateSchedule stores a new schedule for an existing test or suite.
func (s *SQLite) CreateSchedule(ctx context.Context, sc model.Schedule) (model.Schedule, error) {
	if !scheduleTargetOK(sc) {
		return model.Schedule{}, ErrNotFound
	}
	load, err := encodeLoad(sc.Load)
	if err != nil {
		return model.Schedule{}, err
	}
	id, ts := newID(), toMicros(now())
	var out model.Schedule
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqliteCreateScheduleSQL,
			id, sc.TestID, sc.SuiteID, sc.Name, boolInt(sc.Enabled), string(sc.Kind), sc.At, sc.Cron,
			sc.Timezone, load, sc.Workers, sc.Label, sc.Notes, string(sc.Overlap), sc.QueueTimeoutMs,
			ts, ts, optMicros(sc.NextRunAt)); err != nil {
			return sqliteErr(err)
		}
		out, err = s.getSchedule(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}

// UpdateSchedule replaces the spec, Enabled and NextRunAt of a schedule.
func (s *SQLite) UpdateSchedule(ctx context.Context, sc model.Schedule) (model.Schedule, error) {
	if !isUUID(sc.ID) || !scheduleTargetOK(sc) {
		return model.Schedule{}, ErrNotFound
	}
	load, err := encodeLoad(sc.Load)
	if err != nil {
		return model.Schedule{}, err
	}
	var out model.Schedule
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqliteUpdateScheduleSQL,
			sc.TestID, sc.SuiteID, sc.Name, boolInt(sc.Enabled), string(sc.Kind), sc.At, sc.Cron, sc.Timezone,
			load, sc.Workers, sc.Label, sc.Notes, string(sc.Overlap), sc.QueueTimeoutMs,
			toMicros(now()), optMicros(sc.NextRunAt), sc.ID)
		if err != nil {
			return sqliteErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out, err = s.getSchedule(ctx, tx, sc.ID)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}

// DeleteSchedule removes a schedule (runs it started keep schedule_id).
func (s *SQLite) DeleteSchedule(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, sqliteDeleteScheduleSQL, id)
}

// AdvanceSchedule records an occurrence if the schedule is still enabled
// and due at due (compare and set); see ScheduleStore.
func (s *SQLite) AdvanceSchedule(ctx context.Context, id string, due time.Time, a ScheduleAdvance) (model.Schedule, error) {
	if !isUUID(id) {
		return model.Schedule{}, ErrNotFound
	}
	var out model.Schedule
	err := sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqliteAdvanceScheduleSQL,
			optMicros(a.NextRunAt), boolInt(a.Enabled), optMicros(&a.Result.At),
			a.Result.RunID, a.Result.Status, a.Result.Message, id, toMicros(due))
		if err != nil {
			return sqliteErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var c int
			if err := tx.QueryRowContext(ctx, sqliteScheduleExistsSQL, id).Scan(&c); err != nil {
				return sqliteErr(err)
			}
			if c == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		out, err = s.getSchedule(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}

// SetScheduleResult replaces the Last* fields of a schedule.
func (s *SQLite) SetScheduleResult(ctx context.Context, id string, r ScheduleResult) (model.Schedule, error) {
	if !isUUID(id) {
		return model.Schedule{}, ErrNotFound
	}
	var out model.Schedule
	err := sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqliteSetScheduleResultSQL,
			optMicros(&r.At), r.RunID, r.Status, r.Message, id)
		if err != nil {
			return sqliteErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out, err = s.getSchedule(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}
