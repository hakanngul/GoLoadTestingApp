package store

import (
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func strp(s string) *string { return &s }

func testRunMeta(t *testing.T, s Store) {
	tt := mustTest(t, s, "meta")
	c := ctx(t)
	r := mustRun(t, s, model.Run{TestID: tt.ID, Label: "v1.0", Notes: "first <try>"})
	if r.Label != "v1.0" || r.Notes != "first <try>" {
		t.Fatalf("CreateRun label/notes = %q/%q", r.Label, r.Notes)
	}
	got, err := s.GetRun(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "v1.0" || got.Notes != "first <try>" {
		t.Fatalf("GetRun label/notes = %q/%q", got.Label, got.Notes)
	}

	// UpdateRun (runner status updates) never clobbers metadata.
	upd := got
	upd.Status = model.RunPassed
	upd.Label, upd.Notes = "", ""
	if err := s.UpdateRun(c, upd); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun(c, r.ID)
	if got.Status != model.RunPassed || got.Label != "v1.0" || got.Notes != "first <try>" {
		t.Fatalf("after UpdateRun = %+v", got)
	}

	// Partial updates.
	out, err := s.UpdateRunMeta(c, r.ID, strp("v2"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Label != "v2" || out.Notes != "first <try>" || out.Status != model.RunPassed {
		t.Fatalf("UpdateRunMeta(label) = %+v", out)
	}
	out, err = s.UpdateRunMeta(c, r.ID, nil, strp(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Label != "v2" || out.Notes != "" {
		t.Fatalf("UpdateRunMeta(notes) = %+v", out)
	}
	got, _ = s.GetRun(c, r.ID)
	jsonEq(t, "GetRun after UpdateRunMeta", got, out)

	// Metadata shows up in ListRuns and LastRun.
	runs, _ := s.ListRuns(c, RunFilter{TestID: tt.ID})
	if len(runs) != 1 || runs[0].Label != "v2" {
		t.Fatalf("ListRuns = %+v", runs)
	}
	tests, _ := s.ListTests(c)
	if len(tests) != 1 || tests[0].LastRun == nil || tests[0].LastRun.Label != "v2" {
		t.Fatalf("ListTests = %+v", tests)
	}

	for _, id := range []string{missingID, "bad"} {
		_, err := s.UpdateRunMeta(c, id, strp("x"), nil, nil)
		wantErr(t, "UpdateRunMeta unknown", err, ErrNotFound)
	}
}

func testDeleteRun(t *testing.T, s Store) {
	tt := mustTest(t, s, "del")
	c := ctx(t)
	r := mustRun(t, s, model.Run{TestID: tt.ID})
	keep := mustRun(t, s, model.Run{TestID: tt.ID})
	for _, id := range []string{r.ID, keep.ID} {
		if err := s.AppendMetric(c, id, model.MetricPoint{T: base}, []byte{1}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendErrorSamples(c, id, []model.ErrorSample{{T: base, Message: "e"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetBaseline(c, tt.ID, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRun(c, r.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetRun(c, r.ID)
	wantErr(t, "GetRun after delete", err, ErrNotFound)
	if ms, _ := s.ListMetrics(c, r.ID); len(ms) != 0 {
		t.Fatalf("metrics survived: %+v", ms)
	}
	if es, _ := s.ListErrorSamples(c, r.ID); len(es) != 0 {
		t.Fatalf("error samples survived: %+v", es)
	}
	gt, err := s.GetTest(c, tt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gt.BaselineRunID != "" {
		t.Fatalf("baseline not cleared: %q", gt.BaselineRunID)
	}
	// The other run is untouched.
	if ms, _ := s.ListMetrics(c, keep.ID); len(ms) != 1 {
		t.Fatalf("kept metrics = %d", len(ms))
	}
	wantErr(t, "DeleteRun again", s.DeleteRun(c, r.ID), ErrNotFound)
	wantErr(t, "DeleteRun bad", s.DeleteRun(c, "bad"), ErrNotFound)
}

func testBaseline(t *testing.T, s Store) {
	a := mustTest(t, s, "a")
	b := mustTest(t, s, "b")
	c := ctx(t)
	ra := mustRun(t, s, model.Run{TestID: a.ID})
	rb := mustRun(t, s, model.Run{TestID: b.ID})

	got, err := s.SetBaseline(c, a.ID, ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.BaselineRunID != ra.ID || got.Name != "a" || got.Files == nil {
		t.Fatalf("SetBaseline = %+v", got)
	}
	if !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("SetBaseline bumped UpdatedAt")
	}
	gt, _ := s.GetTest(c, a.ID)
	if gt.BaselineRunID != ra.ID {
		t.Fatalf("GetTest baseline = %q", gt.BaselineRunID)
	}
	tests, _ := s.ListTests(c)
	for _, tt := range tests {
		want := ""
		if tt.ID == a.ID {
			want = ra.ID
		}
		if tt.BaselineRunID != want {
			t.Fatalf("ListTests %s baseline = %q, want %q", tt.Name, tt.BaselineRunID, want)
		}
	}

	// Errors.
	_, err = s.SetBaseline(c, a.ID, rb.ID)
	wantErr(t, "baseline of another test", err, ErrConflict)
	_, err = s.SetBaseline(c, a.ID, missingID)
	wantErr(t, "unknown run", err, ErrNotFound)
	_, err = s.SetBaseline(c, a.ID, "bad")
	wantErr(t, "bad run id", err, ErrNotFound)
	_, err = s.SetBaseline(c, missingID, ra.ID)
	wantErr(t, "unknown test", err, ErrNotFound)
	if gt, _ := s.GetTest(c, a.ID); gt.BaselineRunID != ra.ID {
		t.Fatalf("failed SetBaseline changed baseline to %q", gt.BaselineRunID)
	}

	// Clear.
	got, err = s.SetBaseline(c, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.BaselineRunID != "" {
		t.Fatalf("cleared baseline = %q", got.BaselineRunID)
	}

	// Deleting a test that has a baseline cascades cleanly.
	if _, err := s.SetBaseline(c, b.ID, rb.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTest(c, b.ID); err != nil {
		t.Fatalf("DeleteTest with baseline: %v", err)
	}
	_, err = s.GetRun(c, rb.ID)
	wantErr(t, "run after test delete", err, ErrNotFound)
}

func testMetricsWithSnapshots(t *testing.T, s Store) {
	tt := mustTest(t, s, "mws")
	r := mustRun(t, s, model.Run{TestID: tt.ID})
	c := ctx(t)
	// Insert out of order; one point without a snapshot.
	for _, sec := range []int{3, 1, 2} {
		var snap []byte
		if sec != 2 {
			snap = []byte{byte(sec), 0xff}
		}
		p := model.MetricPoint{T: base.Add(time.Duration(sec) * time.Second), Elapsed: float64(sec), Requests: int64(sec * 10)}
		if err := s.AppendMetric(c, r.ID, p, snap); err != nil {
			t.Fatal(err)
		}
	}
	pts, snaps, err := s.ListMetricsWithSnapshots(c, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 || len(snaps) != 3 {
		t.Fatalf("len = %d/%d", len(pts), len(snaps))
	}
	for i, p := range pts {
		sec := i + 1
		if p.Elapsed != float64(sec) || p.Requests != int64(sec*10) {
			t.Fatalf("point %d = %+v", i, p)
		}
		if sec == 2 {
			if snaps[i] == nil || len(snaps[i]) != 0 {
				t.Fatalf("missing snapshot = %v, want empty non-nil", snaps[i])
			}
		} else if len(snaps[i]) != 2 || snaps[i][0] != byte(sec) {
			t.Fatalf("snapshot %d = %v", i, snaps[i])
		}
	}
	// Consistent with the separate listings.
	ms, _ := s.ListMetrics(c, r.ID)
	jsonEq(t, "points", pts, ms)

	for _, id := range []string{missingID, "bad"} {
		pts, snaps, err := s.ListMetricsWithSnapshots(c, id)
		if err != nil || len(pts) != 0 || len(snaps) != 0 {
			t.Fatalf("unknown run: %v %v %v", pts, snaps, err)
		}
	}
}

func testRunsSince(t *testing.T, s Store) {
	a := mustTest(t, s, "a")
	b := mustTest(t, s, "b")
	c := ctx(t)
	old := mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base.Add(-48 * time.Hour)})
	edge := mustRun(t, s, model.Run{TestID: a.ID, StartedAt: base})
	full := fullRun(b.ID)
	full.StartedAt = base.Add(time.Hour)
	newest := mustRun(t, s, full)
	_ = old

	runs, err := s.RunsSince(c, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != newest.ID || runs[1].ID != edge.ID {
		t.Fatalf("RunsSince = %+v", runs)
	}
	if runs[0].Summary == nil || runs[0].Summary.Requests != 1000 || runs[0].Summary.ByStep != nil {
		t.Fatalf("summary = %+v, want without by_step", runs[0].Summary)
	}
	if runs[1].Summary != nil {
		t.Fatalf("nil summary became %+v", runs[1].Summary)
	}
	// The stored run keeps its by_step.
	got, _ := s.GetRun(c, newest.ID)
	if len(got.Summary.ByStep) != 2 {
		t.Fatalf("stored by_step = %+v", got.Summary.ByStep)
	}
	runs, err = s.RunsSince(c, base.Add(24*time.Hour))
	if err != nil || len(runs) != 0 {
		t.Fatalf("future RunsSince = %+v, %v", runs, err)
	}
}
