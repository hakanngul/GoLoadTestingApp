package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// runStoreSuite is the conformance suite every Store implementation must
// pass. newStore must return a fresh, empty store for each call; it is
// called once per subtest.
func runStoreSuite(t *testing.T, newStore func(t *testing.T) Store) {
	cases := []struct {
		name string
		fn   func(t *testing.T, s Store)
	}{
		{"TestCRUD", testTestCRUD},
		{"TestNotFound", testTestNotFound},
		{"TestConflict", testTestConflict},
		{"TestOrdering", testTestOrdering},
		{"Files", testFiles},
		{"RunCRUD", testRunCRUD},
		{"RunNotFound", testRunNotFound},
		{"RunEmptySlices", testRunEmptySlices},
		{"ListRuns", testListRuns},
		{"ListRunsDefaultLimit", testListRunsDefaultLimit},
		{"LastRun", testLastRun},
		{"CascadeDelete", testCascadeDelete},
		{"Metrics", testMetrics},
		{"ErrorSamples", testErrorSamples},
		{"ErrorGroups", testErrorGroups},
		{"MarkInterrupted", testMarkInterrupted},
		{"NoAliasing", testNoAliasing},
		{"Concurrent", testConcurrent},
		{"RunMeta", testRunMeta},
		{"DeleteRun", testDeleteRun},
		{"Baseline", testBaseline},
		{"MetricsWithSnapshots", testMetricsWithSnapshots},
		{"RunsSince", testRunsSince},
		{"PruneRuns", testPruneRuns},
		{"ScheduleCRUD", testScheduleCRUD},
		{"ScheduleAdvance", testScheduleAdvance},
		{"ScheduleCascade", testScheduleCascade},
		{"RunScheduleID", testRunScheduleID},
		{"SuiteCRUD", testSuiteCRUD},
		{"SuiteRuns", testSuiteRuns},
		{"NotificationChannels", testNotificationChannels},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			c.fn(t, s)
		})
	}
}

// ---- helpers ---------------------------------------------------------------

const missingID = "00000000-0000-4000-8000-000000000000"

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func mustTest(t *testing.T, s Store, name string) model.Test {
	t.Helper()
	tt, err := s.CreateTest(ctx(t), model.Test{Name: name, YAML: "name: " + name})
	if err != nil {
		t.Fatalf("CreateTest(%q): %v", name, err)
	}
	return tt
}

func mustRun(t *testing.T, s Store, r model.Run) model.Run {
	t.Helper()
	out, err := s.CreateRun(ctx(t), r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return out
}

func wantErr(t *testing.T, what string, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("%s: got error %v, want %v", what, got, want)
	}
}

// jsonEq compares two values by their JSON encoding, which is what the
// API exposes.
func jsonEq(t *testing.T, what string, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, w) {
		t.Fatalf("%s mismatch:\n got  %s\n want %s", what, g, w)
	}
}

// base is a fixed, microsecond-aligned UTC instant used for explicit
// timestamps.
var base = time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)

// ---- tests -----------------------------------------------------------------

func testTestCRUD(t *testing.T, s Store) {
	before := time.Now().Add(-time.Second)
	created := mustTest(t, s, "alpha")
	if !isUUID(created.ID) {
		t.Fatalf("ID %q is not a canonical UUID", created.ID)
	}
	if created.Name != "alpha" || created.YAML != "name: alpha" {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt.Before(before) || !created.UpdatedAt.Equal(created.CreatedAt) {
		t.Fatalf("timestamps not set: %+v", created)
	}
	if created.Files == nil || len(created.Files) != 0 {
		t.Fatalf("Files = %#v, want empty non-nil", created.Files)
	}

	got, err := s.GetTest(ctx(t), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "GetTest", got, created)

	// IDs are store-assigned even if the caller supplies one.
	other, err := s.CreateTest(ctx(t), model.Test{ID: created.ID, Name: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == created.ID {
		t.Fatal("CreateTest reused caller-supplied ID")
	}

	time.Sleep(2 * time.Millisecond)
	upd, err := s.UpdateTest(ctx(t), model.Test{ID: created.ID, Name: "alpha2", YAML: "y2"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "alpha2" || upd.YAML != "y2" || !upd.CreatedAt.Equal(created.CreatedAt) ||
		!upd.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("UpdateTest = %+v (created %+v)", upd, created)
	}
	got, err = s.GetTest(ctx(t), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "GetTest after update", got, upd)

	// The old name is free again.
	mustTest(t, s, "alpha")

	if err := s.DeleteTest(ctx(t), created.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetTest(ctx(t), created.ID)
	wantErr(t, "GetTest after delete", err, ErrNotFound)
	list, err := s.ListTests(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListTests len = %d, want 2", len(list))
	}
}

func testTestNotFound(t *testing.T, s Store) {
	list, err := s.ListTests(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("ListTests on empty store = %#v, want empty non-nil", list)
	}
	for _, id := range []string{missingID, "not-a-uuid", ""} {
		_, err := s.GetTest(ctx(t), id)
		wantErr(t, "GetTest("+id+")", err, ErrNotFound)
		_, err = s.UpdateTest(ctx(t), model.Test{ID: id, Name: "x"})
		wantErr(t, "UpdateTest("+id+")", err, ErrNotFound)
		wantErr(t, "DeleteTest("+id+")", s.DeleteTest(ctx(t), id), ErrNotFound)
	}
}

func testTestConflict(t *testing.T, s Store) {
	a := mustTest(t, s, "a")
	mustTest(t, s, "b")
	_, err := s.CreateTest(ctx(t), model.Test{Name: "a"})
	wantErr(t, "duplicate CreateTest", err, ErrConflict)
	_, err = s.UpdateTest(ctx(t), model.Test{ID: a.ID, Name: "b"})
	wantErr(t, "UpdateTest to taken name", err, ErrConflict)
	// Keeping one's own name is not a conflict.
	if _, err := s.UpdateTest(ctx(t), model.Test{ID: a.ID, Name: "a", YAML: "new"}); err != nil {
		t.Fatalf("UpdateTest same name: %v", err)
	}
	// A failed update must not have changed anything.
	got, err := s.GetTest(ctx(t), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "a" || got.YAML != "new" {
		t.Fatalf("got %+v", got)
	}
}

func testTestOrdering(t *testing.T, s Store) {
	var ids []string
	for _, n := range []string{"t1", "t2", "t3"} {
		ids = append(ids, mustTest(t, s, n).ID)
		time.Sleep(2 * time.Millisecond)
	}
	names := func() []string {
		list, err := s.ListTests(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range list {
			out = append(out, x.Name)
		}
		return out
	}
	jsonEq(t, "initial order", names(), []string{"t3", "t2", "t1"})
	if _, err := s.UpdateTest(ctx(t), model.Test{ID: ids[0], Name: "t1"}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "order after update", names(), []string{"t1", "t3", "t2"})
}

func testFiles(t *testing.T, s Store) {
	tt := mustTest(t, s, "files")
	c := ctx(t)

	wantErr(t, "PutFile unknown test", s.PutFile(c, missingID, "a.csv", []byte("x")), ErrNotFound)
	wantErr(t, "PutFile bad id", s.PutFile(c, "bad", "a.csv", []byte("x")), ErrNotFound)

	if err := s.PutFile(c, tt.ID, "users.csv", []byte("id\n1\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile(c, tt.ID, "a.csv", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile(c, tt.ID, "empty.csv", nil); err != nil {
		t.Fatal(err)
	}
	// Upsert replaces content and size.
	if err := s.PutFile(c, tt.ID, "users.csv", []byte("id\n1\n2\n3\n")); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFile(c, tt.ID, "users.csv")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "id\n1\n2\n3\n" {
		t.Fatalf("GetFile = %q", got)
	}
	empty, err := s.GetFile(c, tt.ID, "empty.csv")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty file = %#v, want empty non-nil", empty)
	}

	gt, err := s.GetTest(c, tt.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "Files", gt.Files, []model.FileInfo{
		{Name: "a.csv", Size: 5}, {Name: "empty.csv", Size: 0}, {Name: "users.csv", Size: 9},
	})
	list, err := s.ListTests(c)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "ListTests Files", list[0].Files, gt.Files)

	_, err = s.GetFile(c, tt.ID, "nope.csv")
	wantErr(t, "GetFile missing", err, ErrNotFound)
	_, err = s.GetFile(c, missingID, "a.csv")
	wantErr(t, "GetFile missing test", err, ErrNotFound)
	wantErr(t, "DeleteFile missing", s.DeleteFile(c, tt.ID, "nope.csv"), ErrNotFound)
	wantErr(t, "DeleteFile missing test", s.DeleteFile(c, missingID, "a.csv"), ErrNotFound)

	if err := s.DeleteFile(c, tt.ID, "a.csv"); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetFile(c, tt.ID, "a.csv")
	wantErr(t, "GetFile after delete", err, ErrNotFound)
	gt, _ = s.GetTest(c, tt.ID)
	if len(gt.Files) != 2 {
		t.Fatalf("Files after delete = %+v", gt.Files)
	}
}

func fullRun(testID string) model.Run {
	fin := base.Add(90 * time.Second)
	return model.Run{
		TestID:     testID,
		TestName:   "frozen-name",
		Status:     model.RunFailed,
		YAML:       "scenario: yaml",
		Workers:    3,
		StartedAt:  base,
		FinishedAt: &fin,
		Summary: &model.Summary{
			Requests: 1000, Errors: 10, Dropped: 1, ErrorRate: 0.01, RPS: 11.5,
			MeanMs: 12.5, P50Ms: 10, P90Ms: 20, P95Ms: 25, P99Ms: 40, MaxMs: 99.5,
			ByStep: map[string]model.Summary{
				"login": {Requests: 500, P99Ms: 30},
				"buy":   {Requests: 500, Errors: 10, ByStep: map[string]model.Summary{"inner": {Requests: 1}}},
			},
		},
		Thresholds: []model.ThresholdResult{
			{Metric: "p95", Op: "<", Value: 20, Actual: 25, Passed: false},
			{Metric: "error_rate", Op: "<", Value: 0.05, Actual: 0.01, Passed: true},
		},
		PeakCPU:  0.87,
		Warnings: []string{"high CPU", "worker lost"},
		Error:    "boom",
	}
}

func testRunCRUD(t *testing.T, s Store) {
	tt := mustTest(t, s, "runs")
	c := ctx(t)

	// Defaults: status queued, test name, started now.
	before := time.Now().Add(-time.Second)
	r := mustRun(t, s, model.Run{TestID: tt.ID, Workers: 1, YAML: "frozen"})
	if !isUUID(r.ID) || r.TestID != tt.ID || r.TestName != "runs" || r.Status != model.RunQueued ||
		r.StartedAt.Before(before) || r.FinishedAt != nil || r.Summary != nil || r.Workers != 1 || r.YAML != "frozen" {
		t.Fatalf("CreateRun defaults = %+v", r)
	}
	got, err := s.GetRun(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "GetRun", got, r)

	// Explicit values are kept on create.
	full := fullRun(tt.ID)
	fr := mustRun(t, s, full)
	want := full
	want.ID = fr.ID
	jsonEq(t, "CreateRun full", fr, want)
	got, err = s.GetRun(c, fr.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "GetRun full", got, want)

	// UpdateRun is a full replace except ID/TestID.
	upd := fullRun("ignored-test-id")
	upd.ID = r.ID
	upd.Status = model.RunPassed
	upd.Summary.ByStep = nil
	if err := s.UpdateRun(c, upd); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRun(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want = upd
	want.TestID = tt.ID
	jsonEq(t, "GetRun after update", got, want)

	// Clearing optional fields works; a zero StartedAt keeps the old one.
	if err := s.UpdateRun(c, model.Run{ID: r.ID, Status: model.RunStopped}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetRun(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want = model.Run{ID: r.ID, TestID: tt.ID, Status: model.RunStopped, StartedAt: base,
		Thresholds: []model.ThresholdResult{}, Warnings: []string{}}
	jsonEq(t, "GetRun after clearing", got, want)

	// Sub-microsecond and non-UTC timestamps are normalised identically.
	loc := time.FixedZone("X", 3*3600)
	odd := time.Date(2026, 5, 6, 7, 8, 9, 123456789, loc)
	or := mustRun(t, s, model.Run{TestID: tt.ID, StartedAt: odd})
	got, _ = s.GetRun(c, or.ID)
	if !got.StartedAt.Equal(odd.Truncate(time.Microsecond)) || got.StartedAt.Location() != time.UTC {
		t.Fatalf("StartedAt = %v, want %v UTC", got.StartedAt, odd.Truncate(time.Microsecond))
	}
	jsonEq(t, "CreateRun returned vs stored", or, got)
}

func testRunNotFound(t *testing.T, s Store) {
	c := ctx(t)
	for _, id := range []string{missingID, "bad"} {
		_, err := s.CreateRun(c, model.Run{TestID: id})
		wantErr(t, "CreateRun unknown test", err, ErrNotFound)
		_, err = s.GetRun(c, id)
		wantErr(t, "GetRun", err, ErrNotFound)
		wantErr(t, "UpdateRun", s.UpdateRun(c, model.Run{ID: id, Status: model.RunPassed}), ErrNotFound)
		wantErr(t, "AppendMetric", s.AppendMetric(c, id, model.MetricPoint{T: base}, nil), ErrNotFound)
		wantErr(t, "AppendErrorSamples", s.AppendErrorSamples(c, id, []model.ErrorSample{{T: base}}), ErrNotFound)
		if err := s.AppendErrorSamples(c, id, nil); err != nil {
			t.Fatalf("AppendErrorSamples(nil) = %v, want nil", err)
		}
		ms, err := s.ListMetrics(c, id)
		if err != nil || ms == nil || len(ms) != 0 {
			t.Fatalf("ListMetrics unknown = %#v, %v", ms, err)
		}
		ss, err := s.ListSnapshots(c, id)
		if err != nil || ss == nil || len(ss) != 0 {
			t.Fatalf("ListSnapshots unknown = %#v, %v", ss, err)
		}
		es, err := s.ListErrorSamples(c, id)
		if err != nil || es == nil || len(es) != 0 {
			t.Fatalf("ListErrorSamples unknown = %#v, %v", es, err)
		}
		rs, err := s.ListRuns(c, RunFilter{TestID: id})
		if err != nil || rs == nil || len(rs) != 0 {
			t.Fatalf("ListRuns unknown test = %#v, %v", rs, err)
		}
	}
	rs, err := s.ListRuns(c, RunFilter{})
	if err != nil || rs == nil || len(rs) != 0 {
		t.Fatalf("ListRuns empty store = %#v, %v", rs, err)
	}
}

func testRunEmptySlices(t *testing.T, s Store) {
	tt := mustTest(t, s, "slices")
	c := ctx(t)
	check := func(what string, r model.Run) {
		t.Helper()
		if r.Thresholds == nil || r.Warnings == nil {
			t.Fatalf("%s: nil slices: %#v / %#v", what, r.Thresholds, r.Warnings)
		}
		b, _ := json.Marshal(r)
		if !bytes.Contains(b, []byte(`"thresholds":[]`)) || !bytes.Contains(b, []byte(`"warnings":[]`)) {
			t.Fatalf("%s: JSON %s lacks empty arrays", what, b)
		}
	}
	r := mustRun(t, s, model.Run{TestID: tt.ID})
	check("CreateRun", r)
	got, err := s.GetRun(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("GetRun", got)
	if err := s.UpdateRun(c, model.Run{ID: r.ID, Status: model.RunRunning, Thresholds: nil, Warnings: nil}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun(c, r.ID)
	check("GetRun after update", got)
	rs, _ := s.ListRuns(c, RunFilter{})
	check("ListRuns", rs[0])
	ts, _ := s.ListTests(c)
	check("LastRun", *ts[0].LastRun)
	if _, err := s.MarkInterrupted(c, "x"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun(c, r.ID)
	check("after MarkInterrupted", got)
}

func testListRuns(t *testing.T, s Store) {
	a := mustTest(t, s, "a")
	b := mustTest(t, s, "b")
	c := ctx(t)
	// Created out of start order on purpose.
	ra2 := mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base.Add(2 * time.Second)})
	ra0 := mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base})
	rb1 := mustRun(t, s, model.Run{TestID: b.ID, StartedAt: base.Add(time.Second)})
	ra3 := mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base.Add(3 * time.Second)})

	ids := func(rs []model.Run) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	all, err := s.ListRuns(c, RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "all runs", ids(all), []string{ra3.ID, ra2.ID, rb1.ID, ra0.ID})
	onlyA, err := s.ListRuns(c, RunFilter{TestID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "runs of a", ids(onlyA), []string{ra3.ID, ra2.ID, ra0.ID})
	lim, err := s.ListRuns(c, RunFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "limit 2", ids(lim), []string{ra3.ID, ra2.ID})
	limA, err := s.ListRuns(c, RunFilter{TestID: a.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "a limit 1", ids(limA), []string{ra3.ID})
	neg, err := s.ListRuns(c, RunFilter{Limit: -5})
	if err != nil || len(neg) != 4 {
		t.Fatalf("negative limit = %d runs, %v", len(neg), err)
	}

	// Equal start times are ordered deterministically (ID desc).
	t1 := mustRun(t, s, model.Run{TestID: b.ID, StartedAt: base.Add(time.Hour)})
	t2 := mustRun(t, s, model.Run{TestID: b.ID, StartedAt: base.Add(time.Hour)})
	hi, lo := t1.ID, t2.ID
	if lo > hi {
		hi, lo = lo, hi
	}
	top, _ := s.ListRuns(c, RunFilter{Limit: 2})
	jsonEq(t, "tie order", ids(top), []string{hi, lo})
}

func testListRunsDefaultLimit(t *testing.T, s Store) {
	tt := mustTest(t, s, "many")
	for i := 0; i < defaultRunLimit+5; i++ {
		mustRun(t, s, model.Run{TestID: tt.ID, StartedAt: base.Add(time.Duration(i) * time.Second)})
	}
	rs, err := s.ListRuns(ctx(t), RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 50 {
		t.Fatalf("default limit returned %d runs, want 50", len(rs))
	}
	if !rs[0].StartedAt.Equal(base.Add(54 * time.Second)) {
		t.Fatalf("first run started %v", rs[0].StartedAt)
	}
	rs, _ = s.ListRuns(ctx(t), RunFilter{Limit: 100})
	if len(rs) != 55 {
		t.Fatalf("limit 100 returned %d runs, want 55", len(rs))
	}
}

func testLastRun(t *testing.T, s Store) {
	a := mustTest(t, s, "a")
	time.Sleep(2 * time.Millisecond)
	b := mustTest(t, s, "b")
	c := ctx(t)
	mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base})
	newest := fullRun(a.ID)
	newest.StartedAt = base.Add(time.Minute)
	newestRun := mustRun(t, s, newest)
	mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base.Add(time.Second)})

	list, err := s.ListTests(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("ListTests order wrong: %+v", list)
	}
	if list[0].LastRun != nil {
		t.Fatalf("test without runs has LastRun %+v", list[0].LastRun)
	}
	if list[1].LastRun == nil {
		t.Fatal("LastRun not filled")
	}
	jsonEq(t, "LastRun", *list[1].LastRun, newestRun)

	// GetTest does not fill LastRun.
	got, _ := s.GetTest(c, a.ID)
	if got.LastRun != nil {
		t.Fatal("GetTest filled LastRun")
	}
}

func testCascadeDelete(t *testing.T, s Store) {
	keep := mustTest(t, s, "keep")
	gone := mustTest(t, s, "gone")
	c := ctx(t)
	if err := s.PutFile(c, gone.ID, "f.csv", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile(c, keep.ID, "f.csv", []byte("y")); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, s, model.Run{TestID: gone.ID})
	kr := mustRun(t, s, model.Run{TestID: keep.ID})
	for _, id := range []string{r.ID, kr.ID} {
		if err := s.AppendMetric(c, id, model.MetricPoint{T: base}, []byte{1}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendErrorSamples(c, id, []model.ErrorSample{{T: base, Message: "e"}}); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteTest(c, gone.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetRun(c, r.ID)
	wantErr(t, "GetRun after cascade", err, ErrNotFound)
	_, err = s.GetFile(c, gone.ID, "f.csv")
	wantErr(t, "GetFile after cascade", err, ErrNotFound)
	if ms, _ := s.ListMetrics(c, r.ID); len(ms) != 0 {
		t.Fatalf("metrics survived: %+v", ms)
	}
	if ss, _ := s.ListSnapshots(c, r.ID); len(ss) != 0 {
		t.Fatalf("snapshots survived: %+v", ss)
	}
	if es, _ := s.ListErrorSamples(c, r.ID); len(es) != 0 {
		t.Fatalf("error samples survived: %+v", es)
	}
	if rs, _ := s.ListRuns(c, RunFilter{TestID: gone.ID}); len(rs) != 0 {
		t.Fatalf("runs survived: %+v", rs)
	}
	// The name is free again.
	mustTest(t, s, "gone")

	// The other test is untouched.
	if _, err := s.GetRun(c, kr.ID); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ListMetrics(c, kr.ID); len(ms) != 1 {
		t.Fatalf("kept metrics = %d", len(ms))
	}
	if es, _ := s.ListErrorSamples(c, kr.ID); len(es) != 1 {
		t.Fatalf("kept error samples = %d", len(es))
	}
	if f, _ := s.GetFile(c, keep.ID, "f.csv"); string(f) != "y" {
		t.Fatalf("kept file = %q", f)
	}
}

func testMetrics(t *testing.T, s Store) {
	tt := mustTest(t, s, "metrics")
	r := mustRun(t, s, model.Run{TestID: tt.ID})
	other := mustRun(t, s, model.Run{TestID: tt.ID})
	c := ctx(t)

	point := func(sec int) model.MetricPoint {
		return model.MetricPoint{
			T: base.Add(time.Duration(sec) * time.Second), Elapsed: float64(sec), TargetRate: 100,
			Active: int64(sec), Requests: int64(100 * sec), Errors: 1, Dropped: 2, RPS: 99.5,
			ErrorRate: 0.01, MeanMs: 1.5, P50Ms: 1, P95Ms: 3, P99Ms: 4, MaxMs: 9.25, CPU: 0.5, Workers: 2,
		}
	}
	snap := func(sec int) []byte {
		// Arbitrary binary including NUL and high bytes.
		return []byte{0x00, byte(sec), 0xff, 0x80, 'h', 'd', 'r'}
	}
	// Appended out of order.
	for _, sec := range []int{3, 1, 2} {
		if err := s.AppendMetric(c, r.ID, point(sec), snap(sec)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendMetric(c, other.ID, point(9), snap(9)); err != nil {
		t.Fatal(err)
	}
	// Nil snapshot is allowed.
	if err := s.AppendMetric(c, r.ID, point(4), nil); err != nil {
		t.Fatal(err)
	}

	ms, err := s.ListMetrics(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "metrics", ms, []model.MetricPoint{point(1), point(2), point(3), point(4)})
	for _, m := range ms {
		if m.T.Location() != time.UTC {
			t.Fatalf("metric T not UTC: %v", m.T)
		}
	}
	ss, err := s.ListSnapshots(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 4 {
		t.Fatalf("snapshots = %d, want 4", len(ss))
	}
	for i := 0; i < 3; i++ {
		if !bytes.Equal(ss[i], snap(i+1)) {
			t.Fatalf("snapshot %d = %x, want %x", i, ss[i], snap(i+1))
		}
	}
	if ss[3] == nil || len(ss[3]) != 0 {
		t.Fatalf("nil snapshot round-tripped as %#v, want empty non-nil", ss[3])
	}

	// Same T replaces the point.
	p2 := point(2)
	p2.Requests = 12345
	if err := s.AppendMetric(c, r.ID, p2, []byte("new")); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.ListMetrics(c, r.ID)
	ss, _ = s.ListSnapshots(c, r.ID)
	if len(ms) != 4 || ms[1].Requests != 12345 || string(ss[1]) != "new" {
		t.Fatalf("replace failed: %+v %q", ms, ss)
	}
	om, _ := s.ListMetrics(c, other.ID)
	if len(om) != 1 {
		t.Fatalf("other run metrics = %d", len(om))
	}
}

func testErrorSamples(t *testing.T, s Store) {
	tt := mustTest(t, s, "errs")
	r := mustRun(t, s, model.Run{TestID: tt.ID})
	c := ctx(t)
	batch1 := []model.ErrorSample{
		{T: base.Add(2 * time.Second), Step: "login", Status: 500, Message: "internal"},
		{T: base, Step: "home", Status: 0, Message: "dial tcp: connection refused"},
	}
	batch2 := []model.ErrorSample{
		{T: base.Add(time.Second), Step: "buy", Status: 404, Message: "not found"},
		{T: base.Add(2 * time.Second), Step: "login", Status: 503, Message: "unavailable – ünïcödé"},
	}
	if err := s.AppendErrorSamples(c, r.ID, batch1); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendErrorSamples(c, r.ID, batch2); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendErrorSamples(c, r.ID, []model.ErrorSample{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListErrorSamples(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "error samples", got, []model.ErrorSample{batch1[1], batch2[0], batch1[0], batch2[1]})
}

func testErrorGroups(t *testing.T, s Store) {
	tt := mustTest(t, s, "groups")
	r := mustRun(t, s, model.Run{TestID: tt.ID})
	c := ctx(t)

	got, err := s.ListErrorGroups(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "no groups", got, []model.ErrorGroup{})

	groups := []model.ErrorGroup{
		{Step: "login", Status: 503, Message: "unavailable – ünïcödé", Count: 1234,
			First: base, Last: base.Add(90*time.Second + 999*time.Nanosecond)},
		{Step: "home", Status: 0, Message: "dial tcp: connection refused", Count: 7, First: base, Last: base},
		{Step: "get", Status: 404, Message: "item {uuid} not found", Sample: "item 3f2504e0-4f89-11d3-9a0c-0305e82c3301 not found",
			Count: 5, First: base, Last: base},
		{Message: model.ErrorGroupOther, Count: 3},
	}
	if err := s.SetErrorGroups(c, r.ID, groups); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListErrorGroups(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]model.ErrorGroup(nil), groups...)
	want[0].Last = base.Add(90 * time.Second) // microsecond precision
	jsonEq(t, "groups", got, want)

	// Replace, not append.
	if err := s.SetErrorGroups(c, r.ID, groups[1:2]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListErrorGroups(c, r.ID)
	jsonEq(t, "replaced groups", got, want[1:2])
	got[0].Count = 999 // no aliasing
	again, _ := s.ListErrorGroups(c, r.ID)
	if again[0].Count != 7 {
		t.Fatal("ListErrorGroups aliases internal state")
	}

	wantErr(t, "SetErrorGroups unknown", s.SetErrorGroups(c, missingID, groups), ErrNotFound)
	wantErr(t, "SetErrorGroups bad id", s.SetErrorGroups(c, "nope", groups), ErrNotFound)
	_, err = s.ListErrorGroups(c, missingID)
	wantErr(t, "ListErrorGroups unknown", err, ErrNotFound)
	_, err = s.ListErrorGroups(c, "nope")
	wantErr(t, "ListErrorGroups bad id", err, ErrNotFound)

	// Deleting the run removes its groups.
	if err := s.DeleteRun(c, r.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListErrorGroups(c, r.ID)
	wantErr(t, "ListErrorGroups deleted", err, ErrNotFound)
}

func testMarkInterrupted(t *testing.T, s Store) {
	tt := mustTest(t, s, "mi")
	c := ctx(t)
	statuses := []model.RunStatus{model.RunQueued, model.RunRunning, model.RunPassed, model.RunFailed,
		model.RunStopped, model.RunError, model.RunRunning}
	runs := make([]model.Run, len(statuses))
	for i, st := range statuses {
		runs[i] = mustRun(t, s, model.Run{TestID: tt.ID, Status: st, Error: "orig"})
	}
	before := time.Now().Add(-time.Second)
	n, err := s.MarkInterrupted(c, "master restarted")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("MarkInterrupted = %d, want 3", n)
	}
	for i, r := range runs {
		got, err := s.GetRun(c, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch statuses[i] {
		case model.RunQueued, model.RunRunning:
			if got.Status != model.RunError || got.Error != "master restarted" ||
				got.FinishedAt == nil || got.FinishedAt.Before(before) {
				t.Fatalf("run %d not interrupted: %+v", i, got)
			}
		default:
			if got.Status != statuses[i] || got.Error != "orig" || got.FinishedAt != nil {
				t.Fatalf("finished run %d changed: %+v", i, got)
			}
		}
	}
	n, err = s.MarkInterrupted(c, "again")
	if err != nil || n != 0 {
		t.Fatalf("second MarkInterrupted = %d, %v", n, err)
	}
}

// testNoAliasing checks that values passed in or returned can be mutated
// without affecting stored state.
func testNoAliasing(t *testing.T, s Store) {
	tt := mustTest(t, s, "alias")
	c := ctx(t)

	content := []byte("abc")
	if err := s.PutFile(c, tt.ID, "f", content); err != nil {
		t.Fatal(err)
	}
	content[0] = 'X'
	f, _ := s.GetFile(c, tt.ID, "f")
	f[1] = 'Y'
	if f2, _ := s.GetFile(c, tt.ID, "f"); string(f2) != "abc" {
		t.Fatalf("file aliased: %q", f2)
	}

	in := fullRun(tt.ID)
	r := mustRun(t, s, in)
	want, _ := json.Marshal(r)
	in.Warnings[0] = "mut-in"
	in.Thresholds[0].Metric = "mut-in"
	in.Summary.ByStep["login"] = model.Summary{Requests: -1}
	*in.FinishedAt = time.Time{}
	r.Warnings[1] = "mut-out"
	r.Summary.Requests = -1
	r.Summary.ByStep["buy"] = model.Summary{}
	*r.FinishedAt = time.Time{}

	got, _ := s.GetRun(c, r.ID)
	if b, _ := json.Marshal(got); !bytes.Equal(b, want) {
		t.Fatalf("run aliased:\n got  %s\n want %s", b, want)
	}
	got.Warnings = append(got.Warnings[:0], "x")
	rs, _ := s.ListRuns(c, RunFilter{})
	rs[0].Thresholds[0].Actual = -1
	ts, _ := s.ListTests(c)
	ts[0].LastRun.Summary.ByStep["login"] = model.Summary{}
	ts[0].Files = append(ts[0].Files, model.FileInfo{Name: "fake"})
	got, _ = s.GetRun(c, r.ID)
	if b, _ := json.Marshal(got); !bytes.Equal(b, want) {
		t.Fatalf("run aliased via list:\n got  %s\n want %s", b, want)
	}
	if gt, _ := s.GetTest(c, tt.ID); len(gt.Files) != 1 {
		t.Fatalf("files aliased: %+v", gt.Files)
	}

	snap := []byte{1, 2, 3}
	if err := s.AppendMetric(c, r.ID, model.MetricPoint{T: base}, snap); err != nil {
		t.Fatal(err)
	}
	snap[0] = 9
	ss, _ := s.ListSnapshots(c, r.ID)
	ss[0][1] = 9
	if ss2, _ := s.ListSnapshots(c, r.ID); !bytes.Equal(ss2[0], []byte{1, 2, 3}) {
		t.Fatalf("snapshot aliased: %v", ss2[0])
	}

	es := []model.ErrorSample{{T: base, Message: "m"}}
	if err := s.AppendErrorSamples(c, r.ID, es); err != nil {
		t.Fatal(err)
	}
	es[0].Message = "mut"
	got2, _ := s.ListErrorSamples(c, r.ID)
	got2[0].Message = "mut2"
	if got3, _ := s.ListErrorSamples(c, r.ID); got3[0].Message != "m" {
		t.Fatalf("error samples aliased: %+v", got3)
	}
}

// testConcurrent hammers the store from many goroutines; run with -race.
func testConcurrent(t *testing.T, s Store) {
	const workers, perWorker = 8, 5
	c := ctx(t)
	var wg sync.WaitGroup
	errc := make(chan error, workers*perWorker*8)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				tt, err := s.CreateTest(c, model.Test{Name: fmt.Sprintf("w%d-%d", w, i)})
				if err != nil {
					errc <- err
					return
				}
				if err := s.PutFile(c, tt.ID, "f.csv", []byte("x")); err != nil {
					errc <- err
				}
				r, err := s.CreateRun(c, model.Run{TestID: tt.ID, Status: model.RunRunning})
				if err != nil {
					errc <- err
					return
				}
				for sec := 0; sec < 3; sec++ {
					p := model.MetricPoint{T: base.Add(time.Duration(sec) * time.Second), Requests: int64(sec)}
					if err := s.AppendMetric(c, r.ID, p, []byte{byte(sec)}); err != nil {
						errc <- err
					}
				}
				if err := s.AppendErrorSamples(c, r.ID, []model.ErrorSample{{T: base, Message: "x"}}); err != nil {
					errc <- err
				}
				r.Status = model.RunPassed
				if err := s.UpdateRun(c, r); err != nil {
					errc <- err
				}
				if _, err := s.ListTests(c); err != nil {
					errc <- err
				}
				if _, err := s.ListRuns(c, RunFilter{}); err != nil {
					errc <- err
				}
			}
		}(w)
	}
	// Concurrent duplicate-name creates: exactly one wins.
	var dupWins, dupConflicts int
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateTest(c, model.Test{Name: "dup"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				dupWins++
			case errors.Is(err, ErrConflict):
				dupConflicts++
			default:
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	if dupWins != 1 || dupConflicts != workers-1 {
		t.Fatalf("dup creates: %d wins, %d conflicts", dupWins, dupConflicts)
	}
	ts, err := s.ListTests(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != workers*perWorker+1 {
		t.Fatalf("tests = %d, want %d", len(ts), workers*perWorker+1)
	}
	rs, err := s.ListRuns(c, RunFilter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != workers*perWorker {
		t.Fatalf("runs = %d", len(rs))
	}
	for _, r := range rs {
		if r.Status != model.RunPassed {
			t.Fatalf("run %s status %s", r.ID, r.Status)
		}
		if ms, _ := s.ListMetrics(c, r.ID); len(ms) != 3 {
			t.Fatalf("run %s has %d metrics", r.ID, len(ms))
		}
	}
}
