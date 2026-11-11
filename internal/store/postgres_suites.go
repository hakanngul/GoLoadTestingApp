package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ SuiteStore = (*Postgres)(nil)

const (
	pgGetSuiteSQL = `SELECT id::text, name, description, mode, stop_on_failure, gap_ms, items::text,
	created_at, updated_at FROM suites WHERE id = $1`

	pgListSuitesSQL = `SELECT id::text, name, description, mode, stop_on_failure, gap_ms, items::text,
	created_at, updated_at FROM suites ORDER BY name, id`

	pgCreateSuiteSQL = `INSERT INTO suites (id, name, description, mode, stop_on_failure, gap_ms, items,
	created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $8)`

	pgUpdateSuiteSQL = `UPDATE suites SET name = $2, description = $3, mode = $4, stop_on_failure = $5,
	gap_ms = $6, items = $7::jsonb, updated_at = $8 WHERE id = $1`

	pgDeleteSuiteSQL = `DELETE FROM suites WHERE id = $1`

	pgGetSuiteRunSQL = `SELECT id::text, suite_id::text, suite_name, status, mode, stop_on_failure, gap_ms,
	label, started_at, finished_at, items::text, error, started_by FROM suite_runs WHERE id = $1`

	pgListSuiteRunsSQL = `SELECT id::text, suite_id::text, suite_name, status, mode, stop_on_failure, gap_ms,
	label, started_at, finished_at, items::text, error, started_by FROM suite_runs
	WHERE ($1 = '' OR suite_id::text = $1) AND (NOT $2 OR status IN ('queued', 'running'))
	ORDER BY started_at DESC, id DESC LIMIT $3`

	pgCreateSuiteRunSQL = `INSERT INTO suite_runs (id, suite_id, suite_name, status, mode, stop_on_failure,
	gap_ms, label, started_at, finished_at, items, error, started_by)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13)`

	pgUpdateSuiteRunSQL = `UPDATE suite_runs SET status = $2, finished_at = $3, items = $4::jsonb, error = $5
	WHERE id = $1`
)

func scanPgSuite(row pgx.Row) (model.Suite, error) {
	var (
		s           model.Suite
		mode, items string
	)
	if err := row.Scan(&s.ID, &s.Name, &s.Description, &mode, &s.StopOnFailure, &s.GapMs, &items,
		&s.CreatedAt, &s.UpdatedAt); err != nil {
		return model.Suite{}, mapErr(err)
	}
	var err error
	if s.Items, err = decodeSuiteItems(items); err != nil {
		return model.Suite{}, err
	}
	s.Mode = model.SuiteMode(mode)
	s.CreatedAt = normTime(s.CreatedAt)
	s.UpdatedAt = normTime(s.UpdatedAt)
	return s, nil
}

func scanPgSuiteRun(row pgx.Row) (model.SuiteRun, error) {
	var (
		r                   model.SuiteRun
		status, mode, items string
		finished            *time.Time
	)
	if err := row.Scan(&r.ID, &r.SuiteID, &r.SuiteName, &status, &mode, &r.StopOnFailure, &r.GapMs,
		&r.Label, &r.StartedAt, &finished, &items, &r.Error, &r.StartedBy); err != nil {
		return model.SuiteRun{}, mapErr(err)
	}
	var err error
	if r.Items, err = decodeSuiteRunItems(items); err != nil {
		return model.SuiteRun{}, err
	}
	r.Status = model.RunStatus(status)
	r.Mode = model.SuiteMode(mode)
	r.StartedAt = normTime(r.StartedAt)
	r.FinishedAt = pgOptTime(finished)
	return r, nil
}

// ListSuites returns every suite in name order.
func (p *Postgres) ListSuites(ctx context.Context) ([]model.Suite, error) {
	rows, err := p.pool.Query(ctx, pgListSuitesSQL)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Suite{}
	for rows.Next() {
		s, err := scanPgSuite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

// GetSuite returns one suite.
func (p *Postgres) GetSuite(ctx context.Context, id string) (model.Suite, error) {
	if !isUUID(id) {
		return model.Suite{}, ErrNotFound
	}
	return scanPgSuite(p.pool.QueryRow(ctx, pgGetSuiteSQL, id))
}

// CreateSuite stores a new suite.
func (p *Postgres) CreateSuite(ctx context.Context, s model.Suite) (model.Suite, error) {
	items, err := encodeSuiteItems(s.Items)
	if err != nil {
		return model.Suite{}, err
	}
	id := newID()
	var out model.Suite
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, pgCreateSuiteSQL, id, s.Name, s.Description, string(s.Mode),
			s.StopOnFailure, s.GapMs, items, now()); err != nil {
			return mapErr(err)
		}
		out, err = scanPgSuite(tx.QueryRow(ctx, pgGetSuiteSQL, id))
		return err
	})
	return out, err
}

// UpdateSuite replaces a suite's definition.
func (p *Postgres) UpdateSuite(ctx context.Context, s model.Suite) (model.Suite, error) {
	if !isUUID(s.ID) {
		return model.Suite{}, ErrNotFound
	}
	items, err := encodeSuiteItems(s.Items)
	if err != nil {
		return model.Suite{}, err
	}
	var out model.Suite
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, pgUpdateSuiteSQL, s.ID, s.Name, s.Description, string(s.Mode),
			s.StopOnFailure, s.GapMs, items, now())
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		out, err = scanPgSuite(tx.QueryRow(ctx, pgGetSuiteSQL, s.ID))
		return err
	})
	return out, err
}

// DeleteSuite removes a suite and its suite runs.
func (p *Postgres) DeleteSuite(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, pgDeleteSuiteSQL, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSuiteRun stores a new suite run.
func (p *Postgres) CreateSuiteRun(ctx context.Context, r model.SuiteRun) (model.SuiteRun, error) {
	if !isUUID(r.SuiteID) {
		return model.SuiteRun{}, ErrNotFound
	}
	items, err := encodeSuiteRunItems(r.Items)
	if err != nil {
		return model.SuiteRun{}, err
	}
	id := newID()
	var out model.SuiteRun
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, pgCreateSuiteRunSQL, id, r.SuiteID, r.SuiteName, string(r.Status),
			string(r.Mode), r.StopOnFailure, r.GapMs, r.Label, normTime(r.StartedAt), pgOptTime(r.FinishedAt),
			items, r.Error, r.StartedBy); err != nil {
			return mapErr(err)
		}
		out, err = scanPgSuiteRun(tx.QueryRow(ctx, pgGetSuiteRunSQL, id))
		return err
	})
	return out, err
}

// UpdateSuiteRun replaces the mutable fields of a suite run.
func (p *Postgres) UpdateSuiteRun(ctx context.Context, r model.SuiteRun) error {
	if !isUUID(r.ID) {
		return ErrNotFound
	}
	items, err := encodeSuiteRunItems(r.Items)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, pgUpdateSuiteRunSQL, r.ID, string(r.Status), pgOptTime(r.FinishedAt), items, r.Error)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetSuiteRun returns one suite run.
func (p *Postgres) GetSuiteRun(ctx context.Context, id string) (model.SuiteRun, error) {
	if !isUUID(id) {
		return model.SuiteRun{}, ErrNotFound
	}
	return scanPgSuiteRun(p.pool.QueryRow(ctx, pgGetSuiteRunSQL, id))
}

// ListSuiteRuns returns suite runs newest first.
func (p *Postgres) ListSuiteRuns(ctx context.Context, f SuiteRunFilter) ([]model.SuiteRun, error) {
	if f.SuiteID != "" && !isUUID(f.SuiteID) {
		return []model.SuiteRun{}, nil
	}
	rows, err := p.pool.Query(ctx, pgListSuiteRunsSQL, f.SuiteID, f.Active, suiteRunLimit(f))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.SuiteRun{}
	for rows.Next() {
		r, err := scanPgSuiteRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}
