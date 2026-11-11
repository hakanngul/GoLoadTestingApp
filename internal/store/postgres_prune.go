package store

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// Data retention for the PostgreSQL store. Space freed by the DELETE is
// reclaimed by PostgreSQL's autovacuum (reused for new rows; the files
// themselves only shrink with VACUUM FULL, which is left to the operator).

// pgPrunable is the WHERE condition (alias r for runs) selecting
// prunable runs; $1 is the cutoff.
const pgPrunable = `r.status NOT IN ('queued', 'running') AND NOT r.pinned
	AND COALESCE(r.finished_at, r.started_at) < $1
	AND NOT EXISTS (SELECT 1 FROM tests t WHERE t.baseline_run_id = r.id)
	AND r.id <> (SELECT l.id FROM runs l
		WHERE l.test_id = r.test_id AND l.status NOT IN ('queued', 'running')
		ORDER BY l.started_at DESC, l.id DESC LIMIT 1)`

var _ Pruner = (*Postgres)(nil)

// PruneRuns deletes old finished runs except pinned runs, baselines and each test's
// latest finished run (see Pruner) in one statement; metrics, error
// samples and error groups cascade.
func (p *Postgres) PruneRuns(ctx context.Context, olderThan time.Time, dryRun bool) ([]PrunedRun, error) {
	const cols = `r.id::text, r.test_id::text, r.test_name, r.status, r.started_at, r.finished_at`
	q := `SELECT ` + cols + ` FROM runs r WHERE ` + pgPrunable
	if !dryRun {
		q = `DELETE FROM runs r WHERE ` + pgPrunable + ` RETURNING ` + cols
	}
	rows, err := p.pool.Query(ctx, q, normTime(olderThan))
	if err != nil {
		return nil, mapErr(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PrunedRun, error) {
		var r PrunedRun
		var status string
		var finished *time.Time
		if err := row.Scan(&r.ID, &r.TestID, &r.TestName, &status, &r.StartedAt, &finished); err != nil {
			return r, err
		}
		r.Status = model.RunStatus(status)
		r.StartedAt = r.StartedAt.UTC()
		if finished != nil {
			r.FinishedAt = finished.UTC()
		}
		return r, nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	slices.SortFunc(out, comparePruned)
	return out, nil
}
