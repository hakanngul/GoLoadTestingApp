package store

import (
	"context"
	"database/sql"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ SuiteStore = (*SQLite)(nil)

// Suite queries (constant literals, see sqlite_sql_test.go).
const (
	sqliteSuiteCols = `id, name, description, mode, stop_on_failure, gap_ms, items, created_at, updated_at`

	sqliteGetSuiteSQL = `SELECT id, name, description, mode, stop_on_failure, gap_ms, items, created_at, updated_at
	FROM suites WHERE id = ?`

	sqliteListSuitesSQL = `SELECT id, name, description, mode, stop_on_failure, gap_ms, items, created_at, updated_at
	FROM suites ORDER BY name, id`

	sqliteCreateSuiteSQL = `INSERT INTO suites (id, name, description, mode, stop_on_failure, gap_ms, items,
	created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqliteUpdateSuiteSQL = `UPDATE suites SET name = ?, description = ?, mode = ?, stop_on_failure = ?,
	gap_ms = ?, items = ?, updated_at = ? WHERE id = ?`

	sqliteDeleteSuiteSQL = `DELETE FROM suites WHERE id = ?`

	sqliteSuiteRunCols = `id, suite_id, suite_name, status, mode, stop_on_failure, gap_ms, label,
	started_at, finished_at, items, error`

	sqliteGetSuiteRunSQL = `SELECT id, suite_id, suite_name, status, mode, stop_on_failure, gap_ms, label,
	started_at, finished_at, items, error FROM suite_runs WHERE id = ?`

	sqliteListSuiteRunsSQL = `SELECT id, suite_id, suite_name, status, mode, stop_on_failure, gap_ms, label,
	started_at, finished_at, items, error FROM suite_runs
	WHERE (? = '' OR suite_id = ?) AND (? = 0 OR status IN ('queued', 'running'))
	ORDER BY started_at DESC, id DESC LIMIT ?`

	sqliteCreateSuiteRunSQL = `INSERT INTO suite_runs (id, suite_id, suite_name, status, mode, stop_on_failure,
	gap_ms, label, started_at, finished_at, items, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqliteUpdateSuiteRunSQL = `UPDATE suite_runs SET status = ?, finished_at = ?, items = ?, error = ? WHERE id = ?`
)

func scanSQLiteSuite(sc interface{ Scan(...any) error }) (model.Suite, error) {
	var (
		s                model.Suite
		mode, items      string
		stop             int64
		created, updated int64
	)
	if err := sc.Scan(&s.ID, &s.Name, &s.Description, &mode, &stop, &s.GapMs, &items, &created, &updated); err != nil {
		return model.Suite{}, sqliteErr(err)
	}
	var err error
	if s.Items, err = decodeSuiteItems(items); err != nil {
		return model.Suite{}, err
	}
	s.Mode = model.SuiteMode(mode)
	s.StopOnFailure = stop != 0
	s.CreatedAt = fromMicros(created)
	s.UpdatedAt = fromMicros(updated)
	return s, nil
}

func scanSQLiteSuiteRun(sc interface{ Scan(...any) error }) (model.SuiteRun, error) {
	var (
		r                   model.SuiteRun
		status, mode, items string
		stop, started       int64
		finished            sql.NullInt64
	)
	if err := sc.Scan(&r.ID, &r.SuiteID, &r.SuiteName, &status, &mode, &stop, &r.GapMs, &r.Label,
		&started, &finished, &items, &r.Error); err != nil {
		return model.SuiteRun{}, sqliteErr(err)
	}
	var err error
	if r.Items, err = decodeSuiteRunItems(items); err != nil {
		return model.SuiteRun{}, err
	}
	r.Status = model.RunStatus(status)
	r.Mode = model.SuiteMode(mode)
	r.StopOnFailure = stop != 0
	r.StartedAt = fromMicros(started)
	if finished.Valid {
		t := fromMicros(finished.Int64)
		r.FinishedAt = &t
	}
	return r, nil
}

// ListSuites returns every suite in name order.
func (s *SQLite) ListSuites(ctx context.Context) ([]model.Suite, error) {
	rows, err := s.r.QueryContext(ctx, sqliteListSuitesSQL)
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	out := []model.Suite{}
	for rows.Next() {
		su, err := scanSQLiteSuite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, su)
	}
	return out, sqliteErr(rows.Err())
}

// GetSuite returns one suite.
func (s *SQLite) GetSuite(ctx context.Context, id string) (model.Suite, error) {
	if !isUUID(id) {
		return model.Suite{}, ErrNotFound
	}
	return scanSQLiteSuite(s.r.QueryRowContext(ctx, sqliteGetSuiteSQL, id))
}

// CreateSuite stores a new suite.
func (s *SQLite) CreateSuite(ctx context.Context, su model.Suite) (model.Suite, error) {
	items, err := encodeSuiteItems(su.Items)
	if err != nil {
		return model.Suite{}, err
	}
	id, ts := newID(), toMicros(now())
	var out model.Suite
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqliteCreateSuiteSQL, id, su.Name, su.Description, string(su.Mode),
			boolInt(su.StopOnFailure), su.GapMs, items, ts, ts); err != nil {
			return sqliteErr(err)
		}
		out, err = scanSQLiteSuite(tx.QueryRowContext(ctx, sqliteGetSuiteSQL, id))
		return err
	})
	return out, err
}

// UpdateSuite replaces a suite's definition.
func (s *SQLite) UpdateSuite(ctx context.Context, su model.Suite) (model.Suite, error) {
	if !isUUID(su.ID) {
		return model.Suite{}, ErrNotFound
	}
	items, err := encodeSuiteItems(su.Items)
	if err != nil {
		return model.Suite{}, err
	}
	var out model.Suite
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqliteUpdateSuiteSQL, su.Name, su.Description, string(su.Mode),
			boolInt(su.StopOnFailure), su.GapMs, items, toMicros(now()), su.ID)
		if err != nil {
			return sqliteErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out, err = scanSQLiteSuite(tx.QueryRowContext(ctx, sqliteGetSuiteSQL, su.ID))
		return err
	})
	return out, err
}

// DeleteSuite removes a suite and its suite runs.
func (s *SQLite) DeleteSuite(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, sqliteDeleteSuiteSQL, id)
}

// CreateSuiteRun stores a new suite run.
func (s *SQLite) CreateSuiteRun(ctx context.Context, r model.SuiteRun) (model.SuiteRun, error) {
	if !isUUID(r.SuiteID) {
		return model.SuiteRun{}, ErrNotFound
	}
	items, err := encodeSuiteRunItems(r.Items)
	if err != nil {
		return model.SuiteRun{}, err
	}
	id := newID()
	var out model.SuiteRun
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqliteCreateSuiteRunSQL, id, r.SuiteID, r.SuiteName, string(r.Status),
			string(r.Mode), boolInt(r.StopOnFailure), r.GapMs, r.Label, toMicros(r.StartedAt),
			optMicros(r.FinishedAt), items, r.Error); err != nil {
			return sqliteErr(err)
		}
		out, err = scanSQLiteSuiteRun(tx.QueryRowContext(ctx, sqliteGetSuiteRunSQL, id))
		return err
	})
	return out, err
}

// UpdateSuiteRun replaces the mutable fields of a suite run.
func (s *SQLite) UpdateSuiteRun(ctx context.Context, r model.SuiteRun) error {
	if !isUUID(r.ID) {
		return ErrNotFound
	}
	items, err := encodeSuiteRunItems(r.Items)
	if err != nil {
		return err
	}
	return s.execOne(ctx, sqliteUpdateSuiteRunSQL, string(r.Status), optMicros(r.FinishedAt), items, r.Error, r.ID)
}

// GetSuiteRun returns one suite run.
func (s *SQLite) GetSuiteRun(ctx context.Context, id string) (model.SuiteRun, error) {
	if !isUUID(id) {
		return model.SuiteRun{}, ErrNotFound
	}
	return scanSQLiteSuiteRun(s.r.QueryRowContext(ctx, sqliteGetSuiteRunSQL, id))
}

// ListSuiteRuns returns suite runs newest first.
func (s *SQLite) ListSuiteRuns(ctx context.Context, f SuiteRunFilter) ([]model.SuiteRun, error) {
	if f.SuiteID != "" && !isUUID(f.SuiteID) {
		return []model.SuiteRun{}, nil
	}
	rows, err := s.r.QueryContext(ctx, sqliteListSuiteRunsSQL, f.SuiteID, f.SuiteID, boolInt(f.Active), suiteRunLimit(f))
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	out := []model.SuiteRun{}
	for rows.Next() {
		r, err := scanSQLiteSuiteRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sqliteErr(rows.Err())
}
