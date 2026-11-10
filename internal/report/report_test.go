package report

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

func stages(spec ...[2]int) []scenario.Stage {
	out := make([]scenario.Stage, len(spec))
	for i, s := range spec {
		out[i] = scenario.Stage{Duration: scenario.Duration{Duration: time.Duration(s[0]) * time.Second}, Target: s[1]}
	}
	return out
}

func TestResolveWindow(t *testing.T) {
	rampUpDown := stages([2]int{10, 10}, [2]int{20, 10}, [2]int{10, 0})
	cases := []struct {
		name     string
		kind     model.WindowKind
		from, to float64
		stages   []scenario.Stage
		dur      float64
		want     model.Window
		err      bool
	}{
		{name: "full", kind: model.WindowFull, stages: rampUpDown, dur: 40, want: model.Window{Kind: "full", From: 0, To: 40}},
		{name: "steady ramp up and down", kind: model.WindowSteady, stages: rampUpDown, dur: 40, want: model.Window{Kind: "steady", From: 10, To: 30}},
		{name: "steady run longer than scenario", kind: model.WindowSteady, stages: rampUpDown, dur: 40.4, want: model.Window{Kind: "steady", From: 10, To: 30}},
		{name: "steady stopped early", kind: model.WindowSteady, stages: rampUpDown, dur: 15, want: model.Window{Kind: "steady", From: 10, To: 15}},
		{name: "steady stopped during ramp-up", kind: model.WindowSteady, stages: rampUpDown, dur: 8, want: model.Window{Kind: "full", From: 0, To: 8}},
		{name: "steady single stage falls back", kind: model.WindowSteady, stages: stages([2]int{30, 5}), dur: 30, want: model.Window{Kind: "full", From: 0, To: 30}},
		{name: "steady no ramp-down", kind: model.WindowSteady, stages: stages([2]int{10, 10}, [2]int{20, 20}), dur: 30, want: model.Window{Kind: "steady", From: 10, To: 30}},
		{name: "steady first stage at zero kept", kind: model.WindowSteady, stages: stages([2]int{5, 0}, [2]int{20, 10}, [2]int{5, 2}), dur: 30, want: model.Window{Kind: "steady", From: 0, To: 25}},
		{name: "steady up then down only", kind: model.WindowSteady, stages: stages([2]int{10, 10}, [2]int{10, 0}), dur: 20, want: model.Window{Kind: "full", From: 0, To: 20}},
		{name: "steady without stages", kind: model.WindowSteady, dur: 12, want: model.Window{Kind: "full", From: 0, To: 12}},
		{name: "custom", kind: model.WindowCustom, from: 5, to: 12.5, stages: rampUpDown, dur: 40, want: model.Window{Kind: "custom", From: 5, To: 12.5}},
		{name: "custom clamped", kind: model.WindowCustom, from: -5, to: 100, dur: 40, want: model.Window{Kind: "custom", From: 0, To: 40}},
		{name: "custom past end is empty", kind: model.WindowCustom, from: 50, to: 60, dur: 40, want: model.Window{Kind: "custom", From: 40, To: 40}},
		{name: "custom reversed", kind: model.WindowCustom, from: 10, to: 10, dur: 40, err: true},
		{name: "custom NaN", kind: model.WindowCustom, from: math.NaN(), to: 10, dur: 40, err: true},
		{name: "unknown", kind: "bogus", dur: 40, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveWindow(c.kind, c.from, c.to, c.stages, c.dur)
			if c.err {
				if !errors.Is(err, ErrBadWindow) {
					t.Fatalf("err = %v, want ErrBadWindow", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestPointsInWindow(t *testing.T) {
	// 1s points, a lost second (4 missing) and a final partial point.
	pts := []model.MetricPoint{{Elapsed: 1}, {Elapsed: 2}, {Elapsed: 3}, {Elapsed: 5}, {Elapsed: 5.4}}
	got := pointsInWindow(pts, model.Window{From: 2, To: 5})
	want := []bool{false, false, true, true, false} // midpoints .5 1.5 2.5 4.5 5.2
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("point %d: got %v, want %v (all %v)", i, got[i], want[i], got)
		}
	}
}

const reportYAML = `name: s
target:
  base_url: http://example.test
load:
  stages:
    - duration: 10s
      target: 10
    - duration: 20s
      target: 10
    - duration: 10s
      target: 0
steps:
  - name: a
    path: /a
  - name: b
    path: /b
thresholds:
  - metric: latency.p95
    op: lt
    value: 200
`

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// secondResults returns the results recorded in second k (1-based) of a
// 40 s run: ramp seconds are slow (1000 ms) and 10% errors; steady seconds
// have latencies 1..100 ms (times scale) and no errors.
func secondResults(k int, scale float64) []protocol.Result {
	var out []protocol.Result
	if k <= 10 || k > 30 {
		for i := 0; i < 10; i++ {
			r := protocol.Result{Step: "a", Status: 200, Latency: time.Second}
			if i == 0 {
				r.Err, r.Status = errors.New("boom"), 500
			}
			out = append(out, r)
		}
		return out
	}
	for i := 1; i <= 100; i++ {
		step := "a"
		if i%2 == 0 {
			step = "b"
		}
		out = append(out, protocol.Result{Step: step, Status: 200,
			Latency: time.Duration(float64(i) * scale * float64(time.Millisecond))})
	}
	return out
}

// seedRun stores a finished 40 s run with per-second snapshots and
// returns it plus the reference snapshot of its steady seconds.
func seedRun(t *testing.T, st store.Store, testID string, scale float64, label string) (model.Run, *metrics.Snapshot, *metrics.Snapshot) {
	t.Helper()
	ctx := context.Background()
	run, err := st.CreateRun(ctx, model.Run{TestID: testID, Status: model.RunRunning, YAML: reportYAML,
		StartedAt: t0, Label: label})
	if err != nil {
		t.Fatal(err)
	}
	col := metrics.NewCollector(t0)
	steady := metrics.NewCollector(t0.Add(10 * time.Second))
	for k := 1; k <= 40; k++ {
		for _, r := range secondResults(k, scale) {
			col.Record(r)
			if k > 10 && k <= 30 {
				steady.Record(r)
			}
		}
		end := t0.Add(time.Duration(k) * time.Second)
		snap := col.Flush(end)
		blob, err := snap.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		s := metrics.Summarize(snap)
		p := model.MetricPoint{T: end, Elapsed: float64(k), Requests: s.Requests, Errors: s.Errors,
			RPS: s.RPS, P95Ms: ms(s.P95), P50Ms: ms(s.P50), P99Ms: ms(s.P99)}
		if err := st.AppendMetric(ctx, run.ID, p, blob); err != nil {
			t.Fatal(err)
		}
	}
	steady.Flush(t0.Add(30 * time.Second))
	cum := col.Cumulative()
	sum := ToModelSummary(metrics.Summarize(cum))
	fin := t0.Add(40 * time.Second)
	run.Status, run.Summary, run.FinishedAt = model.RunFailed, &sum, &fin
	run.Thresholds = []model.ThresholdResult{{Metric: "latency.p95", Op: "lt", Value: 200, Actual: sum.P95Ms, Passed: false}}
	if err := st.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run, _ = st.GetRun(ctx, run.ID)
	return run, steady.Cumulative(), cum
}

func newTest(t *testing.T, st store.Store, name string) model.Test {
	t.Helper()
	tt, err := st.CreateTest(context.Background(), model.Test{Name: name, YAML: reportYAML})
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestRunReportExactAggregation(t *testing.T) {
	st := store.NewMemory()
	tt := newTest(t, st, "agg")
	run, steadyRef, fullRef := seedRun(t, st, tt.ID, 1, "v1")
	rp := New(st)
	ctx := context.Background()

	steady, err := rp.RunReport(ctx, run.ID, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if steady.Window != (model.Window{Kind: model.WindowSteady, From: 10, To: 30}) {
		t.Fatalf("window = %+v", steady.Window)
	}
	ref := metrics.Summarize(steadyRef)
	s := steady.Summary
	if s.Requests != 2000 || s.Errors != 0 || s.ErrorRate != 0 {
		t.Fatalf("steady counts = %+v", s)
	}
	if s.RPS != 100 {
		t.Fatalf("steady RPS = %v, want 100 (2000 requests / 20 s)", s.RPS)
	}
	if s.P95Ms != ms(ref.P95) || s.P50Ms != ms(ref.P50) || s.P99Ms != ms(ref.P99) || s.MaxMs != ms(ref.Max) || s.MeanMs != ms(ref.Mean) {
		t.Fatalf("steady latencies %+v != reference %+v", s, ToModelSummary(ref))
	}
	// HDR precision (3 significant figures): p95 of 1..100 ms is 95 ms.
	if math.Abs(s.P95Ms-95) > 0.1 {
		t.Fatalf("steady p95 = %v, want ~95", s.P95Ms)
	}
	if len(s.ByStep) != 2 || s.ByStep["a"].Requests != 1000 || s.ByStep["b"].Requests != 1000 {
		t.Fatalf("by_step = %+v", s.ByStep)
	}
	if s.ByStep["b"].P95Ms != ms(metrics.Summarize(steadyRef).ByStep["b"].P95) {
		t.Fatalf("step b p95 = %v", s.ByStep["b"].P95Ms)
	}
	if len(steady.Thresholds) != 1 || !steady.Thresholds[0].Passed || steady.Thresholds[0].Actual != s.P95Ms {
		t.Fatalf("steady thresholds = %+v", steady.Thresholds)
	}
	if steady.Run.ID != run.ID || steady.Run.Label != "v1" || steady.Baseline != nil {
		t.Fatalf("run = %+v baseline = %+v", steady.Run, steady.Baseline)
	}

	full, err := rp.RunReport(ctx, run.ID, WindowSpec{Kind: model.WindowFull}, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	fr := metrics.Summarize(fullRef)
	f := full.Summary
	if full.Window != (model.Window{Kind: model.WindowFull, From: 0, To: 40}) {
		t.Fatalf("full window = %+v", full.Window)
	}
	if f.Requests != 2200 || f.Errors != 20 || f.P95Ms != ms(fr.P95) || f.P99Ms != ms(fr.P99) {
		t.Fatalf("full summary = %+v, reference %+v", f, ToModelSummary(fr))
	}
	if f.RPS != 2200.0/40 {
		t.Fatalf("full RPS = %v", f.RPS)
	}
	if f.P95Ms < 900 || s.P95Ms >= f.P95Ms {
		t.Fatalf("ramp seconds should dominate full p95: steady %v full %v", s.P95Ms, f.P95Ms)
	}
	if len(full.Thresholds) != 1 || full.Thresholds[0].Passed {
		t.Fatalf("full thresholds = %+v", full.Thresholds)
	}

	custom, err := rp.RunReport(ctx, run.ID, WindowSpec{Kind: model.WindowCustom, From: 5, To: 15}, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if custom.Summary.Requests != 5*10+5*100 || custom.Summary.RPS != 55 {
		t.Fatalf("custom = %+v", custom.Summary)
	}

	_, err = rp.RunReport(ctx, "nope", Steady, DefaultTolerance)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown run err = %v", err)
	}
	_, err = rp.RunReport(ctx, run.ID, WindowSpec{Kind: "bogus"}, DefaultTolerance)
	if !errors.Is(err, ErrBadWindow) {
		t.Fatalf("bad window err = %v", err)
	}
}

func TestRunReportBaseline(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	tt := newTest(t, st, "bl")
	base, _, _ := seedRun(t, st, tt.ID, 1, "base")
	cand, _, _ := seedRun(t, st, tt.ID, 2, "slow") // steady latencies doubled
	if _, err := st.SetBaseline(ctx, tt.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	rp := New(st)
	rep, err := rp.RunReport(ctx, cand.ID, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	c := rep.Baseline
	if c == nil || c.ReferenceRunID != base.ID || c.CandidateRunID != cand.ID || !c.Regression || c.Tolerance != DefaultTolerance {
		t.Fatalf("baseline = %+v", c)
	}
	byName := map[string]model.MetricDelta{}
	for _, m := range c.Metrics {
		byName[m.Metric] = m
	}
	if m := byName["p95_ms"]; m.Verdict != model.VerdictWorse || math.Abs(m.Delta-1) > 0.01 {
		t.Fatalf("p95 = %+v", m)
	}
	if m := byName["rps"]; m.Verdict != model.VerdictSame || m.Delta != 0 {
		t.Fatalf("rps = %+v", m)
	}
	if m := byName["error_rate"]; m.Verdict != model.VerdictSame {
		t.Fatalf("error_rate = %+v", m)
	}
	if len(c.ByStep) != 2 {
		t.Fatalf("by_step = %+v", c.ByStep)
	}
	// The baseline run itself has no comparison.
	rep, _ = rp.RunReport(ctx, base.ID, Steady, DefaultTolerance)
	if rep.Baseline != nil {
		t.Fatalf("baseline run compared with itself")
	}
}

func TestRunReportFallback(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	tt := newTest(t, st, "fb")
	fin := t0.Add(20 * time.Second)
	sum := model.Summary{Requests: 7, P95Ms: 3, ByStep: map[string]model.Summary{"a": {Requests: 7}}}
	run, _ := st.CreateRun(ctx, model.Run{TestID: tt.ID, Status: model.RunPassed, YAML: reportYAML, StartedAt: t0,
		FinishedAt: &fin, Summary: &sum, Thresholds: []model.ThresholdResult{{Metric: "x", Passed: true}}})
	for k := 1; k <= 20; k++ {
		blob := []byte("garbage")
		if k == 3 {
			blob = nil
		}
		if err := st.AppendMetric(ctx, run.ID, model.MetricPoint{T: t0.Add(time.Duration(k) * time.Second), Elapsed: float64(k)}, blob); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := New(st).RunReport(ctx, run.ID, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Window != (model.Window{Kind: model.WindowFull, From: 0, To: 20}) || rep.Summary.Requests != 7 ||
		rep.Summary.ByStep["a"].Requests != 7 || len(rep.Thresholds) != 1 || rep.Thresholds[0].Metric != "x" {
		t.Fatalf("fallback report = %+v", rep)
	}

	// No points at all: duration from timestamps.
	run2, _ := st.CreateRun(ctx, model.Run{TestID: tt.ID, Status: model.RunError, YAML: "broken", StartedAt: t0, FinishedAt: &fin})
	rep, err = New(st).RunReport(ctx, run2.ID, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Window != (model.Window{Kind: model.WindowFull, From: 0, To: 20}) || rep.Summary.Requests != 0 || rep.Thresholds == nil {
		t.Fatalf("empty report = %+v", rep)
	}
}

func md(metric string, ref, cand float64) (model.Summary, model.Summary) {
	var r, c model.Summary
	set := func(s *model.Summary, v float64) {
		switch metric {
		case "p50_ms":
			s.P50Ms = v
		case "p90_ms":
			s.P90Ms = v
		case "p95_ms":
			s.P95Ms = v
		case "p99_ms":
			s.P99Ms = v
		case "max_ms":
			s.MaxMs = v
		case "mean_ms":
			s.MeanMs = v
		case "rps":
			s.RPS = v
		case "error_rate":
			s.ErrorRate = v
		}
	}
	set(&r, ref)
	set(&c, cand)
	return r, c
}

func TestCompareVerdicts(t *testing.T) {
	cases := []struct {
		metric    string
		ref, cand float64
		tol       float64
		want      model.Verdict
		delta     float64
		regress   bool
	}{
		{"p95_ms", 100, 120, 0.10, model.VerdictWorse, 0.2, true},
		{"p95_ms", 100, 110, 0.10, model.VerdictSame, 0.1, false}, // exactly at tolerance
		{"p95_ms", 100, 105, 0.10, model.VerdictSame, 0.05, false},
		{"p95_ms", 100, 80, 0.10, model.VerdictBetter, -0.2, false},
		{"p50_ms", 10, 12, 0.10, model.VerdictWorse, 0.2, true},
		{"p90_ms", 10, 12, 0.30, model.VerdictSame, 0.2, false},
		{"p99_ms", 10, 15, 0.10, model.VerdictWorse, 0.5, true},
		{"mean_ms", 10, 12, 0.10, model.VerdictWorse, 0.2, true},
		{"max_ms", 100, 300, 0.10, model.VerdictWorse, 2, false}, // reported, but no regression
		{"p95_ms", 0, 50, 0.10, model.VerdictNA, 0, false},
		{"rps", 100, 80, 0.10, model.VerdictWorse, -0.2, true},
		{"rps", 100, 120, 0.10, model.VerdictBetter, 0.2, false},
		{"rps", 100, 95, 0.10, model.VerdictSame, -0.05, false},
		{"rps", 0, 10, 0.10, model.VerdictNA, 0, false},
		{"error_rate", 0.01, 0.025, 0.10, model.VerdictWorse, 0.015, true}, // absolute, limit 0.01
		{"error_rate", 0.01, 0.015, 0.10, model.VerdictSame, 0.005, false},
		{"error_rate", 0.03, 0.01, 0.10, model.VerdictBetter, -0.02, false},
		{"error_rate", 0, 0.02, 0.10, model.VerdictWorse, 0.02, true}, // zero reference is fine
		{"error_rate", 0, 0, 0.10, model.VerdictSame, 0, false},
		{"error_rate", 0.10, 0.12, 0.10, model.VerdictWorse, 0.02, true}, // +20% relative but judged absolutely
	}
	for _, c := range cases {
		ref, cand := md(c.metric, c.ref, c.cand)
		cmp := Compare(ref, cand, c.tol)
		var got *model.MetricDelta
		for i := range cmp.Metrics {
			if cmp.Metrics[i].Metric == c.metric {
				got = &cmp.Metrics[i]
			}
		}
		if got == nil {
			t.Fatalf("%s missing", c.metric)
		}
		if got.Verdict != c.want || math.Abs(got.Delta-c.delta) > 1e-9 || got.Reference != c.ref || got.Candidate != c.cand {
			t.Errorf("%s %v->%v tol %v: got %+v, want %s delta %v", c.metric, c.ref, c.cand, c.tol, *got, c.want, c.delta)
		}
		if cmp.Regression != c.regress {
			t.Errorf("%s %v->%v: regression = %v, want %v", c.metric, c.ref, c.cand, cmp.Regression, c.regress)
		}
	}

	// Metric order and by_step on common steps only.
	ref := model.Summary{P95Ms: 10, ByStep: map[string]model.Summary{"a": {P95Ms: 10}, "gone": {P95Ms: 1}}}
	cand := model.Summary{P95Ms: 10, ByStep: map[string]model.Summary{"a": {P95Ms: 20}, "new": {P95Ms: 1}}}
	cmp := Compare(ref, cand, -1) // invalid tolerance -> default
	names := []string{}
	for _, m := range cmp.Metrics {
		names = append(names, m.Metric)
	}
	if got := len(names); got != 8 || names[0] != "p50_ms" || names[6] != "rps" || names[7] != "error_rate" {
		t.Fatalf("metrics = %v", names)
	}
	if cmp.Tolerance != DefaultTolerance || len(cmp.ByStep) != 1 || cmp.ByStep["a"][2].Verdict != model.VerdictWorse {
		t.Fatalf("by_step = %+v", cmp.ByStep)
	}
	if cmp.Regression {
		t.Fatal("a step regression must not flag the total")
	}
}

func TestCompareRunsTrendOverview(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	tt := newTest(t, st, "trend")
	other := newTest(t, st, "zz-never-ran")
	_ = other
	r1, _, _ := seedRun(t, st, tt.ID, 1, "one")
	r2, _, _ := seedRun(t, st, tt.ID, 1.5, "two")
	// Make r2 newer.
	r2.StartedAt = t0.Add(time.Hour)
	if err := st.UpdateRun(ctx, r2); err != nil {
		t.Fatal(err)
	}
	active, _ := st.CreateRun(ctx, model.Run{TestID: tt.ID, Status: model.RunRunning, YAML: reportYAML, StartedAt: t0.Add(2 * time.Hour)})
	if _, err := st.SetBaseline(ctx, tt.ID, r1.ID); err != nil {
		t.Fatal(err)
	}
	rp := New(st)

	// Compare.
	cr, err := rp.CompareRuns(ctx, []string{r1.ID, r2.ID}, Steady, DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}
	if len(cr.Runs) != 2 || len(cr.Comparisons) != 1 || cr.Comparisons[0].ReferenceRunID != r1.ID ||
		cr.Comparisons[0].CandidateRunID != r2.ID || !cr.Comparisons[0].Regression || cr.Runs[1].Baseline != nil {
		t.Fatalf("compare = %+v", cr)
	}
	if _, err := rp.CompareRuns(ctx, []string{r1.ID}, Steady, 0.1); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("one run err = %v", err)
	}
	if _, err := rp.CompareRuns(ctx, []string{r1.ID, "missing"}, Steady, 0.1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing run err = %v", err)
	}

	// Trend: finished runs only, oldest first.
	tr, err := rp.Trend(ctx, tt.ID, Steady, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 2 || tr[0].RunID != r1.ID || tr[1].RunID != r2.ID || !tr[0].IsBaseline || tr[1].IsBaseline ||
		tr[0].Label != "one" || tr[0].Summary.ByStep != nil || tr[0].Summary.Requests != 2000 {
		t.Fatalf("trend = %+v", tr)
	}
	tr, _ = rp.Trend(ctx, tt.ID, Steady, 1)
	if len(tr) != 1 || tr[0].RunID != r2.ID {
		t.Fatalf("trend limit 1 = %+v", tr)
	}
	if _, err := rp.Trend(ctx, "missing", Steady, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("trend unknown test err = %v", err)
	}

	// Overview.
	now := t0.Add(3 * time.Hour)
	ov, err := rp.Overview(ctx, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Runs != 3 || ov.Failed != 2 || ov.Passed != 0 || ov.Errored != 0 || ov.Requests != 4400 {
		t.Fatalf("overview counts = %+v", ov)
	}
	if !ov.Since.Equal(now.Add(-30*24*time.Hour)) || len(ov.RunsPerDay) != 31 || ov.RunsPerDay[30].Day != "2026-03-01" ||
		ov.RunsPerDay[30].Failed != 2 || ov.RunsPerDay[30].Other != 1 {
		t.Fatalf("runs per day = %+v", ov.RunsPerDay)
	}
	if len(ov.RecentRuns) != 3 || ov.RecentRuns[0].ID != active.ID || ov.RecentRuns[1].Summary.ByStep != nil {
		t.Fatalf("recent = %+v", ov.RecentRuns)
	}
	if len(ov.Tests) != 2 || ov.Tests[0].TestID != tt.ID || ov.Tests[1].TestID != other.ID || ov.Tests[1].LatestRun != nil {
		t.Fatalf("tests = %+v", ov.Tests)
	}
	h := ov.Tests[0]
	if h.RunCount != 3 || h.PassRate != 0 || h.LatestRun == nil || h.LatestRun.ID != active.ID ||
		h.Baseline == nil || h.Baseline.CandidateRunID != r2.ID || h.Baseline.ReferenceRunID != r1.ID || h.Baseline.ByStep != nil {
		t.Fatalf("health = %+v baseline %+v", h, h.Baseline)
	}
	ov, _ = rp.Overview(ctx, 1, t0.Add(40*24*time.Hour))
	if ov.Runs != 0 || len(ov.RunsPerDay) != 2 || ov.Tests[0].RunCount != 0 || ov.Tests[0].Baseline != nil {
		t.Fatalf("empty period overview = %+v", ov)
	}
}
