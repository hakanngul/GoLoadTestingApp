// Package model holds the JSON shapes shared by the REST API, the store,
// the runner and the web UI (web/src/api/types.ts mirrors this file).
//
// CONTRACT FILE. All durations in JSON are milliseconds (float) and all
// timestamps are RFC 3339.
package model

import "time"

// Test is a saved scenario definition.
type Test struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	YAML      string     `json:"yaml"`
	Files     []FileInfo `json:"files"` // attached data files (CSV)
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastRun   *Run       `json:"last_run,omitempty"` // filled by ListTests only (Summary may be nil)
	// BaselineRunID is the reference run new runs are compared against
	// (empty = none). Cleared when that run is deleted.
	BaselineRunID string `json:"baseline_run_id,omitempty"`
}

// FileInfo describes a data file attached to a test. Scenario `data.file`
// refers to it by Name.
type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// RunStatus is the lifecycle state of a run.
type RunStatus string

const (
	RunQueued  RunStatus = "queued"
	RunRunning RunStatus = "running"
	RunPassed  RunStatus = "passed"  // finished, all thresholds passed
	RunFailed  RunStatus = "failed"  // finished, a threshold failed
	RunStopped RunStatus = "stopped" // stopped by user; partial results
	RunError   RunStatus = "error"   // could not run / crashed
)

// Finished reports whether the status is terminal.
func (s RunStatus) Finished() bool {
	return s == RunPassed || s == RunFailed || s == RunStopped || s == RunError
}

// Run is one execution of a test.
type Run struct {
	ID         string            `json:"id"`
	TestID     string            `json:"test_id"`
	TestName   string            `json:"test_name"`
	Status     RunStatus         `json:"status"`
	YAML       string            `json:"yaml"` // scenario frozen at start
	Workers    int               `json:"workers"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Summary    *Summary          `json:"summary,omitempty"`
	Thresholds []ThresholdResult `json:"thresholds"`
	PeakCPU    float64           `json:"peak_cpu"` // 0..1, highest load-generator CPU seen
	Warnings   []string          `json:"warnings"` // e.g. high CPU, missing worker
	Error      string            `json:"error,omitempty"`
	Label      string            `json:"label"` // short tag, e.g. "v1.4.2" (<= 100 chars)
	Notes      string            `json:"notes"` // free text (<= 10k chars)
}

// Summary is an aggregate over a time window (whole run or one step).
type Summary struct {
	Requests  int64              `json:"requests"`
	Errors    int64              `json:"errors"`
	Dropped   int64              `json:"dropped"`
	ErrorRate float64            `json:"error_rate"` // fraction
	RPS       float64            `json:"rps"`
	MeanMs    float64            `json:"mean_ms"`
	P50Ms     float64            `json:"p50_ms"`
	P90Ms     float64            `json:"p90_ms"`
	P95Ms     float64            `json:"p95_ms"`
	P99Ms     float64            `json:"p99_ms"`
	MaxMs     float64            `json:"max_ms"`
	ByStep    map[string]Summary `json:"by_step,omitempty"`
}

// ThresholdResult is a threshold and its evaluated outcome.
type ThresholdResult struct {
	Metric string  `json:"metric"`
	Op     string  `json:"op"`
	Value  float64 `json:"value"`
	Actual float64 `json:"actual"`
	Passed bool    `json:"passed"`
}

// MetricPoint is one second of a run (live stream and stored time series).
type MetricPoint struct {
	T          time.Time `json:"t"`
	Elapsed    float64   `json:"elapsed"` // seconds since run start
	TargetRate float64   `json:"target"`  // target rps (open) or VUs (closed)
	Active     int64     `json:"active"`  // in-flight iterations
	Requests   int64     `json:"requests"`
	Errors     int64     `json:"errors"`
	Dropped    int64     `json:"dropped"`
	RPS        float64   `json:"rps"`
	ErrorRate  float64   `json:"error_rate"`
	MeanMs     float64   `json:"mean_ms"`
	P50Ms      float64   `json:"p50_ms"`
	P95Ms      float64   `json:"p95_ms"`
	P99Ms      float64   `json:"p99_ms"`
	MaxMs      float64   `json:"max_ms"`
	CPU        float64   `json:"cpu"`     // 0..1, max across workers
	Workers    int       `json:"workers"` // connected workers this second
}

// ErrorSample is one failed request kept for diagnosis (bounded per run).
type ErrorSample struct {
	T       time.Time `json:"t"`
	Step    string    `json:"step"`
	Status  int       `json:"status"`
	Message string    `json:"message"`
}

// Worker is a load generator known to the master.
type Worker struct {
	ID       string    `json:"id"`
	Addr     string    `json:"addr"`
	State    string    `json:"state"` // idle, busy, lost
	RunID    string    `json:"run_id,omitempty"`
	CPU      float64   `json:"cpu"`
	LastSeen time.Time `json:"last_seen"`
	Version  string    `json:"version"`
}

// ValidateResult is returned by POST /api/validate.
type ValidateResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"` // "path: message"
	Model    string   `json:"model,omitempty"`
	Duration float64  `json:"duration_s,omitempty"`
	Steps    int      `json:"steps,omitempty"`
	// Warnings are problems that do not prevent a run, e.g. {{var}}
	// references no source defines ("path: message"). Always present.
	Warnings []string `json:"warnings"`
}

// ValidateRequest is the body of POST /api/validate. TestID (optional)
// lets the server read the header of the test's uploaded data file.
type ValidateRequest struct {
	YAML   string `json:"yaml"`
	TestID string `json:"test_id,omitempty"`
}

// TryRequest is the body of POST /api/tests/try: run one iteration of a
// (possibly unsaved) scenario. TestID (optional) lets the server use the
// test's uploaded data file (its first row supplies the {{vars}}).
type TryRequest struct {
	YAML   string `json:"yaml"`
	TestID string `json:"test_id,omitempty"`
}

// Try step states.
const (
	TryPassed = "passed"
	TryFailed = "failed"
	TryNotRun = "not_run"
)

// TryResult is the outcome of one trial iteration (POST /api/tests/try,
// `goloadtest try`).
type TryResult struct {
	// OK is true when every step ran and passed.
	OK         bool    `json:"ok"`
	DurationMs float64 `json:"duration_ms"`
	// Data is the CSV row used for {{vars}} (sensitive-looking values
	// masked); empty without data.
	Data map[string]string `json:"data"`
	// DataNote explains why no data row was used, if the scenario has a
	// data file that was not available.
	DataNote string    `json:"data_note,omitempty"`
	Steps    []TryStep `json:"steps"`
	Hints    []TryHint `json:"hints"`
}

// TryStep reports one step of a trial iteration (the login request of
// auth type login comes first, as step "auth:login").
type TryStep struct {
	Name string `json:"name"`
	// Kind is http, ws or login.
	Kind string `json:"kind"`
	// Index is the position in scenario steps (-1 for auth:login).
	Index int `json:"index"`
	// State is passed, failed or not_run.
	State          string      `json:"state"`
	Method         string      `json:"method,omitempty"`
	URL            string      `json:"url,omitempty"`
	RequestHeaders []TryHeader `json:"request_headers"`
	RequestBody    string      `json:"request_body,omitempty"`
	Status         int         `json:"status"`
	DurationMs     float64     `json:"duration_ms"`
	Bytes          int64       `json:"bytes"`
	// ResponseHeaders lists selected response headers (set-cookie: cookie
	// names only).
	ResponseHeaders []TryHeader `json:"response_headers"`
	// Body is the start of the response body (text only, capped).
	Body          string `json:"body,omitempty"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
	BodyBinary    bool   `json:"body_binary,omitempty"`
	// Location is the resolved redirect target of a 3xx response.
	Location   string         `json:"location,omitempty"`
	Extracted  []TryExtract   `json:"extracted"`
	Assertions []TryAssertion `json:"assertions"`
	// Messages are the per-message results of a ws step.
	Messages []TryMessage `json:"messages,omitempty"`
	Error    string       `json:"error,omitempty"`
}

// TryHeader is one header (values of sensitive headers are masked).
type TryHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// TryExtract is the outcome of one extract.
type TryExtract struct {
	Var    string `json:"var"`
	Source string `json:"source"`
	Expr   string `json:"expr"`
	Value  string `json:"value,omitempty"` // truncated / masked
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// TryAssertion is the outcome of one assertion.
type TryAssertion struct {
	Assertion string `json:"assertion"` // e.g. "status eq 200"
	Passed    bool   `json:"passed"`
	Message   string `json:"message,omitempty"`
}

// TryMessage is one ws message result ("<step>:<n>" or "<step>:hold").
type TryMessage struct {
	Name       string  `json:"name"`
	DurationMs float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

// TryHint is a diagnosis computed from a trial iteration.
type TryHint struct {
	// Level is error, warning or info.
	Level string `json:"level"`
	// Step names the step the hint is about ("" = whole scenario).
	Step    string  `json:"step,omitempty"`
	Message string  `json:"message"`
	Fix     *TryFix `json:"fix,omitempty"`
}

// TryFix is a suggested scenario edit that a client can apply.
type TryFix struct {
	// Kind is base_url (set target.base_url to Value) or step_path (set
	// the path of steps[Step], or of auth.login when Step is -1).
	Kind  string `json:"kind"`
	Step  int    `json:"step"`
	Value string `json:"value"`
	// Label is a short button text, e.g. "Use https://www.example.com".
	Label string `json:"label"`
}

// StartRunRequest is the body of POST /api/tests/{id}/runs.
type StartRunRequest struct {
	Workers int    `json:"workers"` // 0 => 1 (local)
	Label   string `json:"label,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// UpdateRunRequest is the body of PATCH /api/runs/{id}; nil = unchanged.
type UpdateRunRequest struct {
	Label *string `json:"label,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

// ---- Reporting ----

// WindowKind selects the part of a run a report aggregates.
type WindowKind string

const (
	WindowFull   WindowKind = "full"   // whole run
	WindowSteady WindowKind = "steady" // without the initial ramp-up and a final ramp-down (from the scenario stages)
	WindowCustom WindowKind = "custom" // [From, To) seconds since run start
)

// Window is a resolved time range of a run, in seconds since start.
type Window struct {
	Kind WindowKind `json:"kind"`
	From float64    `json:"from"`
	To   float64    `json:"to"`
}

// RunReport is a run aggregated exactly (merged HDR histograms, never
// averaged percentiles) over a window. GET /api/runs/{id}/report.
type RunReport struct {
	Run        Run               `json:"run"`
	Window     Window            `json:"window"`
	Summary    Summary           `json:"summary"`            // over Window (incl. by_step)
	Thresholds []ThresholdResult `json:"thresholds"`         // re-evaluated over Window
	Baseline   *Comparison       `json:"baseline,omitempty"` // vs the test's baseline run, same window kind
}

// Verdict classifies a metric change between a reference and a candidate.
type Verdict string

const (
	VerdictBetter Verdict = "better"
	VerdictSame   Verdict = "same"  // within tolerance
	VerdictWorse  Verdict = "worse" // regression
	VerdictNA     Verdict = "n/a"   // not comparable (e.g. zero reference)
)

// MetricDelta is one metric of a comparison. Delta is relative
// ((cand-ref)/ref) except for error_rate where it is absolute
// (percentage points as a fraction).
type MetricDelta struct {
	Metric    string  `json:"metric"` // p50_ms p90_ms p95_ms p99_ms max_ms mean_ms rps error_rate
	Reference float64 `json:"reference"`
	Candidate float64 `json:"candidate"`
	Delta     float64 `json:"delta"`
	Verdict   Verdict `json:"verdict"`
}

// Comparison compares a candidate run with a reference run.
type Comparison struct {
	ReferenceRunID string        `json:"reference_run_id"`
	CandidateRunID string        `json:"candidate_run_id"`
	Tolerance      float64       `json:"tolerance"` // relative, default 0.10; error_rate uses tolerance/10 absolute
	Metrics        []MetricDelta `json:"metrics"`
	Regression     bool          `json:"regression"` // any metric worse
	// ByStep compares steps present in both runs (same metrics).
	ByStep map[string][]MetricDelta `json:"by_step,omitempty"`
}

// CompareResult is GET /api/compare?runs=a,b,c&window=...&tolerance=...
// Runs[0] is the reference; Comparisons[i] compares Runs[i+1] to it.
type CompareResult struct {
	Runs        []RunReport  `json:"runs"`
	Comparisons []Comparison `json:"comparisons"`
}

// TrendPoint is one finished run of a test, aggregated over a window.
// GET /api/tests/{id}/trend?limit=&window= (oldest first).
type TrendPoint struct {
	RunID      string    `json:"run_id"`
	StartedAt  time.Time `json:"started_at"`
	Label      string    `json:"label"`
	Status     RunStatus `json:"status"`
	Workers    int       `json:"workers"`
	Summary    Summary   `json:"summary"` // without by_step
	IsBaseline bool      `json:"is_baseline"`
}

// Overview is GET /api/reports/overview: the reporting dashboard.
type Overview struct {
	Since      time.Time    `json:"since"` // ?days= window start (default 30 days)
	Runs       int          `json:"runs"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	Errored    int          `json:"errored"` // error + stopped
	Requests   int64        `json:"requests"`
	Tests      []TestHealth `json:"tests"`
	RecentRuns []Run        `json:"recent_runs"` // newest first, max 10, summaries without by_step
	RunsPerDay []DayCount   `json:"runs_per_day"`
}

// TestHealth is one row of the overview: a test's latest run vs baseline.
type TestHealth struct {
	TestID    string      `json:"test_id"`
	TestName  string      `json:"test_name"`
	LatestRun *Run        `json:"latest_run,omitempty"`
	Baseline  *Comparison `json:"baseline,omitempty"` // latest vs baseline (steady window)
	PassRate  float64     `json:"pass_rate"`          // passed / finished runs in the period
	RunCount  int         `json:"run_count"`
}

// DayCount is runs started on one UTC day, by outcome.
type DayCount struct {
	Day    string `json:"day"` // YYYY-MM-DD
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
	Other  int    `json:"other"`
}

// APIError is the body of every non-2xx response.
type APIError struct {
	Error   string   `json:"error"`
	Details []string `json:"details,omitempty"`
}

// ---- Live statistics (Locust-style live tables) ----

// LiveStats is sent on the run stream as `event: stats` once per second
// (and once right after subscribing) while a run is active. It is not
// stored per second; only the final error groups are persisted.
type LiveStats struct {
	Elapsed float64 `json:"elapsed"` // seconds since run start
	// Total is cumulative since the start of the run (by_step filled):
	// the live per-endpoint table. Percentiles come from merged HDR
	// histograms.
	Total Summary `json:"total"`
	// Current is the last one-second interval (by_step filled).
	Current Summary `json:"current"`
	// Errors groups every failed request so far (not just the sampled
	// ones), most frequent first, at most MaxErrorGroups entries; further
	// distinct errors are counted in a final group with Message
	// ErrorGroupOther.
	Errors []ErrorGroup `json:"errors"`
}

// ErrorGroup counts failed requests with the same step, status and
// (normalized) message.
type ErrorGroup struct {
	Step   string `json:"step"`
	Status int    `json:"status"` // 0 = transport error / timeout
	// Message is the normalized message: request-specific parts (IDs,
	// URL values, timestamps, ephemeral ports) replaced by placeholders
	// such as {n}, {uuid}, {id}, {v}, {ts}, {port} (see
	// metrics.NormalizeErrorMessage).
	Message string `json:"message"`
	// Sample is the first raw message seen for the group (cut to 300
	// runes); it may equal Message. Empty in groups stored before
	// normalization existed.
	Sample string    `json:"sample,omitempty"`
	Count  int64     `json:"count"`
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
}

// MaxErrorGroups caps the distinct error groups tracked per run.
const MaxErrorGroups = 100

// ErrorGroupOther is the Message of the overflow group.
const ErrorGroupOther = "(other errors)"
