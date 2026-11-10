package report

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// maxCacheEntries bounds the aggregate cache; when full it is reset.
const maxCacheEntries = 1024

// Compare limits for CompareRuns.
const (
	MinCompareRuns = 2
	MaxCompareRuns = 6
)

// ErrBadRequest is wrapped by argument errors other than windows (the API
// answers 400).
var ErrBadRequest = errors.New("report: bad request")

// Reporter builds reports from a store. Aggregates of finished runs are
// immutable and cached in memory (bounded), so trends and the overview do
// not re-merge every histogram on each request. Safe for concurrent use.
type Reporter struct {
	st store.Store

	mu    sync.Mutex
	cache map[cacheKey]aggregate
}

type cacheKey struct {
	runID    string
	kind     model.WindowKind
	from, to float64
}

// aggregate is a run summarised over a resolved window.
type aggregate struct {
	window     model.Window
	summary    model.Summary
	thresholds []model.ThresholdResult
}

// New returns a Reporter reading from st.
func New(st store.Store) *Reporter {
	return &Reporter{st: st, cache: make(map[cacheKey]aggregate)}
}

// Forget drops cached aggregates of a run (call after deleting it).
func (r *Reporter) Forget(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.cache {
		if k.runID == runID {
			delete(r.cache, k)
		}
	}
}

// BuildRunReport is RunReport without caching.
func BuildRunReport(ctx context.Context, st store.Store, runID string, w WindowSpec, tolerance float64) (model.RunReport, error) {
	return New(st).RunReport(ctx, runID, w, tolerance)
}

// RunReport aggregates a run exactly over a window, re-evaluates the
// scenario thresholds over it and, when the run's test has a baseline
// other than this run, compares the run with it (same window request,
// resolved per run).
//
// When snapshots are missing or cannot be decoded, the stored run summary
// and thresholds are used and the window is reported as full.
func (r *Reporter) RunReport(ctx context.Context, runID string, w WindowSpec, tolerance float64) (model.RunReport, error) {
	run, err := r.st.GetRun(ctx, runID)
	if err != nil {
		return model.RunReport{}, err
	}
	rep, err := r.report(ctx, run, w)
	if err != nil {
		return model.RunReport{}, err
	}
	test, err := r.st.GetTest(ctx, run.TestID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return rep, nil
		}
		return model.RunReport{}, err
	}
	if test.BaselineRunID == "" || test.BaselineRunID == run.ID {
		return rep, nil
	}
	base, err := r.st.GetRun(ctx, test.BaselineRunID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) { // deleted meanwhile
			return rep, nil
		}
		return model.RunReport{}, err
	}
	bagg, err := r.aggregate(ctx, base, w)
	if err != nil {
		return model.RunReport{}, err
	}
	c := Compare(bagg.summary, rep.Summary, tolerance)
	c.ReferenceRunID, c.CandidateRunID = base.ID, run.ID
	rep.Baseline = &c
	return rep, nil
}

// report builds a RunReport without baseline.
func (r *Reporter) report(ctx context.Context, run model.Run, w WindowSpec) (model.RunReport, error) {
	agg, err := r.aggregate(ctx, run, w)
	if err != nil {
		return model.RunReport{}, err
	}
	return model.RunReport{
		Run:        normalizeRun(run),
		Window:     agg.window,
		Summary:    cloneSummary(agg.summary),
		Thresholds: append([]model.ThresholdResult{}, agg.thresholds...),
	}, nil
}

// aggregate summarises run over w, using the cache for finished runs.
func (r *Reporter) aggregate(ctx context.Context, run model.Run, w WindowSpec) (aggregate, error) {
	key := cacheKey{runID: run.ID, kind: w.Kind}
	if w.Kind == model.WindowCustom {
		key.from, key.to = w.From, w.To
	}
	cacheable := run.Status.Finished()
	if cacheable {
		r.mu.Lock()
		a, ok := r.cache[key]
		r.mu.Unlock()
		if ok {
			return a, nil
		}
	}
	a, err := r.compute(ctx, run, w)
	if err != nil {
		return aggregate{}, err
	}
	if cacheable {
		r.mu.Lock()
		if len(r.cache) >= maxCacheEntries {
			r.cache = make(map[cacheKey]aggregate)
		}
		r.cache[key] = a
		r.mu.Unlock()
	}
	return a, nil
}

func (r *Reporter) compute(ctx context.Context, run model.Run, w WindowSpec) (aggregate, error) {
	pts, snaps, err := r.st.ListMetricsWithSnapshots(ctx, run.ID)
	if err != nil {
		return aggregate{}, err
	}
	dur := runDuration(run, pts)
	sc, perr := scenario.Parse([]byte(run.YAML))
	var stages []scenario.Stage
	if perr == nil {
		stages = sc.Load.Stages
	}
	win, err := ResolveWindow(w.Kind, w.From, w.To, stages, dur)
	if err != nil {
		return aggregate{}, err
	}
	fallback := func() aggregate {
		a := aggregate{window: model.Window{Kind: model.WindowFull, From: 0, To: dur},
			thresholds: append([]model.ThresholdResult{}, run.Thresholds...)}
		if run.Summary != nil {
			a.summary = cloneSummary(*run.Summary)
		}
		return a
	}
	if len(pts) == 0 || len(snaps) != len(pts) {
		return fallback(), nil
	}
	merged, ok := mergeWindow(pts, snaps, win)
	if !ok {
		return fallback(), nil
	}
	sum := metrics.Summarize(merged)
	a := aggregate{window: win, summary: ToModelSummary(sum)}
	a.thresholds = append([]model.ThresholdResult{}, run.Thresholds...)
	if perr == nil {
		if res, err := metrics.EvaluateThresholds(sum, sc.Thresholds); err == nil {
			a.thresholds = make([]model.ThresholdResult, 0, len(res))
			for _, t := range res {
				a.thresholds = append(a.thresholds, model.ThresholdResult{
					Metric: t.Threshold.Metric, Op: t.Threshold.Op, Value: t.Threshold.Value,
					Actual: t.Actual, Passed: t.Passed,
				})
			}
		}
	}
	return a, nil
}

// mergeWindow merges the snapshots of the points inside win into one
// snapshot spanning exactly the window length. ok is false when an
// included snapshot is missing or undecodable.
func mergeWindow(pts []model.MetricPoint, snaps [][]byte, win model.Window) (*metrics.Snapshot, bool) {
	merged := &metrics.Snapshot{Total: metrics.NewStats(), ByStep: map[string]*metrics.Stats{}}
	for i, in := range pointsInWindow(pts, win) {
		if !in {
			continue
		}
		if len(snaps[i]) == 0 {
			return nil, false
		}
		var s metrics.Snapshot
		if err := s.UnmarshalBinary(snaps[i]); err != nil {
			return nil, false
		}
		merged.Merge(&s)
	}
	merged.Start = time.Unix(0, 0).UTC()
	merged.End = merged.Start.Add(time.Duration(math.Round((win.To - win.From) * float64(time.Second))))
	return merged, true
}

// runDuration is the Elapsed of the last point, or FinishedAt-StartedAt
// when there are no points.
func runDuration(run model.Run, pts []model.MetricPoint) float64 {
	var d float64
	for _, p := range pts {
		d = math.Max(d, p.Elapsed)
	}
	if d == 0 && run.FinishedAt != nil {
		d = math.Max(0, run.FinishedAt.Sub(run.StartedAt).Seconds())
	}
	return d
}

// CompareRuns reports every run over the same window request and compares
// runs[1:] with runs[0]. Unknown runs yield store.ErrNotFound.
func (r *Reporter) CompareRuns(ctx context.Context, runIDs []string, w WindowSpec, tolerance float64) (model.CompareResult, error) {
	if len(runIDs) < MinCompareRuns || len(runIDs) > MaxCompareRuns {
		return model.CompareResult{}, fmt.Errorf("%w: compare needs %d to %d runs", ErrBadRequest, MinCompareRuns, MaxCompareRuns)
	}
	out := model.CompareResult{Runs: make([]model.RunReport, 0, len(runIDs)), Comparisons: []model.Comparison{}}
	for _, id := range runIDs {
		run, err := r.st.GetRun(ctx, id)
		if err != nil {
			return model.CompareResult{}, err
		}
		rep, err := r.report(ctx, run, w)
		if err != nil {
			return model.CompareResult{}, err
		}
		out.Runs = append(out.Runs, rep)
	}
	ref := out.Runs[0]
	for _, cand := range out.Runs[1:] {
		c := Compare(ref.Summary, cand.Summary, tolerance)
		c.ReferenceRunID, c.CandidateRunID = ref.Run.ID, cand.Run.ID
		out.Comparisons = append(out.Comparisons, c)
	}
	return out, nil
}

// Trend returns the newest limit finished runs of a test, oldest first,
// each aggregated over the window request (summaries without by_step).
func (r *Reporter) Trend(ctx context.Context, testID string, w WindowSpec, limit int) ([]model.TrendPoint, error) {
	test, err := r.st.GetTest(ctx, testID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	// Over-fetch so that a few active runs do not shorten the trend.
	runs, err := r.st.ListRuns(ctx, store.RunFilter{TestID: testID, Limit: limit + 50})
	if err != nil {
		return nil, err
	}
	var picked []model.Run
	for _, run := range runs {
		if run.Status.Finished() {
			picked = append(picked, run)
			if len(picked) == limit {
				break
			}
		}
	}
	out := make([]model.TrendPoint, 0, len(picked))
	for i := len(picked) - 1; i >= 0; i-- {
		run := picked[i]
		a, err := r.aggregate(ctx, run, w)
		if err != nil {
			return nil, err
		}
		s := a.summary
		s.ByStep = nil
		out = append(out, model.TrendPoint{
			RunID: run.ID, StartedAt: run.StartedAt, Label: run.Label, Status: run.Status,
			Workers: run.Workers, Summary: s, IsBaseline: run.ID == test.BaselineRunID,
		})
	}
	return out, nil
}

// Overview builds the reporting dashboard for runs started in the last
// days days before now. Per test it shows the latest run, the pass rate
// over the period and, when the test has a baseline, the newest finished
// run of the period compared with it over the steady window (default
// tolerance, without by_step). Tests are ordered by latest run, newest
// first; tests that never ran come last, by name.
func (r *Reporter) Overview(ctx context.Context, days int, now time.Time) (model.Overview, error) {
	if days <= 0 {
		days = 30
	}
	now = now.UTC()
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	runs, err := r.st.RunsSince(ctx, since)
	if err != nil {
		return model.Overview{}, err
	}
	tests, err := r.st.ListTests(ctx)
	if err != nil {
		return model.Overview{}, err
	}
	ov := model.Overview{Since: since, Runs: len(runs), Tests: []model.TestHealth{},
		RecentRuns: []model.Run{}, RunsPerDay: []model.DayCount{}}

	// Days, oldest first, every day of the period (zero days included).
	dayIdx := map[string]int{}
	for d := truncDay(since); !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		dayIdx[key] = len(ov.RunsPerDay)
		ov.RunsPerDay = append(ov.RunsPerDay, model.DayCount{Day: key})
	}

	type stats struct{ finished, passed, count int }
	perTest := map[string]*stats{}
	newestFinished := map[string]model.Run{}
	for _, run := range runs {
		switch run.Status {
		case model.RunPassed:
			ov.Passed++
		case model.RunFailed:
			ov.Failed++
		case model.RunError, model.RunStopped:
			ov.Errored++
		}
		if run.Summary != nil {
			ov.Requests += run.Summary.Requests
		}
		if len(ov.RecentRuns) < 10 {
			ov.RecentRuns = append(ov.RecentRuns, stripRun(run))
		}
		if i, ok := dayIdx[run.StartedAt.UTC().Format("2006-01-02")]; ok {
			switch run.Status {
			case model.RunPassed:
				ov.RunsPerDay[i].Passed++
			case model.RunFailed:
				ov.RunsPerDay[i].Failed++
			default:
				ov.RunsPerDay[i].Other++
			}
		}
		st := perTest[run.TestID]
		if st == nil {
			st = &stats{}
			perTest[run.TestID] = st
		}
		st.count++
		if run.Status.Finished() {
			st.finished++
			if run.Status == model.RunPassed {
				st.passed++
			}
			if _, ok := newestFinished[run.TestID]; !ok {
				newestFinished[run.TestID] = run // runs are newest first
			}
		}
	}

	for _, t := range tests {
		h := model.TestHealth{TestID: t.ID, TestName: t.Name}
		if t.LastRun != nil {
			lr := stripRun(*t.LastRun)
			h.LatestRun = &lr
		}
		if st := perTest[t.ID]; st != nil {
			h.RunCount = st.count
			if st.finished > 0 {
				h.PassRate = float64(st.passed) / float64(st.finished)
			}
		}
		if cand, ok := newestFinished[t.ID]; ok && t.BaselineRunID != "" && t.BaselineRunID != cand.ID {
			c, err := r.baselineComparison(ctx, t.BaselineRunID, cand)
			if err != nil && ctx.Err() != nil {
				return model.Overview{}, ctx.Err()
			}
			h.Baseline = c
		}
		ov.Tests = append(ov.Tests, h)
	}
	sort.SliceStable(ov.Tests, func(i, j int) bool {
		a, b := ov.Tests[i], ov.Tests[j]
		switch {
		case a.LatestRun != nil && b.LatestRun != nil:
			if !a.LatestRun.StartedAt.Equal(b.LatestRun.StartedAt) {
				return a.LatestRun.StartedAt.After(b.LatestRun.StartedAt)
			}
			return a.TestID < b.TestID
		case a.LatestRun != nil:
			return true
		case b.LatestRun != nil:
			return false
		}
		return strings.ToLower(a.TestName) < strings.ToLower(b.TestName)
	})
	return ov, nil
}

// baselineComparison compares cand with the baseline run over the steady
// window, without by_step. A missing baseline yields nil, nil.
func (r *Reporter) baselineComparison(ctx context.Context, baselineID string, cand model.Run) (*model.Comparison, error) {
	base, err := r.st.GetRun(ctx, baselineID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	ba, err := r.aggregate(ctx, base, Steady)
	if err != nil {
		return nil, err
	}
	ca, err := r.aggregate(ctx, cand, Steady)
	if err != nil {
		return nil, err
	}
	c := Compare(ba.summary, ca.summary, DefaultTolerance)
	c.ReferenceRunID, c.CandidateRunID = base.ID, cand.ID
	c.ByStep = nil
	return &c, nil
}

func truncDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// stripRun returns run with list fields non-nil and by_step removed.
func stripRun(run model.Run) model.Run {
	run = normalizeRun(run)
	if run.Summary != nil {
		s := *run.Summary
		s.ByStep = nil
		run.Summary = &s
	}
	return run
}

func normalizeRun(run model.Run) model.Run {
	if run.Thresholds == nil {
		run.Thresholds = []model.ThresholdResult{}
	}
	if run.Warnings == nil {
		run.Warnings = []string{}
	}
	return run
}

func cloneSummary(s model.Summary) model.Summary {
	if len(s.ByStep) == 0 {
		s.ByStep = nil
		return s
	}
	by := make(map[string]model.Summary, len(s.ByStep))
	for k, v := range s.ByStep {
		by[k] = v
	}
	s.ByStep = by
	return s
}

// ToModelSummary converts a metrics.Summary to its JSON shape (durations
// in milliseconds), including per-step summaries.
func ToModelSummary(s metrics.Summary) model.Summary {
	out := summaryFields(s)
	if len(s.ByStep) > 0 {
		out.ByStep = make(map[string]model.Summary, len(s.ByStep))
		for name, st := range s.ByStep {
			out.ByStep[name] = summaryFields(st)
		}
	}
	return out
}

func summaryFields(s metrics.Summary) model.Summary {
	return model.Summary{
		Requests: s.Requests, Errors: s.Errors, Dropped: s.Dropped,
		ErrorRate: s.ErrorRate, RPS: s.RPS,
		MeanMs: ms(s.Mean), P50Ms: ms(s.P50), P90Ms: ms(s.P90), P95Ms: ms(s.P95), P99Ms: ms(s.P99), MaxMs: ms(s.Max),
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
