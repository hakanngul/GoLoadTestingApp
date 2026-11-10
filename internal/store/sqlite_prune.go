package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// Data retention for the SQLite store.
//
// Space reclaim: SQLite never shrinks a database file on DELETE; freed
// pages go to a freelist and are reused by later inserts. To give disk
// space back the store uses incremental auto-vacuum:
//
//   - A new database file gets PRAGMA auto_vacuum=INCREMENTAL before its
//     first table is created (first pragma of the writer DSN; free).
//   - A file created before that (auto_vacuum=NONE) can only switch mode
//     through a full VACUUM, which rewrites the whole file. That is not
//     done at open, so users who never prune never pay for it: the first
//     prune that deletes something runs it once (after the delete, so the
//     file it rewrites is already small) and every later prune runs
//     PRAGMA incremental_vacuum in 16 MiB steps instead. The VACUUM holds
//     the write connection for its duration (an active run's metric
//     inserts wait) and needs free disk space for a copy of the
//     post-prune database. Measured on 50 runs x 600 points (119 MiB,
//     45 runs pruned to 12 MiB): delete 0.17 s; one-time VACUUM 0.21 s;
//     incremental vacuum of the same 108 MiB 1.07 s.

// sqlitePruneChunk bounds how many runs one delete transaction removes,
// so a large prune does not hold the write lock (and stall an active
// run's metric inserts) for long.
const sqlitePruneChunk = 50

// sqliteVacuumStep is how many free pages one incremental_vacuum step
// returns to the file system (16 MiB with the default 4 KiB pages).
const sqliteVacuumStep = 4096

// sqlitePrunable is the WHERE condition (alias r for runs) selecting
// prunable runs; its single parameter is the cutoff in microseconds.
const sqlitePrunable = `r.status NOT IN ('queued', 'running')
	AND COALESCE(r.finished_at, r.started_at) < ?
	AND NOT EXISTS (SELECT 1 FROM tests t WHERE t.baseline_run_id = r.id)
	AND r.id <> (SELECT l.id FROM runs l
		WHERE l.test_id = r.test_id AND l.status NOT IN ('queued', 'running')
		ORDER BY l.started_at DESC, l.id DESC LIMIT 1)`

// sqlitePruneCandidatesSQL lists the prunable runs, oldest first; its
// single parameter is the cutoff in microseconds.
const sqlitePruneCandidatesSQL = `SELECT r.id, r.test_id, r.test_name, r.status, r.started_at, r.finished_at
		FROM runs r WHERE r.status NOT IN ('queued', 'running')
	AND COALESCE(r.finished_at, r.started_at) < ?
	AND NOT EXISTS (SELECT 1 FROM tests t WHERE t.baseline_run_id = r.id)
	AND r.id <> (SELECT l.id FROM runs l
		WHERE l.test_id = r.test_id AND l.status NOT IN ('queued', 'running')
		ORDER BY l.started_at DESC, l.id DESC LIMIT 1) ORDER BY r.started_at, r.id`

// sqlitePruneDeleteSQL deletes one run if it is still prunable; its
// parameters are the run ID and the cutoff in microseconds.
const sqlitePruneDeleteSQL = `DELETE FROM runs AS r WHERE r.id = ? AND r.status NOT IN ('queued', 'running')
	AND COALESCE(r.finished_at, r.started_at) < ?
	AND NOT EXISTS (SELECT 1 FROM tests t WHERE t.baseline_run_id = r.id)
	AND r.id <> (SELECT l.id FROM runs l
		WHERE l.test_id = r.test_id AND l.status NOT IN ('queued', 'running')
		ORDER BY l.started_at DESC, l.id DESC LIMIT 1) RETURNING id`

var _ Pruner = (*SQLite)(nil)

// PruneRuns deletes old finished runs except baselines and each test's
// latest finished run (see Pruner), then reclaims the freed space.
func (s *SQLite) PruneRuns(ctx context.Context, olderThan time.Time, dryRun bool) ([]PrunedRun, error) {
	cutoff := toMicros(olderThan)
	rows, err := s.r.QueryContext(ctx, sqlitePruneCandidatesSQL, cutoff)
	if err != nil {
		return nil, sqliteErr(err)
	}
	var cands []PrunedRun
	for rows.Next() {
		var p PrunedRun
		var status string
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&p.ID, &p.TestID, &p.TestName, &status, &started, &finished); err != nil {
			rows.Close()
			return nil, sqliteErr(err)
		}
		p.Status = model.RunStatus(status)
		p.StartedAt = fromMicros(started)
		if finished.Valid {
			p.FinishedAt = fromMicros(finished.Int64)
		}
		cands = append(cands, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, sqliteErr(err)
	}
	rows.Close()
	if dryRun || len(cands) == 0 {
		return cands, nil
	}

	// Delete in chunks (one transaction each), re-checking the condition
	// per run: a run may have become a baseline since it was selected.
	deleted := make(map[string]bool, len(cands))
	for start := 0; start < len(cands); start += sqlitePruneChunk {
		chunk := cands[start:min(start+sqlitePruneChunk, len(cands))]
		// Only report a chunk's deletions once its transaction commits; a
		// rollback leaves those runs in place.
		var done []string
		err := sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
			done = done[:0]
			stmt, err := tx.PrepareContext(ctx, sqlitePruneDeleteSQL)
			if err != nil {
				return sqliteErr(err)
			}
			defer stmt.Close()
			for _, p := range chunk {
				ok, err := sqlitePruneOne(ctx, stmt, p.ID, cutoff)
				if err != nil {
					return err
				}
				if ok {
					done = append(done, p.ID)
				}
			}
			return nil
		})
		if err != nil {
			return pickPruned(cands, deleted), err
		}
		for _, id := range done {
			deleted[id] = true
		}
	}
	out := pickPruned(cands, deleted)
	if len(out) > 0 {
		if err := s.reclaim(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

// sqlitePruneOne runs the prepared sqlitePruneDeleteSQL for one run and
// reports whether it deleted it.
func sqlitePruneOne(ctx context.Context, stmt *sql.Stmt, id string, cutoff int64) (bool, error) {
	rows, err := stmt.QueryContext(ctx, id, cutoff)
	if err != nil {
		return false, sqliteErr(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
	}
	return found, sqliteErr(rows.Err())
}

// pickPruned keeps the candidates that were actually deleted.
func pickPruned(cands []PrunedRun, deleted map[string]bool) []PrunedRun {
	out := make([]PrunedRun, 0, len(deleted))
	for _, p := range cands {
		if deleted[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// reclaim returns free pages to the file system: a one-time full VACUUM
// that switches an old database to incremental auto-vacuum, else an
// incremental vacuum; then a WAL checkpoint so the -wal file shrinks too.
func (s *SQLite) reclaim(ctx context.Context) error {
	if s.memory {
		return nil
	}
	var mode int
	if err := s.w.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return fmt.Errorf("store: sqlite: reclaim space: %w", err)
	}
	if mode != 2 { // 0 NONE, 1 FULL, 2 INCREMENTAL
		if _, err := s.w.ExecContext(ctx, `PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			return fmt.Errorf("store: sqlite: reclaim space: %w", err)
		}
		if _, err := s.w.ExecContext(ctx, `VACUUM`); err != nil {
			return fmt.Errorf("store: sqlite: reclaim space: vacuum: %w", err)
		}
	} else {
		// In steps, each its own write transaction, so an active run's
		// metric inserts can interleave with a large reclaim.
		for {
			var free int64
			if err := s.w.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
				return fmt.Errorf("store: sqlite: reclaim space: %w", err)
			}
			if free == 0 {
				break
			}
			q := fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, sqliteVacuumStep)
			if err := drainPragma(ctx, s.w, q); err != nil {
				return fmt.Errorf("store: sqlite: reclaim space: incremental_vacuum: %w", err)
			}
		}
	}
	// Best effort: a reader holding an old snapshot can keep part of the
	// WAL alive until the next checkpoint.
	_, _ = s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return nil
}

// drainPragma runs a pragma that does its work one row per step (such as
// incremental_vacuum, which frees one page per step) to completion.
func drainPragma(ctx context.Context, db *sql.DB, q string) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}
