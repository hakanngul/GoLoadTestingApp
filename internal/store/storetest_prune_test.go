package store

import (
	"slices"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// testPruneRuns checks the retention rules of Pruner.PruneRuns: only
// finished runs older than the cutoff go (by finished_at, else
// started_at), with their children; baselines, each test's latest
// finished run and active runs stay.
func testPruneRuns(t *testing.T, s Store) {
	p, ok := s.(Pruner)
	if !ok {
		t.Fatalf("%T does not implement Pruner", s)
	}
	c := ctx(t)
	cutoff := base.Add(100 * time.Hour)
	at := func(h time.Duration) time.Time { return base.Add(h) }
	run := func(testID string, st model.RunStatus, started time.Time, finished *time.Time) model.Run {
		return mustRun(t, s, model.Run{TestID: testID, Status: st, StartedAt: started, FinishedAt: finished})
	}
	fin := func(tm time.Time) *time.Time { return &tm }

	a := mustTest(t, s, "prune-a")
	b := mustTest(t, s, "prune-b")
	cc := mustTest(t, s, "prune-c")
	a1 := run(a.ID, model.RunPassed, at(0), fin(at(time.Minute)))                // old -> pruned
	a2 := run(a.ID, model.RunFailed, at(time.Hour), fin(at(61*time.Minute)))     // old -> pruned (with children)
	a3 := run(a.ID, model.RunStopped, at(2*time.Hour), fin(at(121*time.Minute))) // baseline -> kept
	a4 := run(a.ID, model.RunError, at(3*time.Hour), nil)                        // no finished_at, old start -> pruned
	a5 := run(a.ID, model.RunPassed, at(4*time.Hour), fin(at(200*time.Hour)))    // finished after cutoff -> kept
	a9 := run(a.ID, model.RunPassed, at(5*time.Hour), fin(at(5*time.Hour)))      // latest finished of A -> kept
	a6 := run(a.ID, model.RunRunning, at(time.Hour), nil)                        // active -> kept
	a7 := run(a.ID, model.RunQueued, at(6*time.Hour), nil)                       // active, newest -> kept
	b1 := run(b.ID, model.RunPassed, at(0), fin(at(time.Minute)))                // only run of B -> kept
	c1 := run(cc.ID, model.RunFailed, at(0), fin(at(time.Minute)))               // old -> pruned
	c2 := run(cc.ID, model.RunPassed, at(300*time.Hour), fin(at(301*time.Hour))) // recent -> kept

	if err := s.AppendMetric(c, a2.ID, model.MetricPoint{T: at(time.Hour)}, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendErrorSamples(c, a2.ID, []model.ErrorSample{{T: at(time.Hour), Message: "e"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetErrorGroups(c, a2.ID, []model.ErrorGroup{{Message: "e", Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMetric(c, a9.ID, model.MetricPoint{T: at(5 * time.Hour)}, []byte{3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBaseline(c, a.ID, a3.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(rs []PrunedRun) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.ID
		}
		return out
	}
	wantIDs := []string{a1.ID, c1.ID, a2.ID, a4.ID}
	if a1.ID > c1.ID { // same StartedAt: ordered by ID
		wantIDs[0], wantIDs[1] = c1.ID, a1.ID
	}
	kept := []model.Run{a3, a5, a9, a6, a7, b1, c2}

	// Dry run: reports, deletes nothing.
	got, err := p.PruneRuns(c, cutoff, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(got), wantIDs) {
		t.Fatalf("dry run = %v, want %v", ids(got), wantIDs)
	}
	for _, r := range got {
		if r.ID == a2.ID {
			want := PrunedRun{ID: a2.ID, TestID: a.ID, TestName: "prune-a", Status: model.RunFailed,
				StartedAt: at(time.Hour), FinishedAt: at(61 * time.Minute)}
			if r != want {
				t.Fatalf("dry run entry = %+v, want %+v", r, want)
			}
		}
		if r.ID == a4.ID && !r.FinishedAt.IsZero() {
			t.Fatalf("a4 FinishedAt = %v, want zero", r.FinishedAt)
		}
	}
	if _, err := s.GetRun(c, a1.ID); err != nil {
		t.Fatalf("dry run deleted a run: %v", err)
	}

	// Real prune.
	got, err = p.PruneRuns(c, cutoff, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(got), wantIDs) {
		t.Fatalf("prune = %v, want %v", ids(got), wantIDs)
	}
	for _, id := range wantIDs {
		_, err := s.GetRun(c, id)
		wantErr(t, "GetRun pruned", err, ErrNotFound)
	}
	for _, r := range kept {
		if _, err := s.GetRun(c, r.ID); err != nil {
			t.Fatalf("kept run %s (%s): %v", r.ID, r.Status, err)
		}
	}
	if ms, _ := s.ListMetrics(c, a2.ID); len(ms) != 0 {
		t.Fatalf("metrics survived: %+v", ms)
	}
	if es, _ := s.ListErrorSamples(c, a2.ID); len(es) != 0 {
		t.Fatalf("error samples survived: %+v", es)
	}
	_, err = s.ListErrorGroups(c, a2.ID)
	wantErr(t, "ListErrorGroups pruned", err, ErrNotFound)
	if ms, _ := s.ListMetrics(c, a9.ID); len(ms) != 1 {
		t.Fatalf("kept run metrics = %d, want 1", len(ms))
	}
	if gt, _ := s.GetTest(c, a.ID); gt.BaselineRunID != a3.ID {
		t.Fatalf("baseline = %q, want %q", gt.BaselineRunID, a3.ID)
	}

	// Idempotent.
	if got, err = p.PruneRuns(c, cutoff, false); err != nil || len(got) != 0 {
		t.Fatalf("second prune = %v, %v; want none", ids(got), err)
	}

	// Once no longer the baseline, the old run goes too; the latest
	// finished run of every test still stays even with a cutoff in the
	// far future.
	if _, err := s.SetBaseline(c, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, err = p.PruneRuns(c, base.Add(1000*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{a3.ID, a5.ID}; !slices.Equal(ids(got), want) {
		t.Fatalf("prune after baseline cleared = %v, want %v", ids(got), want)
	}
	runs, err := s.ListRuns(c, RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	left := make([]string, 0, len(runs))
	for _, r := range runs {
		left = append(left, r.ID)
	}
	slices.Sort(left)
	wantLeft := []string{a9.ID, a6.ID, a7.ID, b1.ID, c2.ID}
	slices.Sort(wantLeft)
	if !slices.Equal(left, wantLeft) {
		t.Fatalf("runs left = %v, want %v", left, wantLeft)
	}
	tests, err := s.ListTests(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		if tt.LastRun == nil {
			t.Fatalf("test %s lost its last run", tt.Name)
		}
	}
}
