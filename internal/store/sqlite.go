package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

//go:embed migrations_sqlite/*.sql
var sqliteMigrationFS embed.FS

// SQLite is a Store backed by a single SQLite database file through the
// pure-Go modernc.org/sqlite driver (no CGO).
//
// Connections: every write goes through one dedicated connection (w), so
// writers queue in Go instead of failing with SQLITE_BUSY; reads use a
// small pool of query-only connections (r) that run concurrently with the
// writer thanks to WAL mode. For an in-memory database both are the same
// single connection, since each connection would otherwise see its own
// empty database.
//
// Timestamps are stored as INTEGER microseconds since the Unix epoch
// (UTC), the precision PostgreSQL keeps, so both stores return identical
// values and ORDER BY works numerically.
type SQLite struct {
	w, r   *sql.DB
	memory bool // ":memory:" database: nothing to reclaim on disk
}

var _ Store = (*SQLite)(nil)

// sqliteBusyTimeout is how long a connection waits for a lock held by
// another process (e.g. the sqlite3 CLI taking a backup) before failing.
const sqliteBusyTimeout = 10 * time.Second

// NewSQLite opens (creating if needed) the SQLite database at path and
// applies pending embedded migrations. path ":memory:" opens a private
// in-memory database (one connection; data is lost on Close).
func NewSQLite(ctx context.Context, path string) (*SQLite, error) {
	if path == "" {
		return nil, errors.New("store: sqlite: empty path")
	}
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("store: sqlite: path %q must not contain '?'", path)
	}
	memory := path == ":memory:"

	pragmas := []string{
		// Must come first: it only takes effect on a database without
		// tables, and before journal_mode(WAL) writes the file header. On
		// an existing file it is inert until a VACUUM (see sqlite_prune.go).
		"auto_vacuum(INCREMENTAL)",
		fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout.Milliseconds()),
		"foreign_keys(1)",
		"synchronous(NORMAL)", // durable across app crashes in WAL mode; fsync on checkpoint
	}
	if !memory {
		pragmas = append(pragmas, "journal_mode(WAL)")
	}
	dsn := func(extra ...string) string {
		q := url.Values{}
		for _, p := range append(pragmas, extra...) {
			q.Add("_pragma", p)
		}
		// Writers take the write lock up front: a deferred transaction that
		// later upgrades can fail with SQLITE_BUSY without honouring
		// busy_timeout when another process holds the lock.
		q.Set("_txlock", "immediate")
		return path + "?" + q.Encode()
	}

	w, err := sql.Open("sqlite", dsn())
	if err != nil {
		return nil, fmt.Errorf("store: sqlite: open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: sqlite: open %s: %w", path, err)
	}
	if err := migrateSQLite(ctx, w); err != nil {
		w.Close()
		return nil, err
	}

	r := w
	if !memory {
		r, err = sql.Open("sqlite", dsn("query_only(1)"))
		if err != nil {
			w.Close()
			return nil, fmt.Errorf("store: sqlite: open %s: %w", path, err)
		}
		n := runtime.GOMAXPROCS(0)
		if n < 2 {
			n = 2
		}
		if n > 8 {
			n = 8
		}
		r.SetMaxOpenConns(n)
		r.SetMaxIdleConns(n)
		r.SetConnMaxIdleTime(5 * time.Minute)
		if err := r.PingContext(ctx); err != nil {
			r.Close()
			w.Close()
			return nil, fmt.Errorf("store: sqlite: open %s: %w", path, err)
		}
	}
	return &SQLite{w: w, r: r, memory: memory}, nil
}

// Close checkpoints the WAL into the main file and closes every
// connection.
func (s *SQLite) Close() {
	if s.r != s.w {
		s.r.Close()
	}
	// Best effort: leaves a self-contained .db file when this was the last
	// connection, which makes copying it while stopped safe.
	_, _ = s.w.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	s.w.Close()
}

// ---- migrations ------------------------------------------------------------

// migrateSQLite applies pending migrations in version order, each in its
// own IMMEDIATE transaction (which takes the database write lock, so two
// processes opening the same file cannot both apply one).
func migrateSQLite(ctx context.Context, db *sql.DB) error {
	migs, err := loadMigrationsFrom(sqliteMigrationFS, "migrations_sqlite")
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: migrate: create schema_migrations: %w", err)
	}
	for _, m := range migs {
		err := sqliteTx(ctx, db, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return nil
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				m.version, toMicros(now()))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migrate: apply %s: %w", m.name, err)
		}
	}
	return nil
}

// sqliteTx runs fn in a transaction, committing on success.
func sqliteTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return sqliteErr(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return sqliteErr(tx.Commit())
}

// ---- helpers ---------------------------------------------------------------

// toMicros encodes t (normalised with normTime) as stored.
func toMicros(t time.Time) int64 { return normTime(t).UnixMicro() }

// fromMicros decodes a stored timestamp as a UTC time.
func fromMicros(v int64) time.Time { return time.UnixMicro(v).UTC() }

// sqliteErr translates driver errors into the package's sentinel errors:
// no rows -> ErrNotFound, unique/primary-key violation -> ErrConflict and
// foreign-key violation (missing parent) -> ErrNotFound.
func sqliteErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return err
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %s", ErrConflict, se.Error())
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%w: %s", ErrNotFound, se.Error())
		}
	}
	return fmt.Errorf("store: %w", err)
}

// sqliteQuerier is satisfied by *sql.DB and *sql.Tx.
type sqliteQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// sqliteRunColumns lists the runs columns in the order sqliteRunRow scans.
var sqliteRunColumns = []string{"id", "test_id", "test_name", "status", "yaml", "workers",
	"started_at", "finished_at", "summary", "thresholds", "peak_cpu", "warnings", "error",
	"label", "notes"}

// sqliteRunCols returns the run columns, qualified with alias unless it is
// empty (RETURNING only accepts bare column names).
func sqliteRunCols(alias string) string {
	cols := make([]string, len(sqliteRunColumns))
	for i, c := range sqliteRunColumns {
		if alias != "" {
			c = alias + "." + c
		}
		cols[i] = c
	}
	return strings.Join(cols, ", ")
}

// sqliteRunColsSlim is sqliteRunCols with the summary's by_step removed
// (RunsSince).
func sqliteRunColsSlim(alias string) string {
	sum := alias + ".summary"
	return strings.Replace(sqliteRunCols(alias), sum+",",
		"CASE WHEN json_type("+sum+") = 'object' THEN json_remove("+sum+", '$.by_step') ELSE "+sum+" END,", 1)
}

// sqliteRunRow receives one runs row. Every field is nullable so the same
// type serves the LEFT JOIN in ListTests.
type sqliteRunRow struct {
	id, testID, testName, status, yaml sql.NullString
	workers                            sql.NullInt64
	startedAt, finishedAt              sql.NullInt64
	summary, thresholds, warnings      sql.NullString
	peakCPU                            sql.NullFloat64
	errText, label, notes              sql.NullString
}

func (r *sqliteRunRow) dest() []any {
	return []any{&r.id, &r.testID, &r.testName, &r.status, &r.yaml, &r.workers,
		&r.startedAt, &r.finishedAt, &r.summary, &r.thresholds, &r.peakCPU, &r.warnings, &r.errText,
		&r.label, &r.notes}
}

// run converts a scanned row; ok is false when the row was all NULL.
func (r *sqliteRunRow) run() (model.Run, bool, error) {
	if !r.id.Valid {
		return model.Run{}, false, nil
	}
	out := model.Run{
		ID:         r.id.String,
		TestID:     r.testID.String,
		TestName:   r.testName.String,
		Status:     model.RunStatus(r.status.String),
		YAML:       r.yaml.String,
		Workers:    int(r.workers.Int64),
		PeakCPU:    r.peakCPU.Float64,
		Error:      r.errText.String,
		Label:      r.label.String,
		Notes:      r.notes.String,
		Thresholds: []model.ThresholdResult{},
		Warnings:   []string{},
	}
	if r.startedAt.Valid {
		out.StartedAt = fromMicros(r.startedAt.Int64)
	}
	if r.finishedAt.Valid {
		f := fromMicros(r.finishedAt.Int64)
		out.FinishedAt = &f
	}
	if r.summary.Valid && r.summary.String != "" && r.summary.String != "null" {
		out.Summary = new(model.Summary)
		if err := json.Unmarshal([]byte(r.summary.String), out.Summary); err != nil {
			return model.Run{}, false, fmt.Errorf("store: decode summary: %w", err)
		}
	}
	if r.thresholds.Valid && r.thresholds.String != "" {
		if err := json.Unmarshal([]byte(r.thresholds.String), &out.Thresholds); err != nil {
			return model.Run{}, false, fmt.Errorf("store: decode thresholds: %w", err)
		}
	}
	if r.warnings.Valid && r.warnings.String != "" {
		if err := json.Unmarshal([]byte(r.warnings.String), &out.Warnings); err != nil {
			return model.Run{}, false, fmt.Errorf("store: decode warnings: %w", err)
		}
	}
	if out.Thresholds == nil {
		out.Thresholds = []model.ThresholdResult{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	return out, true, nil
}

// scanRuns collects every row of rows as runs.
func scanSQLiteRuns(rows *sql.Rows) ([]model.Run, error) {
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		var rr sqliteRunRow
		if err := rows.Scan(rr.dest()...); err != nil {
			return nil, sqliteErr(err)
		}
		r, _, err := rr.run()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, sqliteErr(err)
	}
	return out, nil
}

// sqliteTestCols selects the columns sqliteTestRow scans, for alias t; the
// files are aggregated as a JSON array ordered by name.
const sqliteTestCols = `t.id, t.name, t.yaml, t.created_at, t.updated_at, COALESCE(t.baseline_run_id, ''),
	(SELECT json_group_array(json_object('name', f.name, 'size', f.size) ORDER BY f.name)
	 FROM test_files f WHERE f.test_id = t.id)`

type sqliteTestRow struct {
	t                model.Test
	created, updated int64
	files            sql.NullString
}

func (r *sqliteTestRow) dest() []any {
	return []any{&r.t.ID, &r.t.Name, &r.t.YAML, &r.created, &r.updated, &r.t.BaselineRunID, &r.files}
}

func (r *sqliteTestRow) test() (model.Test, error) {
	t := r.t
	t.CreatedAt = fromMicros(r.created)
	t.UpdatedAt = fromMicros(r.updated)
	t.Files = []model.FileInfo{}
	if r.files.Valid && r.files.String != "" {
		if err := json.Unmarshal([]byte(r.files.String), &t.Files); err != nil {
			return model.Test{}, fmt.Errorf("store: decode files: %w", err)
		}
	}
	if t.Files == nil {
		t.Files = []model.FileInfo{}
	}
	return t, nil
}

// ---- tests -----------------------------------------------------------------

// ListTests returns all tests, most recently updated first, each with its
// files and newest run, in a single query.
func (s *SQLite) ListTests(ctx context.Context) ([]model.Test, error) {
	rows, err := s.r.QueryContext(ctx, `
		SELECT `+sqliteTestCols+`, `+sqliteRunCols("lr")+`
		FROM tests t
		LEFT JOIN runs lr ON lr.id = (
			SELECT r.id FROM runs r WHERE r.test_id = t.id
			ORDER BY r.started_at DESC, r.id DESC LIMIT 1)
		ORDER BY t.updated_at DESC, t.id DESC`)
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	out := []model.Test{}
	for rows.Next() {
		var (
			tr sqliteTestRow
			rr sqliteRunRow
		)
		if err := rows.Scan(append(tr.dest(), rr.dest()...)...); err != nil {
			return nil, sqliteErr(err)
		}
		t, err := tr.test()
		if err != nil {
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
		return nil, sqliteErr(err)
	}
	return out, nil
}

// GetTest returns one test with its file list (LastRun is not filled).
func (s *SQLite) GetTest(ctx context.Context, id string) (model.Test, error) {
	if !isUUID(id) {
		return model.Test{}, ErrNotFound
	}
	return s.getTest(ctx, s.r, id)
}

func (s *SQLite) getTest(ctx context.Context, q sqliteQuerier, id string) (model.Test, error) {
	var tr sqliteTestRow
	if err := q.QueryRowContext(ctx, `SELECT `+sqliteTestCols+` FROM tests t WHERE t.id = ?`, id).
		Scan(tr.dest()...); err != nil {
		return model.Test{}, sqliteErr(err)
	}
	return tr.test()
}

// CreateTest stores a new test with a fresh ID and timestamps. A duplicate
// name yields ErrConflict.
func (s *SQLite) CreateTest(ctx context.Context, t model.Test) (model.Test, error) {
	ts := now()
	out := model.Test{ID: newID(), Name: t.Name, YAML: t.YAML, Files: []model.FileInfo{}, CreatedAt: ts, UpdatedAt: ts}
	_, err := s.w.ExecContext(ctx, `INSERT INTO tests (id, name, yaml, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		out.ID, out.Name, out.YAML, toMicros(ts), toMicros(ts))
	if err != nil {
		return model.Test{}, sqliteErr(err)
	}
	return out, nil
}

// UpdateTest replaces the name and YAML of an existing test and bumps
// UpdatedAt.
func (s *SQLite) UpdateTest(ctx context.Context, t model.Test) (model.Test, error) {
	if !isUUID(t.ID) {
		return model.Test{}, ErrNotFound
	}
	var out model.Test
	err := sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tests SET name = ?, yaml = ?, updated_at = ? WHERE id = ?`,
			t.Name, t.YAML, toMicros(now()), t.ID)
		if err != nil {
			return sqliteErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out, err = s.getTest(ctx, tx, t.ID)
		return err
	})
	if err != nil {
		return model.Test{}, err
	}
	return out, nil
}

// DeleteTest removes a test; files, runs, metrics and error samples go
// with it through ON DELETE CASCADE.
func (s *SQLite) DeleteTest(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, `DELETE FROM tests WHERE id = ?`, id)
}

// execOne runs a write that must affect at least one row (else
// ErrNotFound).
func (s *SQLite) execOne(ctx context.Context, query string, args ...any) error {
	res, err := s.w.ExecContext(ctx, query, args...)
	if err != nil {
		return sqliteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- files -----------------------------------------------------------------

// PutFile creates or replaces a data file attached to a test.
func (s *SQLite) PutFile(ctx context.Context, testID, name string, content []byte) error {
	if !isUUID(testID) {
		return ErrNotFound
	}
	if content == nil {
		content = []byte{}
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO test_files (test_id, name, content, size) VALUES (?, ?, ?, ?)
		ON CONFLICT (test_id, name) DO UPDATE SET content = excluded.content, size = excluded.size`,
		testID, name, content, int64(len(content)))
	return sqliteErr(err)
}

// GetFile returns a file's content.
func (s *SQLite) GetFile(ctx context.Context, testID, name string) ([]byte, error) {
	if !isUUID(testID) {
		return nil, ErrNotFound
	}
	var content []byte
	if err := s.r.QueryRowContext(ctx, `SELECT content FROM test_files WHERE test_id = ? AND name = ?`,
		testID, name).Scan(&content); err != nil {
		return nil, sqliteErr(err)
	}
	if content == nil {
		content = []byte{}
	}
	return content, nil
}

// DeleteFile removes a file from a test.
func (s *SQLite) DeleteFile(ctx context.Context, testID, name string) error {
	if !isUUID(testID) {
		return ErrNotFound
	}
	return s.execOne(ctx, `DELETE FROM test_files WHERE test_id = ? AND name = ?`, testID, name)
}

// ---- runs ------------------------------------------------------------------

// sqliteRunArgs encodes the mutable run columns test_name .. error, in
// that order. started_at is nil when StartedAt is zero.
func sqliteRunArgs(r model.Run) ([]any, error) {
	var summary any // nil -> SQL NULL
	if r.Summary != nil {
		b, err := json.Marshal(r.Summary)
		if err != nil {
			return nil, fmt.Errorf("store: encode summary: %w", err)
		}
		summary = string(b)
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
	var finished, started any
	if r.FinishedAt != nil {
		finished = toMicros(*r.FinishedAt)
	}
	if !r.StartedAt.IsZero() {
		started = toMicros(r.StartedAt)
	}
	return []any{r.TestName, string(r.Status), r.YAML, r.Workers, started, finished,
		summary, string(tj), r.PeakCPU, string(wj), r.Error}, nil
}

// CreateRun stores a new run for an existing test. The store assigns the
// ID; StartedAt defaults to now, Status to queued and TestName to the
// test's current name. An unknown test yields ErrNotFound.
func (s *SQLite) CreateRun(ctx context.Context, r model.Run) (model.Run, error) {
	if !isUUID(r.TestID) {
		return model.Run{}, ErrNotFound
	}
	a, err := sqliteRunArgs(r)
	if err != nil {
		return model.Run{}, err
	}
	var rr sqliteRunRow
	err = s.w.QueryRowContext(ctx, `
		INSERT INTO runs (id, test_id, test_name, status, yaml, workers, started_at, finished_at,
		                  summary, thresholds, peak_cpu, warnings, error, label, notes)
		SELECT ?, t.id, COALESCE(NULLIF(?, ''), t.name), COALESCE(NULLIF(?, ''), 'queued'),
		       ?, ?, COALESCE(?, ?), ?, ?, ?, ?, ?, ?, ?, ?
		FROM tests t WHERE t.id = ?
		RETURNING `+sqliteRunCols(""),
		newID(), a[0], a[1], a[2], a[3], a[4], toMicros(now()), a[5], a[6], a[7], a[8], a[9], a[10],
		r.Label, r.Notes, r.TestID).Scan(rr.dest()...)
	if err != nil {
		return model.Run{}, sqliteErr(err)
	}
	out, _, err := rr.run()
	return out, err
}

// UpdateRun replaces every field of an existing run except ID, TestID,
// Label and Notes. A zero StartedAt keeps the stored value.
func (s *SQLite) UpdateRun(ctx context.Context, r model.Run) error {
	if !isUUID(r.ID) {
		return ErrNotFound
	}
	a, err := sqliteRunArgs(r)
	if err != nil {
		return err
	}
	return s.execOne(ctx, `
		UPDATE runs SET test_name = ?, status = ?, yaml = ?, workers = ?,
		       started_at = COALESCE(?, started_at), finished_at = ?, summary = ?,
		       thresholds = ?, peak_cpu = ?, warnings = ?, error = ?
		WHERE id = ?`, append(a, r.ID)...)
}

// GetRun returns one run.
func (s *SQLite) GetRun(ctx context.Context, id string) (model.Run, error) {
	if !isUUID(id) {
		return model.Run{}, ErrNotFound
	}
	var rr sqliteRunRow
	if err := s.r.QueryRowContext(ctx, `SELECT `+sqliteRunCols("r")+` FROM runs r WHERE r.id = ?`, id).
		Scan(rr.dest()...); err != nil {
		return model.Run{}, sqliteErr(err)
	}
	out, _, err := rr.run()
	return out, err
}

// ListRuns returns runs newest first, optionally restricted to one test.
// A non-positive limit means 50.
func (s *SQLite) ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRunLimit
	}
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case f.TestID == "":
		rows, err = s.r.QueryContext(ctx, `SELECT `+sqliteRunCols("r")+` FROM runs r
			ORDER BY r.started_at DESC, r.id DESC LIMIT ?`, limit)
	case !isUUID(f.TestID):
		return []model.Run{}, nil
	default:
		rows, err = s.r.QueryContext(ctx, `SELECT `+sqliteRunCols("r")+` FROM runs r WHERE r.test_id = ?
			ORDER BY r.started_at DESC, r.id DESC LIMIT ?`, f.TestID, limit)
	}
	if err != nil {
		return nil, sqliteErr(err)
	}
	return scanSQLiteRuns(rows)
}

// MarkInterrupted moves every queued or running run to error with msg and
// sets FinishedAt to now. It returns the number of runs changed.
func (s *SQLite) MarkInterrupted(ctx context.Context, msg string) (int, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE runs SET status = 'error', error = ?, finished_at = ?
		WHERE status IN ('queued', 'running')`, msg, toMicros(now()))
	if err != nil {
		return 0, sqliteErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, sqliteErr(err)
	}
	return int(n), nil
}

// UpdateRunMeta sets label and/or notes (nil = unchanged).
func (s *SQLite) UpdateRunMeta(ctx context.Context, id string, label, notes *string) (model.Run, error) {
	if !isUUID(id) {
		return model.Run{}, ErrNotFound
	}
	var rr sqliteRunRow
	err := s.w.QueryRowContext(ctx, `UPDATE runs SET label = COALESCE(?, label), notes = COALESCE(?, notes)
		WHERE id = ? RETURNING `+sqliteRunCols(""), label, notes, id).Scan(rr.dest()...)
	if err != nil {
		return model.Run{}, sqliteErr(err)
	}
	out, _, err := rr.run()
	return out, err
}

// DeleteRun removes a run; metrics and error samples cascade and a test
// baseline pointing at it is set to NULL by the foreign key.
func (s *SQLite) DeleteRun(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, `DELETE FROM runs WHERE id = ?`, id)
}

// SetBaseline sets or clears a test's baseline run.
func (s *SQLite) SetBaseline(ctx context.Context, testID, runID string) (model.Test, error) {
	if !isUUID(testID) {
		return model.Test{}, ErrNotFound
	}
	if runID != "" && !isUUID(runID) {
		return model.Test{}, ErrNotFound
	}
	var out model.Test
	err := sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		var tid string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tests WHERE id = ?`, testID).Scan(&tid); err != nil {
			return sqliteErr(err)
		}
		var baseline any // nil -> NULL
		if runID != "" {
			var owner string
			if err := tx.QueryRowContext(ctx, `SELECT test_id FROM runs WHERE id = ?`, runID).Scan(&owner); err != nil {
				return sqliteErr(err)
			}
			if owner != testID {
				return fmt.Errorf("%w: run belongs to another test", ErrConflict)
			}
			baseline = runID
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tests SET baseline_run_id = ? WHERE id = ?`, baseline, testID); err != nil {
			return sqliteErr(err)
		}
		var err error
		out, err = s.getTest(ctx, tx, testID)
		return err
	})
	if err != nil {
		return model.Test{}, err
	}
	return out, nil
}

// RunsSince returns runs started at or after since, newest first, with
// summaries stripped of by_step.
func (s *SQLite) RunsSince(ctx context.Context, since time.Time) ([]model.Run, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+sqliteRunColsSlim("r")+` FROM runs r WHERE r.started_at >= ?
		ORDER BY r.started_at DESC, r.id DESC`, toMicros(since))
	if err != nil {
		return nil, sqliteErr(err)
	}
	return scanSQLiteRuns(rows)
}

// ---- metrics & error samples ------------------------------------------------

// AppendMetric stores one per-second point and its encoded snapshot. A
// point with the same T as an existing one replaces it. An unknown run
// yields ErrNotFound.
func (s *SQLite) AppendMetric(ctx context.Context, runID string, pt model.MetricPoint, snapshot []byte) error {
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
	_, err = s.w.ExecContext(ctx, `INSERT INTO run_metrics (run_id, t, point, snapshot) VALUES (?, ?, ?, ?)
		ON CONFLICT (run_id, t) DO UPDATE SET point = excluded.point, snapshot = excluded.snapshot`,
		runID, toMicros(pt.T), string(pj), snapshot)
	return sqliteErr(err)
}

// ListMetrics returns a run's points ordered by T (empty for unknown runs).
func (s *SQLite) ListMetrics(ctx context.Context, runID string) ([]model.MetricPoint, error) {
	pts, _, err := s.listMetrics(ctx, runID, false)
	return pts, err
}

// ListSnapshots returns a run's encoded snapshots ordered by T (one per
// stored point, empty for unknown runs).
func (s *SQLite) ListSnapshots(ctx context.Context, runID string) ([][]byte, error) {
	out := [][]byte{}
	if !isUUID(runID) {
		return out, nil
	}
	rows, err := s.r.QueryContext(ctx, `SELECT snapshot FROM run_metrics WHERE run_id = ? ORDER BY t`, runID)
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, sqliteErr(err)
		}
		if b == nil {
			b = []byte{}
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, sqliteErr(err)
	}
	return out, nil
}

// ListMetricsWithSnapshots returns a run's points and snapshots, aligned.
func (s *SQLite) ListMetricsWithSnapshots(ctx context.Context, runID string) ([]model.MetricPoint, [][]byte, error) {
	return s.listMetrics(ctx, runID, true)
}

func (s *SQLite) listMetrics(ctx context.Context, runID string, withSnaps bool) ([]model.MetricPoint, [][]byte, error) {
	pts, snaps := []model.MetricPoint{}, [][]byte{}
	if !isUUID(runID) {
		return pts, snaps, nil
	}
	q := `SELECT t, point FROM run_metrics WHERE run_id = ? ORDER BY t`
	if withSnaps {
		q = `SELECT t, point, snapshot FROM run_metrics WHERE run_id = ? ORDER BY t`
	}
	rows, err := s.r.QueryContext(ctx, q, runID)
	if err != nil {
		return nil, nil, sqliteErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			t  int64
			pj string
			b  []byte
			pt model.MetricPoint
		)
		dest := []any{&t, &pj}
		if withSnaps {
			dest = append(dest, &b)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, sqliteErr(err)
		}
		if err := json.Unmarshal([]byte(pj), &pt); err != nil {
			return nil, nil, fmt.Errorf("store: decode metric point: %w", err)
		}
		pt.T = fromMicros(t)
		pts = append(pts, pt)
		if withSnaps {
			if b == nil {
				b = []byte{}
			}
			snaps = append(snaps, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, sqliteErr(err)
	}
	return pts, snaps, nil
}

// AppendErrorSamples stores error samples for a run in one transaction.
// An empty slice is a no-op; an unknown run yields ErrNotFound.
func (s *SQLite) AppendErrorSamples(ctx context.Context, runID string, samples []model.ErrorSample) error {
	if len(samples) == 0 {
		return nil
	}
	if !isUUID(runID) {
		return ErrNotFound
	}
	return sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO run_errors (run_id, t, step, status, message) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return sqliteErr(err)
		}
		defer stmt.Close()
		for _, e := range samples {
			if _, err := stmt.ExecContext(ctx, runID, toMicros(e.T), e.Step, e.Status, e.Message); err != nil {
				return sqliteErr(err)
			}
		}
		return nil
	})
}

// ListErrorSamples returns a run's error samples ordered by T, then
// insertion order (empty for unknown runs).
func (s *SQLite) ListErrorSamples(ctx context.Context, runID string) ([]model.ErrorSample, error) {
	out := []model.ErrorSample{}
	if !isUUID(runID) {
		return out, nil
	}
	rows, err := s.r.QueryContext(ctx, `SELECT t, step, status, message FROM run_errors
		WHERE run_id = ? ORDER BY t, id`, runID)
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e model.ErrorSample
			t int64
		)
		if err := rows.Scan(&t, &e.Step, &e.Status, &e.Message); err != nil {
			return nil, sqliteErr(err)
		}
		e.T = fromMicros(t)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, sqliteErr(err)
	}
	return out, nil
}

// SetErrorGroups replaces a run's error groups (stored as JSON).
func (s *SQLite) SetErrorGroups(ctx context.Context, runID string, groups []model.ErrorGroup) error {
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
	return s.execOne(ctx, `UPDATE runs SET error_groups = ? WHERE id = ?`, string(b), runID)
}

// ListErrorGroups returns a run's stored error groups.
func (s *SQLite) ListErrorGroups(ctx context.Context, runID string) ([]model.ErrorGroup, error) {
	if !isUUID(runID) {
		return nil, ErrNotFound
	}
	var b string
	if err := s.r.QueryRowContext(ctx, `SELECT COALESCE(error_groups, '[]') FROM runs WHERE id = ?`, runID).
		Scan(&b); err != nil {
		return nil, sqliteErr(err)
	}
	out := []model.ErrorGroup{}
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		return nil, fmt.Errorf("store: decode error groups: %w", err)
	}
	if out == nil {
		out = []model.ErrorGroup{}
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
