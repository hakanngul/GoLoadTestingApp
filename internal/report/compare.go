package report

import (
	"math"
	"sort"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// eps absorbs floating-point noise so a change of exactly the tolerance
// counts as "same".
const eps = 1e-9

type metricDef struct {
	name         string
	get          func(model.Summary) float64
	higherBetter bool
	absolute     bool // error_rate: absolute delta, tolerance/10
}

var metricDefs = []metricDef{
	{name: "p50_ms", get: func(s model.Summary) float64 { return s.P50Ms }},
	{name: "p90_ms", get: func(s model.Summary) float64 { return s.P90Ms }},
	{name: "p95_ms", get: func(s model.Summary) float64 { return s.P95Ms }},
	{name: "p99_ms", get: func(s model.Summary) float64 { return s.P99Ms }},
	{name: "max_ms", get: func(s model.Summary) float64 { return s.MaxMs }},
	{name: "mean_ms", get: func(s model.Summary) float64 { return s.MeanMs }},
	{name: "rps", get: func(s model.Summary) float64 { return s.RPS }, higherBetter: true},
	{name: "error_rate", get: func(s model.Summary) float64 { return s.ErrorRate }, absolute: true},
}

// noisyMetrics are reported but never make a comparison a regression.
var noisyMetrics = map[string]bool{"max_ms": true}

// Compare compares cand against ref.
//
// Latency metrics (p50/p90/p95/p99/max/mean, lower is better) and rps
// (higher is better) use the relative delta (cand-ref)/ref: worse when it
// exceeds tol in the bad direction, better when it exceeds tol in the good
// direction, else same; n/a (delta 0) when ref is 0. error_rate uses the
// absolute delta cand-ref: worse above tol/10, better below -tol/10.
// Regression is true when any total metric except max_ms is worse. ByStep
// compares the steps present in both summaries. Run IDs are left empty
// for the caller to fill.
func Compare(ref, cand model.Summary, tol float64) model.Comparison {
	tol = normTolerance(tol)
	out := model.Comparison{Tolerance: tol, Metrics: compareMetrics(ref, cand, tol)}
	for _, m := range out.Metrics {
		if m.Verdict == model.VerdictWorse && !noisyMetrics[m.Metric] {
			out.Regression = true
		}
	}
	var steps []string
	for name := range cand.ByStep {
		if _, ok := ref.ByStep[name]; ok {
			steps = append(steps, name)
		}
	}
	sort.Strings(steps)
	if len(steps) > 0 {
		out.ByStep = make(map[string][]model.MetricDelta, len(steps))
		for _, name := range steps {
			out.ByStep[name] = compareMetrics(ref.ByStep[name], cand.ByStep[name], tol)
		}
	}
	return out
}

func normTolerance(tol float64) float64 {
	if math.IsNaN(tol) || math.IsInf(tol, 0) || tol < 0 {
		return DefaultTolerance
	}
	return tol
}

func compareMetrics(ref, cand model.Summary, tol float64) []model.MetricDelta {
	out := make([]model.MetricDelta, 0, len(metricDefs))
	for _, d := range metricDefs {
		r, c := d.get(ref), d.get(cand)
		md := model.MetricDelta{Metric: d.name, Reference: r, Candidate: c, Verdict: model.VerdictSame}
		if d.absolute {
			md.Delta = c - r
			limit := tol / 10
			switch {
			case md.Delta > limit+eps:
				md.Verdict = model.VerdictWorse
			case md.Delta < -limit-eps:
				md.Verdict = model.VerdictBetter
			}
			out = append(out, md)
			continue
		}
		if r == 0 {
			md.Verdict = model.VerdictNA
			out = append(out, md)
			continue
		}
		md.Delta = (c - r) / r
		up, down := md.Delta > tol+eps, md.Delta < -tol-eps
		switch {
		case up && d.higherBetter, down && !d.higherBetter:
			md.Verdict = model.VerdictBetter
		case up, down:
			md.Verdict = model.VerdictWorse
		}
		out = append(out, md)
	}
	return out
}
