package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// newSQLiteTestStore opens a store on a fresh database file in a
// per-test temporary directory.
func newSQLiteTestStore(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goload.db")
	s, err := NewSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(s.Close)
	return s, path
}

func TestSQLite(t *testing.T) {
	runStoreSuite(t, func(t *testing.T) Store {
		s, _ := newSQLiteTestStore(t)
		return s
	})
}

func TestSQLiteInMemory(t *testing.T) {
	runStoreSuite(t, func(t *testing.T) Store {
		s, err := NewSQLite(context.Background(), ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	})
}

// TestSQLiteReopen checks that data survives closing and reopening the
// file, that migrations are recorded once and are idempotent, and that
// several stores can open an already-migrated file at once.
func TestSQLiteReopen(t *testing.T) {
	c := context.Background()
	path := filepath.Join(t.TempDir(), "goload.db")
	s, err := NewSQLite(c, path)
	if err != nil {
		t.Fatal(err)
	}
	tt, err := s.CreateTest(c, model.Test{Name: "kept", YAML: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile(c, tt.ID, "users.csv", []byte{0, 1, 2, 0xff}); err != nil {
		t.Fatal(err)
	}
	run := fullRun(tt.ID)
	run.Label, run.Notes = "v1", "n"
	r, err := s.CreateRun(c, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMetric(c, r.ID, model.MetricPoint{T: base, Requests: 7}, []byte{9, 8}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendErrorSamples(c, r.ID, []model.ErrorSample{{T: base, Step: "s", Status: 500, Message: "m"}}); err != nil {
		t.Fatal(err)
	}
	groups := []model.ErrorGroup{{Step: "s", Status: 500, Message: "m", Count: 3, First: base, Last: base.Add(time.Second)}}
	if err := s.SetErrorGroups(c, r.ID, groups); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBaseline(c, tt.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	wantTest, _ := s.GetTest(c, tt.ID)
	wantRun, _ := s.GetRun(c, r.ID)
	s.Close()

	// Reopen several times, some concurrently: migrations must not be
	// re-applied and nothing may be lost.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := NewSQLite(c, path)
			if err != nil {
				errs <- err
				return
			}
			s.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	s, err = NewSQLite(c, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	migs, err := loadMigrationsFrom(sqliteMigrationFS, "migrations_sqlite")
	if err != nil {
		t.Fatal(err)
	}
	var n, maxV int
	if err := s.r.QueryRowContext(c, `SELECT count(*), max(version) FROM schema_migrations`).Scan(&n, &maxV); err != nil {
		t.Fatal(err)
	}
	if n != len(migs) || maxV != migs[len(migs)-1].version {
		t.Fatalf("schema_migrations: %d rows, max %d; want %d rows", n, maxV, len(migs))
	}
	var mode string
	if err := s.r.QueryRowContext(c, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}

	gotTest, err := s.GetTest(c, tt.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "test after reopen", gotTest, wantTest)
	gotRun, err := s.GetRun(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "run after reopen", gotRun, wantRun)
	if f, _ := s.GetFile(c, tt.ID, "users.csv"); string(f) != string([]byte{0, 1, 2, 0xff}) {
		t.Fatalf("file after reopen = %v", f)
	}
	pts, snaps, err := s.ListMetricsWithSnapshots(c, r.ID)
	if err != nil || len(pts) != 1 || pts[0].Requests != 7 || !pts[0].T.Equal(base) || string(snaps[0]) != string([]byte{9, 8}) {
		t.Fatalf("metrics after reopen = %+v %v %v", pts, snaps, err)
	}
	es, _ := s.ListErrorSamples(c, r.ID)
	if len(es) != 1 || es[0].Message != "m" {
		t.Fatalf("error samples after reopen = %+v", es)
	}
	gg, err := s.ListErrorGroups(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "error groups after reopen", gg, groups)

	// The read pool must not be able to write.
	if _, err := s.r.ExecContext(c, `DELETE FROM tests`); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
}

// TestSQLiteForeignKeys checks that foreign keys are enforced on every
// connection: writes for unknown parents fail with ErrNotFound.
func TestSQLiteForeignKeys(t *testing.T) {
	s, _ := newSQLiteTestStore(t)
	c := context.Background()
	if err := s.AppendMetric(c, missingID, model.MetricPoint{T: base}, nil); !isNotFound(err) {
		t.Fatalf("AppendMetric unknown run: %v", err)
	}
	if err := s.AppendErrorSamples(c, missingID, []model.ErrorSample{{T: base}}); !isNotFound(err) {
		t.Fatalf("AppendErrorSamples unknown run: %v", err)
	}
}

func isNotFound(err error) bool { return err != nil && errors.Is(err, ErrNotFound) }
