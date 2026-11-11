package mcpserver

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// The structs below are the compact JSON the tools return. Durations are
// in the unit their field name says (_ms, _s); error_rate is a fraction
// (0.01 = 1%).

func r2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

func r4(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10000) / 10000
}

// SummaryOut is the aggregate of a run or step.
type SummaryOut struct {
	Requests  int64   `json:"requests"`
	Errors    int64   `json:"errors"`
	Dropped   int64   `json:"dropped,omitempty"`
	RPS       float64 `json:"rps"`
	ErrorRate float64 `json:"error_rate"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	MaxMs     float64 `json:"max_ms,omitempty"`
}

func summaryOut(s *model.Summary) *SummaryOut {
	if s == nil {
		return nil
	}
	return &SummaryOut{
		Requests: s.Requests, Errors: s.Errors, Dropped: s.Dropped, RPS: r2(s.RPS), ErrorRate: r4(s.ErrorRate),
		P50Ms: r2(s.P50Ms), P95Ms: r2(s.P95Ms), P99Ms: r2(s.P99Ms), MaxMs: r2(s.MaxMs),
	}
}

// ThresholdOut is one threshold or regression result.
type ThresholdOut struct {
	Metric   string   `json:"metric"`
	Op       string   `json:"op"`
	Limit    float64  `json:"limit"`
	Actual   float64  `json:"actual"`
	Passed   bool     `json:"passed"`
	Baseline *float64 `json:"baseline,omitempty"`
}

// splitThresholds separates thresholds from regression-rule results (the
// latter carry the baseline value).
func splitThresholds(ts []model.ThresholdResult) (thresholds, regression []ThresholdOut) {
	for _, t := range ts {
		o := ThresholdOut{Metric: t.Metric, Op: t.Op, Limit: r4(t.Value), Actual: r4(t.Actual), Passed: t.Passed}
		if t.Baseline != nil {
			b := r4(*t.Baseline)
			o.Baseline = &b
			regression = append(regression, o)
		} else {
			thresholds = append(thresholds, o)
		}
	}
	return
}

// RunOut is a run without its scenario or metric series.
type RunOut struct {
	ID         string         `json:"id"`
	Test       string         `json:"test"`
	TestID     string         `json:"test_id"`
	Status     string         `json:"status"`
	Label      string         `json:"label,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	DurationS  float64        `json:"duration_s,omitempty"`
	Summary    *SummaryOut    `json:"summary,omitempty"`
	Thresholds []ThresholdOut `json:"thresholds,omitempty"`
	Regression []ThresholdOut `json:"regression,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Error      string         `json:"error,omitempty"`
	PeakCPU    float64        `json:"peak_cpu,omitempty"`
	URL        string         `json:"url,omitempty"`
}

func (h *handlers) runOut(r model.Run, full bool) RunOut {
	o := RunOut{
		ID: r.ID, Test: r.TestName, TestID: r.TestID, Status: string(r.Status), Label: r.Label,
		StartedAt: r.StartedAt, Summary: summaryOut(r.Summary), Error: r.Error, URL: h.url("/runs/" + r.ID),
	}
	if r.FinishedAt != nil {
		o.DurationS = r2(r.FinishedAt.Sub(r.StartedAt).Seconds())
	}
	if full {
		o.Thresholds, o.Regression = splitThresholds(r.Thresholds)
		o.Warnings = r.Warnings
		o.PeakCPU = r2(r.PeakCPU)
	}
	return o
}

// ErrorGroupOut is a group of identical failures.
type ErrorGroupOut struct {
	Step    string `json:"step"`
	Status  int    `json:"status"` // 0 = transport error or timeout
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

func topErrorGroups(gs []model.ErrorGroup, n int) []ErrorGroupOut {
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].Count > gs[j].Count })
	var out []ErrorGroupOut
	for _, g := range gs {
		if len(out) == n {
			break
		}
		out = append(out, ErrorGroupOut{Step: g.Step, Status: g.Status, Message: cut(g.Message, 200), Count: g.Count})
	}
	return out
}

// StepOut is one step's aggregate.
type StepOut struct {
	Step      string  `json:"step"`
	Requests  int64   `json:"requests"`
	ErrorRate float64 `json:"error_rate"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

// slowestSteps returns the n steps with the highest p95.
func slowestSteps(by map[string]model.Summary, n int) []StepOut {
	out := make([]StepOut, 0, len(by))
	for name, s := range by {
		out = append(out, StepOut{Step: name, Requests: s.Requests, ErrorRate: r4(s.ErrorRate), P50Ms: r2(s.P50Ms), P95Ms: r2(s.P95Ms), P99Ms: r2(s.P99Ms)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P95Ms != out[j].P95Ms {
			return out[i].P95Ms > out[j].P95Ms
		}
		return out[i].Step < out[j].Step
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// DeltaOut is one metric of a comparison; Delta is relative to the
// reference (error_rate: absolute, as a fraction).
type DeltaOut struct {
	Metric    string  `json:"metric"`
	Reference float64 `json:"reference"`
	Candidate float64 `json:"candidate"`
	Delta     float64 `json:"delta"`
	Verdict   string  `json:"verdict"`
}

// ComparisonOut compares a candidate run with a reference run.
type ComparisonOut struct {
	ReferenceRunID string     `json:"reference_run_id"`
	CandidateRunID string     `json:"candidate_run_id"`
	Regression     bool       `json:"regression"`
	Metrics        []DeltaOut `json:"metrics"`
	// WorseSteps lists up to 5 steps with a worse metric ("GET /x: p95_ms +32%").
	WorseSteps []string `json:"worse_steps,omitempty"`
}

func comparisonOut(c model.Comparison) ComparisonOut {
	o := ComparisonOut{ReferenceRunID: c.ReferenceRunID, CandidateRunID: c.CandidateRunID, Regression: c.Regression, Metrics: []DeltaOut{}}
	for _, m := range c.Metrics {
		o.Metrics = append(o.Metrics, DeltaOut{Metric: m.Metric, Reference: r4(m.Reference), Candidate: r4(m.Candidate), Delta: r4(m.Delta), Verdict: string(m.Verdict)})
	}
	var names []string
	for n := range c.ByStep {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, m := range c.ByStep[n] {
			if m.Verdict == model.VerdictWorse && len(o.WorseSteps) < 5 {
				o.WorseSteps = append(o.WorseSteps, fmt.Sprintf("%s: %s %+.0f%%", n, m.Metric, m.Delta*100))
			}
		}
	}
	return o
}

// verdict summarises thresholds and regression results of a finished run.
func verdict(status model.RunStatus, ts []model.ThresholdResult) string {
	if !status.Finished() {
		return string(status)
	}
	for _, t := range ts {
		if !t.Passed {
			return "failed"
		}
	}
	if status == model.RunError || status == model.RunStopped {
		return string(status)
	}
	return "passed"
}

// SuiteRunItemOut is one test of a suite run.
type SuiteRunItemOut struct {
	Test    string      `json:"test"`
	Status  string      `json:"status"`
	RunID   string      `json:"run_id,omitempty"`
	Error   string      `json:"error,omitempty"`
	Summary *SummaryOut `json:"summary,omitempty"`
}

// SuiteRunOut is a suite run.
type SuiteRunOut struct {
	ID        string            `json:"id"`
	Suite     string            `json:"suite"`
	SuiteID   string            `json:"suite_id"`
	Status    string            `json:"status"`
	Mode      string            `json:"mode"`
	Label     string            `json:"label,omitempty"`
	StartedAt time.Time         `json:"started_at"`
	DurationS float64           `json:"duration_s,omitempty"`
	Items     []SuiteRunItemOut `json:"items"`
	Error     string            `json:"error,omitempty"`
	URL       string            `json:"url,omitempty"`
}

func (h *handlers) suiteRunOut(s model.SuiteRun) SuiteRunOut {
	o := SuiteRunOut{
		ID: s.ID, Suite: s.SuiteName, SuiteID: s.SuiteID, Status: string(s.Status), Mode: string(s.Mode),
		Label: s.Label, StartedAt: s.StartedAt, Error: s.Error, URL: h.url("/suite-runs/" + s.ID),
		Items: []SuiteRunItemOut{},
	}
	if s.FinishedAt != nil {
		o.DurationS = r2(s.FinishedAt.Sub(s.StartedAt).Seconds())
	}
	for _, it := range s.Items {
		name := it.TestName
		if name == "" {
			name = "(deleted test)"
		}
		o.Items = append(o.Items, SuiteRunItemOut{Test: name, Status: it.Status, RunID: it.RunID, Error: it.Error, Summary: summaryOut(it.Summary)})
	}
	return o
}

// suiteRunLine is the one-line text summary of a suite run.
func suiteRunLine(s SuiteRunOut) string {
	pass, fail, other := 0, 0, 0
	for _, it := range s.Items {
		switch it.Status {
		case string(model.RunPassed):
			pass++
		case string(model.RunFailed), string(model.RunError):
			fail++
		default:
			other++
		}
	}
	return fmt.Sprintf("Suite run %s (%s) is %s: %d passed, %d failed/error, %d other of %d tests.", s.ID, s.Suite, s.Status, pass, fail, other, len(s.Items))
}

// runLine is the one-line text summary of a run.
func runLine(r RunOut) string {
	line := fmt.Sprintf("Run %s (%s) is %s", r.ID, r.Test, r.Status)
	if s := r.Summary; s != nil {
		line += fmt.Sprintf(": %d requests, %.1f rps, error rate %.2f%%, p95 %.0f ms", s.Requests, s.RPS, s.ErrorRate*100, s.P95Ms)
	}
	for _, t := range r.Thresholds {
		if !t.Passed {
			line += fmt.Sprintf("; threshold failed: %s %s %g (actual %g)", t.Metric, t.Op, t.Limit, t.Actual)
		}
	}
	for _, t := range r.Regression {
		if !t.Passed {
			line += fmt.Sprintf("; regression: %s (actual %g)", t.Metric, t.Actual)
		}
	}
	if r.Error != "" {
		line += "; error: " + r.Error
	}
	return line + "."
}

// cut shortens s to at most n bytes at a rune boundary, marking the cut.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// url joins a UI path to the server URL.
func (h *handlers) url(path string) string {
	if h.client.BaseURL == "" {
		return ""
	}
	return strings.TrimRight(h.client.BaseURL, "/") + path
}
