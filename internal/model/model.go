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
}

// StartRunRequest is the body of POST /api/tests/{id}/runs.
type StartRunRequest struct {
	Workers int `json:"workers"` // 0 => 1 (local)
}

// APIError is the body of every non-2xx response.
type APIError struct {
	Error   string   `json:"error"`
	Details []string `json:"details,omitempty"`
}
