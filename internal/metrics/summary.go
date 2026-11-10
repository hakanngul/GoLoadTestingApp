package metrics

import (
	"fmt"
	"math"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// Summary is a human-oriented digest of a Snapshot. Latency fields are
// derived from the (merged) histogram. ByStep is populated only on the
// top-level summary; nested summaries have a nil ByStep and zero Dropped.
type Summary struct {
	Requests, Errors, Dropped int64
	ErrorRate, RPS            float64
	Mean, P50, P90, P95, P99  time.Duration
	Max                       time.Duration
	ByStep                    map[string]Summary // nil inside nested summaries
}

// Summarize computes a Summary of s. RPS is Requests divided by the
// snapshot's duration (0 when the duration is not positive). A nil
// snapshot yields the zero Summary.
func Summarize(s *Snapshot) Summary {
	if s == nil {
		return Summary{}
	}
	dur := s.End.Sub(s.Start)
	sum := summarizeStats(s.Total, dur)
	sum.Dropped = s.Dropped
	sum.ByStep = make(map[string]Summary, len(s.ByStep))
	for name, st := range s.ByStep {
		sum.ByStep[name] = summarizeStats(st, dur)
	}
	return sum
}

func summarizeStats(st *Stats, dur time.Duration) Summary {
	var sum Summary
	if st == nil {
		return sum
	}
	sum.Requests = st.Requests
	sum.Errors = st.Errors
	if st.Requests > 0 {
		sum.ErrorRate = float64(st.Errors) / float64(st.Requests)
	}
	if secs := dur.Seconds(); secs > 0 {
		sum.RPS = float64(st.Requests) / secs
	}
	if h := st.Hist; h != nil && h.TotalCount() > 0 {
		sum.Mean = microsToDuration(h.Mean())
		sum.P50 = microsToDuration(float64(h.ValueAtQuantile(50)))
		sum.P90 = microsToDuration(float64(h.ValueAtQuantile(90)))
		sum.P95 = microsToDuration(float64(h.ValueAtQuantile(95)))
		sum.P99 = microsToDuration(float64(h.ValueAtQuantile(99)))
		sum.Max = microsToDuration(float64(h.Max()))
	}
	return sum
}

func microsToDuration(us float64) time.Duration {
	return time.Duration(math.Round(us * float64(time.Microsecond)))
}

// ThresholdResult is the outcome of evaluating one threshold. Actual is in
// the same unit as Threshold.Value (milliseconds for latency metrics, a
// fraction for error_rate, requests/s for rps).
type ThresholdResult struct {
	Threshold scenario.Threshold
	Actual    float64
	Passed    bool
}

// metricValue returns the value of a threshold metric in its native unit.
func metricValue(sum Summary, metric string) (float64, error) {
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	switch metric {
	case "latency.p50":
		return ms(sum.P50), nil
	case "latency.p90":
		return ms(sum.P90), nil
	case "latency.p95":
		return ms(sum.P95), nil
	case "latency.p99":
		return ms(sum.P99), nil
	case "latency.mean":
		return ms(sum.Mean), nil
	case "latency.max":
		return ms(sum.Max), nil
	case "error_rate":
		return sum.ErrorRate, nil
	case "rps":
		return sum.RPS, nil
	}
	return 0, fmt.Errorf("metrics: unknown threshold metric %q", metric)
}

// compare applies op to actual and limit.
func compare(op string, actual, limit float64) (bool, error) {
	switch op {
	case "lt":
		return actual < limit, nil
	case "lte":
		return actual <= limit, nil
	case "gt":
		return actual > limit, nil
	case "gte":
		return actual >= limit, nil
	}
	return false, fmt.Errorf("metrics: unknown threshold op %q", op)
}

// EvaluateThresholds checks every threshold against sum, in order. It
// returns an error (and no results) if any threshold names an unknown
// metric or operator, so that typos cannot silently pass.
func EvaluateThresholds(sum Summary, th []scenario.Threshold) ([]ThresholdResult, error) {
	out := make([]ThresholdResult, 0, len(th))
	for i, t := range th {
		actual, err := metricValue(sum, t.Metric)
		if err != nil {
			return nil, fmt.Errorf("threshold %d: %w", i, err)
		}
		ok, err := compare(t.Op, actual, t.Value)
		if err != nil {
			return nil, fmt.Errorf("threshold %d (%s): %w", i, t.Metric, err)
		}
		out = append(out, ThresholdResult{Threshold: t, Actual: actual, Passed: ok})
	}
	return out, nil
}
