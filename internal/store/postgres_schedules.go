package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ ScheduleStore = (*Postgres)(nil)

// Schedule queries, written out as constants (the column list matches
// pgScheduleRow.dest).
const (
	pgGetScheduleSQL = `SELECT s.id::text, COALESCE(s.test_id::text, ''), COALESCE(s.suite_id::text, ''), COALESCE(t.name, ''),
	COALESCE(su.name, ''), s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message
	FROM schedules s LEFT JOIN tests t ON t.id = s.test_id LEFT JOIN suites su ON su.id = s.suite_id WHERE s.id = $1`

	pgListSchedulesSQL = `SELECT s.id::text, COALESCE(s.test_id::text, ''), COALESCE(s.suite_id::text, ''), COALESCE(t.name, ''),
	COALESCE(su.name, ''), s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message
	FROM schedules s LEFT JOIN tests t ON t.id = s.test_id LEFT JOIN suites su ON su.id = s.suite_id
	ORDER BY s.created_at, s.id`

	pgListTestSchedulesSQL = `SELECT s.id::text, COALESCE(s.test_id::text, ''), COALESCE(s.suite_id::text, ''), COALESCE(t.name, ''),
	COALESCE(su.name, ''), s.name, s.enabled, s.kind, s.run_at_local, s.cron, s.timezone, s.load_override, s.workers, s.label, s.notes, s.overlap, s.queue_timeout_ms,
	s.created_at, s.updated_at, s.next_run_at, s.last_run_at, s.last_run_id, s.last_status, s.last_message
	FROM schedules s LEFT JOIN tests t ON t.id = s.test_id LEFT JOIN suites su ON su.id = s.suite_id WHERE s.test_id = $1
	ORDER BY s.created_at, s.id`

	pgCreateScheduleSQL = `INSERT INTO schedules (id, test_id, suite_id, name, enabled, kind, run_at_local, cron,
	timezone, load_override, workers, label, notes, overlap, queue_timeout_ms,
	created_at, updated_at, next_run_at)
	VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12,
	$13, $14, $15, $16, $16, $17)`

	pgUpdateScheduleSQL = `UPDATE schedules SET test_id = NULLIF($2, '')::uuid, suite_id = NULLIF($3, '')::uuid, name = $4,
	enabled = $5, kind = $6, run_at_local = $7, cron = $8, timezone = $9, load_override = $10::jsonb,
	workers = $11, label = $12, notes = $13, overlap = $14, queue_timeout_ms = $15, updated_at = $16,
	next_run_at = $17
	WHERE id = $1`

	pgAdvanceScheduleSQL = `UPDATE schedules SET next_run_at = $3, enabled = $4,
	last_run_at = COALESCE($5, last_run_at), last_run_id = $6, last_status = $7, last_message = $8
	WHERE id = $1 AND enabled AND next_run_at = $2`

	pgSetScheduleResultSQL = `UPDATE schedules SET last_run_at = COALESCE($2, last_run_at),
	last_run_id = $3, last_status = $4, last_message = $5
	WHERE id = $1`

	pgDeleteScheduleSQL = `DELETE FROM schedules WHERE id = $1`

	pgScheduleExistsSQL = `SELECT count(*) FROM schedules WHERE id = $1`
)

type pgScheduleRow struct {
	s                    model.Schedule
	load                 []byte
	nextRunAt, lastRunAt *time.Time
	kind, overlap        string
	workers              int32
}

func (r *pgScheduleRow) dest() []any {
	s := &r.s
	return []any{&s.ID, &s.TestID, &s.SuiteID, &s.TestName, &s.SuiteName, &s.Name, &s.Enabled, &r.kind, &s.At,
		&s.Cron, &s.Timezone, &r.load, &r.workers, &s.Label, &s.Notes, &r.overlap, &s.QueueTimeoutMs,
		&s.CreatedAt, &s.UpdatedAt, &r.nextRunAt, &r.lastRunAt, &s.LastRunID, &s.LastStatus, &s.LastMessage}
}

func (r *pgScheduleRow) schedule() (model.Schedule, error) {
	out := r.s
	out.Kind = model.ScheduleKind(r.kind)
	out.Overlap = model.OverlapPolicy(r.overlap)
	out.Workers = int(r.workers)
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	if r.nextRunAt != nil {
		t := r.nextRunAt.UTC()
		out.NextRunAt = &t
	}
	if r.lastRunAt != nil {
		t := r.lastRunAt.UTC()
		out.LastRunAt = &t
	}
	if len(r.load) > 0 && string(r.load) != "null" {
		out.Load = new(model.LoadOverride)
		if err := json.Unmarshal(r.load, out.Load); err != nil {
			return model.Schedule{}, fmt.Errorf("store: decode schedule load: %w", err)
		}
	}
	return out, nil
}

// pgOptTime returns t normalised, or nil (SQL NULL) when unset.
func pgOptTime(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	n := normTime(*t)
	return &n
}

// pgLoad encodes a load override as JSON text (nil = NULL).
func pgLoad(l *model.LoadOverride) (*string, error) {
	if l == nil {
		return nil, nil
	}
	b, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("store: encode schedule load: %w", err)
	}
	s := string(b)
	return &s, nil
}

func (p *Postgres) getSchedule(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id string) (model.Schedule, error) {
	var r pgScheduleRow
	if err := q.QueryRow(ctx, pgGetScheduleSQL, id).Scan(r.dest()...); err != nil {
		return model.Schedule{}, mapErr(err)
	}
	return r.schedule()
}

// ListSchedules returns schedules oldest created first (one test's when
// testID != "").
func (p *Postgres) ListSchedules(ctx context.Context, testID string) ([]model.Schedule, error) {
	var (
		rows pgx.Rows
		err  error
	)
	switch {
	case testID == "":
		rows, err = p.pool.Query(ctx, pgListSchedulesSQL)
	case !isUUID(testID):
		return []model.Schedule{}, nil
	default:
		rows, err = p.pool.Query(ctx, pgListTestSchedulesSQL, testID)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Schedule{}
	for rows.Next() {
		var r pgScheduleRow
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, mapErr(err)
		}
		sc, err := r.schedule()
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetSchedule returns one schedule.
func (p *Postgres) GetSchedule(ctx context.Context, id string) (model.Schedule, error) {
	if !isUUID(id) {
		return model.Schedule{}, ErrNotFound
	}
	return p.getSchedule(ctx, p.pool, id)
}

// CreateSchedule stores a new schedule for an existing test or suite.
func (p *Postgres) CreateSchedule(ctx context.Context, sc model.Schedule) (model.Schedule, error) {
	if !scheduleTargetOK(sc) {
		return model.Schedule{}, ErrNotFound
	}
	load, err := pgLoad(sc.Load)
	if err != nil {
		return model.Schedule{}, err
	}
	id := newID()
	var out model.Schedule
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, pgCreateScheduleSQL,
			id, sc.TestID, sc.SuiteID, sc.Name, sc.Enabled, string(sc.Kind), sc.At, sc.Cron, sc.Timezone, load,
			sc.Workers, sc.Label, sc.Notes, string(sc.Overlap), sc.QueueTimeoutMs, now(),
			pgOptTime(sc.NextRunAt)); err != nil {
			return mapErr(err)
		}
		out, err = p.getSchedule(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}

// UpdateSchedule replaces the spec, Enabled and NextRunAt of a schedule.
func (p *Postgres) UpdateSchedule(ctx context.Context, sc model.Schedule) (model.Schedule, error) {
	if !isUUID(sc.ID) || !scheduleTargetOK(sc) {
		return model.Schedule{}, ErrNotFound
	}
	load, err := pgLoad(sc.Load)
	if err != nil {
		return model.Schedule{}, err
	}
	var out model.Schedule
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, pgUpdateScheduleSQL,
			sc.ID, sc.TestID, sc.SuiteID, sc.Name, sc.Enabled, string(sc.Kind), sc.At, sc.Cron, sc.Timezone, load,
			sc.Workers, sc.Label, sc.Notes, string(sc.Overlap), sc.QueueTimeoutMs, now(),
			pgOptTime(sc.NextRunAt))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		out, err = p.getSchedule(ctx, tx, sc.ID)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}

// DeleteSchedule removes a schedule (runs it started keep schedule_id).
func (p *Postgres) DeleteSchedule(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, pgDeleteScheduleSQL, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdvanceSchedule records an occurrence if the schedule is still enabled
// and due at due (compare and set); see ScheduleStore.
func (p *Postgres) AdvanceSchedule(ctx context.Context, id string, due time.Time, a ScheduleAdvance) (model.Schedule, error) {
	if !isUUID(id) {
		return model.Schedule{}, ErrNotFound
	}
	var out model.Schedule
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, pgAdvanceScheduleSQL, id, normTime(due),
			pgOptTime(a.NextRunAt), a.Enabled, pgOptTime(&a.Result.At),
			a.Result.RunID, a.Result.Status, a.Result.Message)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			var c int
			if err := tx.QueryRow(ctx, pgScheduleExistsSQL, id).Scan(&c); err != nil {
				return mapErr(err)
			}
			if c == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		out, err = p.getSchedule(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}

// SetScheduleResult replaces the Last* fields of a schedule.
func (p *Postgres) SetScheduleResult(ctx context.Context, id string, r ScheduleResult) (model.Schedule, error) {
	if !isUUID(id) {
		return model.Schedule{}, ErrNotFound
	}
	var out model.Schedule
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, pgSetScheduleResultSQL, id, pgOptTime(&r.At), r.RunID, r.Status, r.Message)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		out, err = p.getSchedule(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.Schedule{}, err
	}
	return out, nil
}
