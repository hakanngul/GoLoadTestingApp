package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockKey is the pg_advisory_lock key that serialises schema
// migrations between masters starting concurrently ("goload" in ASCII).
const migrationLockKey int64 = 0x676f6c6f6164

// Postgres is a Store backed by PostgreSQL through a pgx connection pool.
type Postgres struct {
	pool *pgxpool.Pool
}

var _ Store = (*Postgres)(nil)

// NewPostgres connects to dsn, verifies the connection and applies any
// pending embedded migrations before returning. Tables are created in the
// first schema of the connection's search_path.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

// Close releases every pooled connection.
func (p *Postgres) Close() { p.pool.Close() }

// ---- migrations ------------------------------------------------------------

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads migrations/NNNN_name.sql, sorted by version.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		prefix, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("store: bad migration file name %q (want NNNN_name.sql)", name)
		}
		v, err := strconv.Atoi(prefix)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("store: bad migration version in %q", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("store: duplicate migration version %d (%s, %s)", v, prev, name)
		}
		seen[v] = name
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", name, err)
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate applies pending migrations in version order, each in its own
// transaction, while holding a session-level advisory lock so concurrent
// masters cannot race.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("store: migrate: lock: %w", err)
	}
	defer func() {
		// Use a fresh context: ctx may already be cancelled, and the lock
		// must not outlive this function on a pooled connection.
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil {
			// Destroy the connection so a dangling lock is released.
			_ = conn.Conn().Close(uctx)
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("store: migrate: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: migrate: list applied: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return fmt.Errorf("store: migrate: list applied: %w", err)
	}
	for _, v := range versions {
		applied[int(v)] = true
	}

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES ($1, now())`, m.version)
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migrate: apply %s: %w", m.name, err)
		}
	}
	return nil
}

// ---- error mapping ---------------------------------------------------------

// mapErr translates pgx/PostgreSQL errors into the package's sentinel
// errors: no rows -> ErrNotFound, unique violation -> ErrConflict and
// foreign-key violation (missing parent) -> ErrNotFound.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrNotFound, pgErr.ConstraintName)
		}
	}
	return fmt.Errorf("store: %w", err)
}

// ---- tests -----------------------------------------------------------------

// filesExpr aggregates a test's files as a JSON array ordered by name.
const filesExpr = `COALESCE((SELECT json_agg(json_build_object('name', f.name, 'size', f.size) ORDER BY f.name)
	FROM test_files f WHERE f.test_id = t.id), '[]')`

// runCols lists the runs columns in the order scanRun expects, qualified
// with alias.
func runCols(alias string) string {
	cols := []string{"id::text", "test_id::text", "test_name", "status", "yaml", "workers",
		"started_at", "finished_at", "summary", "thresholds", "peak_cpu", "warnings", "error",
		"label", "notes"}
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

// runColsSlim is runCols with the summary's by_step removed (RunsSince).
func runColsSlim(alias string) string {
	return strings.Replace(runCols(alias), alias+".summary,",
		"CASE WHEN jsonb_typeof("+alias+".summary) = 'object' THEN "+alias+".summary - 'by_step' ELSE "+alias+".summary END,", 1)
}

// runRow receives one runs row. Every field is nullable so the same type
// serves the LEFT JOIN in ListTests.
type runRow struct {
	id, testID, testName, status, yaml *string
	workers                            *int32
	startedAt, finishedAt              *time.Time
	summary, thresholds, warnings      []byte
	peakCPU                            *float64
	errText                            *string
	label, notes                       *string
}

func (r *runRow) dest() []any {
	return []any{&r.id, &r.testID, &r.testName, &r.status, &r.yaml, &r.workers,
		&r.startedAt, &r.finishedAt, &r.summary, &r.thresholds, &r.peakCPU, &r.warnings, &r.errText,
		&r.label, &r.notes}
}

// run converts a scanned row; ok is false when the row was all NULL.
func (r *runRow) run() (model.Run, bool, error) {
	if r.id == nil {
		return model.Run{}, false, nil
	}
	out := model.Run{
		ID:         *r.id,
		TestID:     deref(r.testID),
		TestName:   deref(r.testName),
		Status:     model.RunStatus(deref(r.status)),
		YAML:       deref(r.yaml),
		Workers:    int(deref(r.workers)),
		PeakCPU:    deref(r.peakCPU),
		Error:      deref(r.errText),
		Label:      deref(r.label),
		Notes:      deref(r.notes),
		Thresholds: []model.ThresholdResult{},
		Warnings:   []string{},
	}
	if r.startedAt != nil {
		out.StartedAt = r.startedAt.UTC()
	}
	if r.finishedAt != nil {
		f := r.finishedAt.UTC()
		out.FinishedAt = &f
	}
	if len(r.summary) > 0 && string(r.summary) != "null" {
		out.Summary = new(model.Summary)
		if err := json.Unmarshal(r.summary, out.Summary); err != nil {
			return model.Run{}, false, fmt.Errorf("store: decode summary: %w", err)
		}
	}
	if len(r.thresholds) > 0 {
		if err := json.Unmarshal(r.thresholds, &out.Thresholds); err != nil {
			return model.Run{}, false, fmt.Errorf("store: decode thresholds: %w", err)
		}
	}
	if len(r.warnings) > 0 {
		if err := json.Unmarshal(r.warnings, &out.Warnings); err != nil {
			return model.Run{}, false, fmt.Errorf("store: decode warnings: %w", err)
		}
	}
	// A stored JSON null would decode to nil; keep the [] guarantee.
	if out.Thresholds == nil {
		out.Thresholds = []model.ThresholdResult{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	return out, true, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// testCols selects the columns testDest scans, for alias t.
const testCols = `t.id::text, t.name, t.yaml, t.created_at, t.updated_at,
	COALESCE(t.baseline_run_id::text, ''), ` + filesExpr

// testDest returns scan targets for id, name, yaml, created_at,
// updated_at, baseline_run_id and the files JSON.
func testDest(t *model.Test, files *[]byte) []any {
	return []any{&t.ID, &t.Name, &t.YAML, &t.CreatedAt, &t.UpdatedAt, &t.BaselineRunID, files}
}

func finishTest(t *model.Test, files []byte) error {
	t.CreatedAt = t.CreatedAt.UTC()
	t.UpdatedAt = t.UpdatedAt.UTC()
	t.Files = []model.FileInfo{}
	if err := json.Unmarshal(files, &t.Files); err != nil {
		return fmt.Errorf("store: decode files: %w", err)
	}
	if t.Files == nil {
		t.Files = []model.FileInfo{}
	}
	return nil
}

// ListTests returns all tests, most recently updated first, each with its
// files and newest run, in a single query.
func (p *Postgres) ListTests(ctx context.Context) ([]model.Test, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT `+testCols+`, `+runCols("lr")+`
		FROM tests t
		LEFT JOIN LATERAL (
			SELECT * FROM runs r WHERE r.test_id = t.id
			ORDER BY r.started_at DESC, r.id DESC LIMIT 1
		) lr ON true
		ORDER BY t.updated_at DESC, t.id DESC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Test{}
	for rows.Next() {
		var (
			t     model.Test
			files []byte
			rr    runRow
		)
		if err := rows.Scan(append(testDest(&t, &files), rr.dest()...)...); err != nil {
			return nil, mapErr(err)
		}
		if err := finishTest(&t, files); err != nil {
			return nil, err
		}
		r, ok, err := rr.run()
		if err != nil {
			return nil, err
		}
		if ok {
			t.LastRun = &r
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetTest returns one test with its file list (LastRun is not filled).
func (p *Postgres) GetTest(ctx context.Context, id string) (model.Test, error) {
	if !isUUID(id) {
		return model.Test{}, ErrNotFound
	}
	return p.getTest(ctx, p.pool, id)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (p *Postgres) getTest(ctx context.Context, q querier, id string) (model.Test, error) {
	var (
		t     model.Test
		files []byte
	)
	err := q.QueryRow(ctx, `SELECT `+testCols+`
		FROM tests t WHERE t.id = $1`, id).Scan(testDest(&t, &files)...)
	if err != nil {
		return model.Test{}, mapErr(err)
	}
	return t, finishTest(&t, files)
}

// CreateTest stores a new test with a fresh ID and timestamps. Files and
// LastRun on t are ignored (files are attached with PutFile). A duplicate
// name yields ErrConflict.
func (p *Postgres) CreateTest(ctx context.Context, t model.Test) (model.Test, error) {
	ts := now()
	out := model.Test{ID: newID(), Name: t.Name, YAML: t.YAML, Files: []model.FileInfo{}, CreatedAt: ts, UpdatedAt: ts}
	_, err := p.pool.Exec(ctx, `INSERT INTO tests (id, name, yaml, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)`,
		out.ID, out.Name, out.YAML, ts)
	if err != nil {
		return model.Test{}, mapErr(err)
	}
	return out, nil
}

// UpdateTest replaces the name and YAML of an existing test and bumps
// UpdatedAt.
func (p *Postgres) UpdateTest(ctx context.Context, t model.Test) (model.Test, error) {
	if !isUUID(t.ID) {
		return model.Test{}, ErrNotFound
	}
	var out model.Test
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE tests SET name = $2, yaml = $3, updated_at = $4 WHERE id = $1`,
			t.ID, t.Name, t.YAML, now())
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		out, err = p.getTest(ctx, tx, t.ID)
		return err
	})
	if err != nil {
		return model.Test{}, err
	}
	return out, nil
}

// DeleteTest removes a test; files, runs, metrics and error samples go
// with it through ON DELETE CASCADE.
func (p *Postgres) DeleteTest(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, `DELETE FROM tests WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- files -----------------------------------------------------------------

// PutFile creates or replaces a data file attached to a test.
func (p *Postgres) PutFile(ctx context.Context, testID, name string, content []byte) error {
	if !isUUID(testID) {
		return ErrNotFound
	}
	if content == nil {
		content = []byte{}
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO test_files (test_id, name, content, size) VALUES ($1, $2, $3, $4)
		ON CONFLICT (test_id, name) DO UPDATE SET content = EXCLUDED.content, size = EXCLUDED.size`,
		testID, name, content, int64(len(content)))
	return mapErr(err)
}

// GetFile returns a file's content.
func (p *Postgres) GetFile(ctx context.Context, testID, name string) ([]byte, error) {
	if !isUUID(testID) {
		return nil, ErrNotFound
	}
	var content []byte
	err := p.pool.QueryRow(ctx, `SELECT content FROM test_files WHERE test_id = $1 AND name = $2`,
		testID, name).Scan(&content)
	if err != nil {
		return nil, mapErr(err)
	}
	if content == nil {
		content = []byte{}
	}
	return content, nil
}

// DeleteFile removes a file from a test.
func (p *Postgres) DeleteFile(ctx context.Context, testID, name string) error {
	if !isUUID(testID) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, `DELETE FROM test_files WHERE test_id = $1 AND name = $2`, testID, name)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- runs ------------------------------------------------------------------

// runArgs encodes the mutable run columns (test_name .. error).
func runArgs(r model.Run) ([]any, error) {
	var summary []byte // nil -> SQL NULL
	if r.Summary != nil {
		b, err := json.Marshal(r.Summary)
		if err != nil {
			return nil, fmt.Errorf("store: encode summary: %w", err)
		}
		summary = b
	}
	thresholds := r.Thresholds
	if thresholds == nil {
		thresholds = []model.ThresholdResult{}
	}
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	tj, err := json.Marshal(thresholds)
	if err != nil {
		return nil, fmt.Errorf("store: encode thresholds: %w", err)
	}
	wj, err := json.Marshal(warnings)
	if err != nil {
		return nil, fmt.Errorf("store: encode warnings: %w", err)
	}
	var finished *time.Time
	if r.FinishedAt != nil {
		f := normTime(*r.FinishedAt)
		finished = &f
	}
	var started *time.Time // nil -> keep / default
	if !r.StartedAt.IsZero() {
		s := normTime(r.StartedAt)
		started = &s
	}
	return []any{r.TestName, string(r.Status), r.YAML, r.Workers, started, finished,
		summary, tj, r.PeakCPU, wj, r.Error}, nil
}

// CreateRun stores a new run for an existing test. The store assigns the
// ID; StartedAt defaults to now, Status to queued and TestName to the
// test's current name. An unknown test yields ErrNotFound.
func (p *Postgres) CreateRun(ctx context.Context, r model.Run) (model.Run, error) {
	if !isUUID(r.TestID) {
		return model.Run{}, ErrNotFound
	}
	args, err := runArgs(r)
	if err != nil {
		return model.Run{}, err
	}
	id := newID()
	// $1 id, $2 test_id, $3.. = runArgs; $14 = now; $15 label; $16 notes.
	row := p.pool.QueryRow(ctx, `
		INSERT INTO runs (id, test_id, test_name, status, yaml, workers, started_at, finished_at,
		                  summary, thresholds, peak_cpu, warnings, error, label, notes)
		SELECT $1::uuid, t.id, COALESCE(NULLIF($3::text, ''), t.name), COALESCE(NULLIF($4::text, ''), 'queued'),
		       $5::text, $6::integer, COALESCE($7::timestamptz, $14::timestamptz), $8::timestamptz,
		       $9::jsonb, $10::jsonb, $11::double precision, $12::jsonb, $13::text, $15::text, $16::text
		FROM tests t WHERE t.id = $2::uuid
		RETURNING `+runCols("runs"),
		append([]any{id, r.TestID}, append(args, now(), r.Label, r.Notes)...)...)
	var rr runRow
	if err := row.Scan(rr.dest()...); err != nil {
		return model.Run{}, mapErr(err)
	}
	out, _, err := rr.run()
	return out, err
}

// UpdateRun replaces every field of an existing run except ID, TestID,
// Label and Notes. A zero StartedAt keeps the stored value.
func (p *Postgres) UpdateRun(ctx context.Context, r model.Run) error {
	if !isUUID(r.ID) {
		return ErrNotFound
	}
	args, err := runArgs(r)
	if err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE runs SET test_name = $2, status = $3, yaml = $4, workers = $5,
		       started_at = COALESCE($6::timestamptz, started_at), finished_at = $7, summary = $8,
		       thresholds = $9, peak_cpu = $10, warnings = $11, error = $12
		WHERE id = $1`, append([]any{r.ID}, args...)...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRun returns one run.
func (p *Postgres) GetRun(ctx context.Context, id string) (model.Run, error) {
	if !isUUID(id) {
		return model.Run{}, ErrNotFound
	}
	var rr runRow
	if err := p.pool.QueryRow(ctx, `SELECT `+runCols("r")+` FROM runs r WHERE r.id = $1`, id).
		Scan(rr.dest()...); err != nil {
		return model.Run{}, mapErr(err)
	}
	out, _, err := rr.run()
	return out, err
}

// ListRuns returns runs newest first, optionally restricted to one test.
// A non-positive limit means 50.
func (p *Postgres) ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRunLimit
	}
	var (
		rows pgx.Rows
		err  error
	)
	switch {
	case f.TestID == "":
		rows, err = p.pool.Query(ctx, `SELECT `+runCols("r")+` FROM runs r
			ORDER BY r.started_at DESC, r.id DESC LIMIT $1`, limit)
	case !isUUID(f.TestID):
		return []model.Run{}, nil
	default:
		rows, err = p.pool.Query(ctx, `SELECT `+runCols("r")+` FROM runs r WHERE r.test_id = $1
			ORDER BY r.started_at DESC, r.id DESC LIMIT $2`, f.TestID, limit)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		var rr runRow
		if err := rows.Scan(rr.dest()...); err != nil {
			return nil, mapErr(err)
		}
		r, _, err := rr.run()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// MarkInterrupted moves every queued or running run to error with msg and
// sets FinishedAt to now. It returns the number of runs changed.
func (p *Postgres) MarkInterrupted(ctx context.Context, msg string) (int, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET status = 'error', error = $1, finished_at = $2
		WHERE status IN ('queued', 'running')`, msg, now())
	if err != nil {
		return 0, mapErr(err)
	}
	return int(tag.RowsAffected()), nil
}

// ---- metrics & error samples ------------------------------------------------

// AppendMetric stores one per-second point and its encoded snapshot. A
// point with the same T as an existing one replaces it. An unknown run
// yields ErrNotFound.
func (p *Postgres) AppendMetric(ctx context.Context, runID string, pt model.MetricPoint, snapshot []byte) error {
	if !isUUID(runID) {
		return ErrNotFound
	}
	pt.T = normTime(pt.T)
	pj, err := json.Marshal(pt)
	if err != nil {
		return fmt.Errorf("store: encode metric point: %w", err)
	}
	if snapshot == nil {
		snapshot = []byte{}
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO run_metrics (run_id, t, point, snapshot) VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id, t) DO UPDATE SET point = EXCLUDED.point, snapshot = EXCLUDED.snapshot`,
		runID, pt.T, pj, snapshot)
	return mapErr(err)
}

// ListMetrics returns a run's points ordered by T (empty for unknown runs).
func (p *Postgres) ListMetrics(ctx context.Context, runID string) ([]model.MetricPoint, error) {
	out := []model.MetricPoint{}
	if !isUUID(runID) {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT t, point FROM run_metrics WHERE run_id = $1 ORDER BY t`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			t  time.Time
			pj []byte
			pt model.MetricPoint
		)
		if err := rows.Scan(&t, &pj); err != nil {
			return nil, mapErr(err)
		}
		if err := json.Unmarshal(pj, &pt); err != nil {
			return nil, fmt.Errorf("store: decode metric point: %w", err)
		}
		pt.T = t.UTC()
		out = append(out, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// ListSnapshots returns a run's encoded snapshots ordered by T (one per
// stored point, empty for unknown runs).
func (p *Postgres) ListSnapshots(ctx context.Context, runID string) ([][]byte, error) {
	out := [][]byte{}
	if !isUUID(runID) {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT COALESCE(snapshot, ''::bytea) FROM run_metrics WHERE run_id = $1 ORDER BY t`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, mapErr(err)
		}
		if b == nil {
			b = []byte{}
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// AppendErrorSamples stores error samples for a run using COPY. An empty
// slice is a no-op; an unknown run yields ErrNotFound.
func (p *Postgres) AppendErrorSamples(ctx context.Context, runID string, s []model.ErrorSample) error {
	if len(s) == 0 {
		return nil
	}
	if !isUUID(runID) {
		return ErrNotFound
	}
	_, err := p.pool.CopyFrom(ctx, pgx.Identifier{"run_errors"},
		[]string{"run_id", "t", "step", "status", "message"},
		pgx.CopyFromSlice(len(s), func(i int) ([]any, error) {
			e := s[i]
			return []any{runID, normTime(e.T), e.Step, int32(e.Status), e.Message}, nil
		}))
	return mapErr(err)
}

// ListErrorSamples returns a run's error samples ordered by T, then
// insertion order (empty for unknown runs).
func (p *Postgres) ListErrorSamples(ctx context.Context, runID string) ([]model.ErrorSample, error) {
	out := []model.ErrorSample{}
	if !isUUID(runID) {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT t, step, status, message FROM run_errors
		WHERE run_id = $1 ORDER BY t, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e      model.ErrorSample
			status int32
		)
		if err := rows.Scan(&e.T, &e.Step, &status, &e.Message); err != nil {
			return nil, mapErr(err)
		}
		e.T = e.T.UTC()
		e.Status = int(status)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// SetErrorGroups replaces a run's error groups (stored as jsonb).
func (p *Postgres) SetErrorGroups(ctx context.Context, runID string, groups []model.ErrorGroup) error {
	if !isUUID(runID) {
		return ErrNotFound
	}
	cp := make([]model.ErrorGroup, len(groups))
	for i, g := range groups {
		g.First, g.Last = normTime(g.First), normTime(g.Last)
		cp[i] = g
	}
	b, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("store: encode error groups: %w", err)
	}
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET error_groups = $2 WHERE id = $1`, runID, b)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListErrorGroups returns a run's stored error groups.
func (p *Postgres) ListErrorGroups(ctx context.Context, runID string) ([]model.ErrorGroup, error) {
	if !isUUID(runID) {
		return nil, ErrNotFound
	}
	var b []byte
	err := p.pool.QueryRow(ctx, `SELECT COALESCE(error_groups, '[]'::jsonb) FROM runs WHERE id = $1`, runID).Scan(&b)
	if err != nil {
		return nil, mapErr(err)
	}
	out := []model.ErrorGroup{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("store: decode error groups: %w", err)
	}
	for i := range out {
		if !out[i].First.IsZero() {
			out[i].First = out[i].First.UTC()
		}
		if !out[i].Last.IsZero() {
			out[i].Last = out[i].Last.UTC()
		}
	}
	return out, nil
}

// UpdateRunMeta sets label and/or notes (nil = unchanged).
func (p *Postgres) UpdateRunMeta(ctx context.Context, id string, label, notes *string) (model.Run, error) {
	if !isUUID(id) {
		return model.Run{}, ErrNotFound
	}
	var rr runRow
	err := p.pool.QueryRow(ctx, `UPDATE runs SET label = COALESCE($2::text, label), notes = COALESCE($3::text, notes)
		WHERE id = $1 RETURNING `+runCols("runs"), id, label, notes).Scan(rr.dest()...)
	if err != nil {
		return model.Run{}, mapErr(err)
	}
	out, _, err := rr.run()
	return out, err
}

// DeleteRun removes a run; metrics and error samples cascade and a test
// baseline pointing at it is set to NULL by the foreign key.
func (p *Postgres) DeleteRun(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, `DELETE FROM runs WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMetricsWithSnapshots returns a run's points and snapshots, aligned.
func (p *Postgres) ListMetricsWithSnapshots(ctx context.Context, runID string) ([]model.MetricPoint, [][]byte, error) {
	pts, snaps := []model.MetricPoint{}, [][]byte{}
	if !isUUID(runID) {
		return pts, snaps, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT t, point, COALESCE(snapshot, ''::bytea) FROM run_metrics
		WHERE run_id = $1 ORDER BY t`, runID)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			t  time.Time
			pj []byte
			b  []byte
			pt model.MetricPoint
		)
		if err := rows.Scan(&t, &pj, &b); err != nil {
			return nil, nil, mapErr(err)
		}
		if err := json.Unmarshal(pj, &pt); err != nil {
			return nil, nil, fmt.Errorf("store: decode metric point: %w", err)
		}
		pt.T = t.UTC()
		if b == nil {
			b = []byte{}
		}
		pts = append(pts, pt)
		snaps = append(snaps, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, mapErr(err)
	}
	return pts, snaps, nil
}

// SetBaseline sets or clears a test's baseline run.
func (p *Postgres) SetBaseline(ctx context.Context, testID, runID string) (model.Test, error) {
	if !isUUID(testID) {
		return model.Test{}, ErrNotFound
	}
	if runID != "" && !isUUID(runID) {
		return model.Test{}, ErrNotFound
	}
	var out model.Test
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		// Lock the test row so a concurrent delete cannot interleave.
		var tid string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tests WHERE id = $1 FOR UPDATE`, testID).Scan(&tid); err != nil {
			return mapErr(err)
		}
		var baseline *string
		if runID != "" {
			var owner string
			if err := tx.QueryRow(ctx, `SELECT test_id::text FROM runs WHERE id = $1`, runID).Scan(&owner); err != nil {
				return mapErr(err)
			}
			if owner != testID {
				return fmt.Errorf("%w: run belongs to another test", ErrConflict)
			}
			baseline = &runID
		}
		if _, err := tx.Exec(ctx, `UPDATE tests SET baseline_run_id = $2::uuid WHERE id = $1`, testID, baseline); err != nil {
			return mapErr(err)
		}
		var err error
		out, err = p.getTest(ctx, tx, testID)
		return err
	})
	if err != nil {
		return model.Test{}, err
	}
	return out, nil
}

// RunsSince returns runs started at or after since, newest first, with
// summaries stripped of by_step.
func (p *Postgres) RunsSince(ctx context.Context, since time.Time) ([]model.Run, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+runColsSlim("r")+` FROM runs r WHERE r.started_at >= $1
		ORDER BY r.started_at DESC, r.id DESC`, normTime(since))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		var rr runRow
		if err := rows.Scan(rr.dest()...); err != nil {
			return nil, mapErr(err)
		}
		r, _, err := rr.run()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
