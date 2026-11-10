package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func sqliteAutoVacuum(t *testing.T, s *SQLite) int {
	t.Helper()
	var mode int
	if err := s.w.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// fillRuns adds n finished runs of one test, each with points metric
// points carrying a 2 KiB snapshot, all finished at base.
func fillRuns(t *testing.T, s *SQLite, testID string, n, points int) {
	t.Helper()
	c := context.Background()
	snap := make([]byte, 2048)
	for i := range n {
		fin := base.Add(time.Duration(i) * time.Second)
		r, err := s.CreateRun(c, model.Run{TestID: testID, Status: model.RunPassed, StartedAt: fin, FinishedAt: &fin})
		if err != nil {
			t.Fatal(err)
		}
		for j := range points {
			if err := s.AppendMetric(c, r.ID, model.MetricPoint{T: fin.Add(time.Duration(j) * time.Second)}, snap); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestSQLiteNewFileUsesIncrementalAutoVacuum(t *testing.T) {
	s, _ := newSQLiteTestStore(t)
	if got := sqliteAutoVacuum(t, s); got != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (INCREMENTAL)", got)
	}
}

// TestSQLitePruneReclaimsSpace checks that a prune shrinks the file: the
// first one on a database created without auto-vacuum converts it with a
// full VACUUM, later ones use incremental_vacuum.
func TestSQLitePruneReclaimsSpace(t *testing.T) {
	c := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	// A pre-existing database: tables created before auto_vacuum was set.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE legacy (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := NewSQLite(c, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := sqliteAutoVacuum(t, s); got != 0 {
		t.Fatalf("legacy auto_vacuum = %d, want 0", got)
	}
	tt, err := s.CreateTest(c, model.Test{Name: "big"})
	if err != nil {
		t.Fatal(err)
	}

	for round := range 2 {
		fillRuns(t, s, tt.ID, 10, 100)
		if _, err := s.w.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		before := fileSize(t, path)
		pruned, err := s.PruneRuns(c, base.Add(time.Hour), false)
		if err != nil {
			t.Fatal(err)
		}
		if want := 9 + round; len(pruned) != want { // round 1 also drops round 0's kept latest run
			t.Fatalf("round %d: pruned %d runs, want %d", round, len(pruned), want)
		}
		after := fileSize(t, path)
		if after >= before/2 {
			t.Fatalf("round %d: file %d -> %d bytes, want it at least halved", round, before, after)
		}
		if got := sqliteAutoVacuum(t, s); got != 2 {
			t.Fatalf("round %d: auto_vacuum = %d, want 2", round, got)
		}
		t.Logf("round %d: %d -> %d bytes", round, before, after)
	}
}
