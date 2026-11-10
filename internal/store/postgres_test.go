package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// testDSNEnv names the environment variable holding a PostgreSQL DSN for
// the Postgres conformance tests; they are skipped when it is unset.
const testDSNEnv = "GOLOAD_TEST_DSN"

// newPostgresTestStore opens a Postgres store in a fresh, randomly named
// schema that is dropped when the test ends.
func newPostgresTestStore(t *testing.T, dsn string) *Postgres {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema := "goload_test_" + strings.ReplaceAll(newID(), "-", "")
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	s, err := NewPostgres(ctx, withSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(s.Close) // registered last, so it runs before the schema drop
	return s
}

// withSearchPath adds a search_path runtime parameter to dsn (URL or
// keyword/value form); pgx sends unknown DSN keys as runtime parameters.
func withSearchPath(t *testing.T, dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse dsn: %v", err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema
}

func postgresDSN(t *testing.T) string {
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping PostgreSQL tests", testDSNEnv)
	}
	return dsn
}

func TestPostgres(t *testing.T) {
	dsn := postgresDSN(t)
	runStoreSuite(t, func(t *testing.T) Store { return newPostgresTestStore(t, dsn) })
}

// TestPostgresMigrations checks that migrations are recorded, idempotent
// on reopen, and safe when several masters open the same schema at once.
func TestPostgresMigrations(t *testing.T) {
	dsn := postgresDSN(t)
	first := newPostgresTestStore(t, dsn)
	ctx := context.Background()

	var schema string
	if err := first.pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "goload_test_") {
		t.Fatalf("current_schema = %q; search_path not applied", schema)
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateTest(ctx, model.Test{Name: "kept"}); err != nil {
		t.Fatal(err)
	}

	// Reopen concurrently against the already-migrated schema.
	scoped := withSearchPath(t, dsn, schema)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := NewPostgres(ctx, scoped)
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

	var n, maxV int
	if err := first.pool.QueryRow(ctx, "SELECT count(*), max(version) FROM schema_migrations").Scan(&n, &maxV); err != nil {
		t.Fatal(err)
	}
	if n != len(migs) || maxV != migs[len(migs)-1].version {
		t.Fatalf("schema_migrations: %d rows, max %d; want %d rows", n, maxV, len(migs))
	}
	if ts, err := first.ListTests(ctx); err != nil || len(ts) != 1 {
		t.Fatalf("data lost after reopen: %v %v", ts, err)
	}
}

// TestPostgresConcurrentFreshMigrate opens several stores on an empty
// schema at once; the advisory lock must make exactly one apply 0001.
func TestPostgresConcurrentFreshMigrate(t *testing.T) {
	dsn := postgresDSN(t)
	ctx := context.Background()
	schema := "goload_test_" + strings.ReplaceAll(newID(), "-", "")
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck

	scoped := withSearchPath(t, dsn, schema)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := NewPostgres(ctx, scoped)
			if err != nil {
				errs <- fmt.Errorf("open: %w", err)
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
}
