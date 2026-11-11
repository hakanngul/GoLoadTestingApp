// Package regression evaluates a scenario's regression rules: it compares
// the whole-run summary of a finished run with the summary of the test's
// baseline run and yields one model.ThresholdResult per rule.
package regression

import (
	"fmt"
	"math"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// Suffix is appended to the metric name of every result, e.g.
// "p95_ms vs baseline".
const Suffix = " vs baseline"

// Warning texts used when the check cannot run (the run is not failed).
const (
	WarnNoBaseline        = "regression check skipped: no baseline run"
	WarnBaselineNoSummary = "regression check skipped: baseline run has no summary"
)

// Check decides whether the rules can be evaluated for run runID against
// base (nil when the test has no baseline or it was deleted) and
// evaluates them. When they cannot, results is nil and warning says why.
// The baseline must not be the run itself. No rules: nothing, no warning.
func Check(rules []scenario.RegressionRule, runID string, cur model.Summary, base *model.Run) (results []model.ThresholdResult, warning string) {
	if len(rules) == 0 {
		return nil, ""
	}
	if base == nil || base.ID == runID {
		return nil, WarnNoBaseline
	}
	if base.Summary == nil {
		return nil, WarnBaselineNoSummary
	}
	return Evaluate(rules, cur, *base.Summary), ""
}

// Evaluate checks every rule, in order. Increase limits give Op "lte" with
// Value = baseline*(1+pct/100) or baseline+abs; decrease limits give "gte"
// with baseline*(1-pct/100) or baseline-abs (never below 0). A percentage
// limit on a baseline of 0 is therefore 0: the metric may not move away
// from 0. Value is rounded to 3 decimals and the comparison uses the
// rounded limit. A rule with an unknown metric or no limit (Validate
// rejects both earlier) is reported as failed.
func Evaluate(rules []scenario.RegressionRule, cur, base model.Summary) []model.ThresholdResult {
	out := make([]model.ThresholdResult, 0, len(rules))
	for _, r := range rules {
		b, ok1 := metric(base, r.Metric)
		a, ok2 := metric(cur, r.Metric)
		res := model.ThresholdResult{Metric: r.Metric + Suffix, Op: "lte", Actual: a}
		if !ok1 || !ok2 {
			out = append(out, res)
			continue
		}
		bb := b
		res.Baseline = &bb
		var limit float64
		switch {
		case r.MaxIncreasePct != nil:
			limit = b * (1 + *r.MaxIncreasePct/100)
		case r.MaxIncrease != nil:
			limit = b + *r.MaxIncrease
		case r.MaxDecreasePct != nil:
			res.Op = "gte"
			limit = b * (1 - *r.MaxDecreasePct/100)
		case r.MaxDecrease != nil:
			res.Op = "gte"
			limit = b - *r.MaxDecrease
		default:
			out = append(out, res)
			continue
		}
		res.Value = round3(math.Max(limit, 0))
		if res.Op == "lte" {
			res.Passed = a <= res.Value
		} else {
			res.Passed = a >= res.Value
		}
		out = append(out, res)
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func metric(s model.Summary, name string) (float64, bool) {
	switch name {
	case "p50_ms":
		return s.P50Ms, true
	case "p90_ms":
		return s.P90Ms, true
	case "p95_ms":
		return s.P95Ms, true
	case "p99_ms":
		return s.P99Ms, true
	case "mean_ms":
		return s.MeanMs, true
	case "max_ms":
		return s.MaxMs, true
	case "error_rate":
		return s.ErrorRate, true
	case "rps":
		return s.RPS, true
	}
	return 0, false
}

// Describe renders a result as one text line, e.g.
// "p95_ms vs baseline lte 54 (baseline 45, actual 50)".
func Describe(r model.ThresholdResult) string {
	b := 0.0
	if r.Baseline != nil {
		b = *r.Baseline
	}
	return fmt.Sprintf("%s %s %g (baseline %g, actual %g)", r.Metric, r.Op, r.Value, b, r.Actual)
}
