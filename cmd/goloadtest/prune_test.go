package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

func TestParseRetention(t *testing.T) {
	ok := map[string]time.Duration{
		"":      0,
		"0":     0,
		" 90d ": 90 * 24 * time.Hour,
		"720h":  720 * time.Hour,
		"1d12h": 36 * time.Hour,
		"1h":    time.Hour,
		"90m":   90 * time.Minute,
	}
	for in, want := range ok {
		got, err := parseRetention(in)
		if err != nil || got != want {
			t.Errorf("parseRetention(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"59m", "0d", "-1h", "-2d", "1.5d", "d", "90", "abc", "1d-1h", "30s"} {
		if got, err := parseRetention(in); err == nil {
			t.Errorf("parseRetention(%q) = %v, want error", in, got)
		}
	}
	if got := fmtRetention(90 * 24 * time.Hour); got != "90d" {
		t.Errorf("fmtRetention = %q", got)
	}
	if got := fmtRetention(36 * time.Hour); got != "36h0m0s" {
		t.Errorf("fmtRetention = %q", got)
	}
}

// seedPruneDB creates a SQLite file with one test holding an old run, a
// baseline old run and a latest (old) run; returns the db spec and the
// old run's ID.
func seedPruneDB(t *testing.T) (dbSpec, oldID string) {
	t.Helper()
	c := context.Background()
	path := filepath.Join(t.TempDir(), "goload.db")
	s, err := store.NewSQLite(c, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tt, err := s.CreateTest(c, model.Test{Name: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-60 * 24 * time.Hour)
	var ids []string
	for i := range 3 {
		st := old.Add(time.Duration(i) * time.Hour)
		r, err := s.CreateRun(c, model.Run{TestID: tt.ID, Status: model.RunPassed, StartedAt: st, FinishedAt: &st})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AppendMetric(c, r.ID, model.MetricPoint{T: st}, []byte{1}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if _, err := s.SetBaseline(c, tt.ID, ids[1]); err != nil {
		t.Fatal(err)
	}
	return "sqlite:" + path, ids[0]
}

func TestCmdPrune(t *testing.T) {
	db, oldID := seedPruneDB(t)
	path := strings.TrimPrefix(db, "sqlite:")

	var out, errb bytes.Buffer
	if code := dispatch([]string{"prune", "-db", db, "-older-than", "30d", "-dry-run"}, &out, &errb); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), oldID) || !strings.Contains(out.String(), "would delete 1 run(s) older than 30d") {
		t.Fatalf("dry run output:\n%s", out.String())
	}

	out.Reset()
	if code := dispatch([]string{"prune", "-db", db, "-older-than", "30d"}, &out, &errb); code != exitOK {
		t.Fatalf("prune exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), oldID) || !strings.Contains(out.String(), "deleted 1 run(s)") {
		t.Fatalf("prune output:\n%s", out.String())
	}
	s, err := store.NewSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runs, err := s.ListRuns(context.Background(), store.RunFilter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs left = %d, %v; want 2 (baseline + latest)", len(runs), err)
	}

	out.Reset()
	if code := dispatch([]string{"prune", "-db", db, "-older-than", "30d"}, &out, &errb); code != exitOK || !strings.Contains(out.String(), "deleted 0 run(s)") {
		t.Fatalf("second prune exit %d:\n%s%s", code, out.String(), errb.String())
	}
}

func TestCmdPruneUsage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.db")
	for _, args := range [][]string{
		{"prune"},
		{"prune", "-older-than", "10m"},
		{"prune", "-older-than", "0"},
		{"prune", "-db", "memory", "-older-than", "30d"},
		{"prune", "-db", "sqlite:" + missing, "-older-than", "30d"},
		{"prune", "-older-than", "30d", "extra"},
	} {
		if code := dispatch(args, io.Discard, io.Discard); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestServeRejectsBadRetention(t *testing.T) {
	var errb bytes.Buffer
	if code := dispatch([]string{"serve", "-db", "memory", "-retention", "30m"}, io.Discard, &errb); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errb.String(), "-retention") {
		t.Fatalf("stderr = %q", errb.String())
	}
}

// TestStartRetention checks the serve loop prunes at start and stops
// when its context is cancelled.
func TestStartRetention(t *testing.T) {
	db, oldID := seedPruneDB(t)
	st, err := store.NewSQLite(context.Background(), strings.TrimPrefix(db, "sqlite:"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	wait, err := startRetention(ctx, st, 30*24*time.Hour, log)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), "retention: deleted old runs") {
		if time.Now().After(deadline) {
			t.Fatalf("old run not pruned at start; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	wait()
	if !strings.Contains(logs.String(), "count=1") {
		t.Fatalf("logs:\n%s", logs.String())
	}
	if _, err := st.GetRun(context.Background(), oldID); err == nil {
		t.Fatal("old run still present")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
