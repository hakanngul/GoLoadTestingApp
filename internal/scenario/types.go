// Package scenario defines the YAML scenario format (frozen in Phase 0).
//
// CONTRACT FILE: field names and YAML keys are shared by engine, protocol,
// metrics and (later) the UI form. Change only with a plan update.
package scenario

import "time"

// Scenario is one load test definition loaded from a single YAML file.
type Scenario struct {
	Name       string      `yaml:"name"`
	Target     Target      `yaml:"target"`
	Load       Load        `yaml:"load"`
	Data       *Data       `yaml:"data,omitempty"`
	Steps      []Step      `yaml:"steps"`
	Thresholds []Threshold `yaml:"thresholds,omitempty"`
}

// Target holds connection-level settings shared by every step.
type Target struct {
	BaseURL            string            `yaml:"base_url"`
	Headers            map[string]string `yaml:"headers,omitempty"`
	Timeout            Duration          `yaml:"timeout,omitempty"` // default 30s
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify,omitempty"`
}

// LoadModel selects how load is generated.
type LoadModel string

const (
	// ModelOpen: arrival-rate model. Stage.Target is iterations per second.
	ModelOpen LoadModel = "open"
	// ModelClosed: fixed virtual users looping. Stage.Target is VU count.
	ModelClosed LoadModel = "closed"
)

// Load describes the load profile. Stages run in order; the target ramps
// linearly from the previous stage's target (0 for the first) to this one.
type Load struct {
	Model  LoadModel `yaml:"model"` // default open
	Stages []Stage   `yaml:"stages"`
	// MaxVUs caps concurrent in-flight iterations in the open model.
	// Arrivals beyond the cap are dropped and counted. Default 1000.
	MaxVUs int `yaml:"max_vus,omitempty"`
}

// Stage is one segment of the load profile.
type Stage struct {
	Duration Duration `yaml:"duration"`
	Target   int      `yaml:"target"`
}

// DataMode controls how CSV rows are handed to iterations.
type DataMode string

const (
	DataSequential DataMode = "sequential" // round-robin over rows (default)
	DataRandom     DataMode = "random"
	DataUnique     DataMode = "unique" // each row used at most once; exhausted => stop
)

// Data configures CSV-backed test data. Columns become template variables.
type Data struct {
	File string   `yaml:"file"`
	Mode DataMode `yaml:"mode,omitempty"`
}

// Step is one request in an iteration. Path, header values and Body may
// contain {{var}} placeholders (see Render).
type Step struct {
	Name      string            `yaml:"name"`
	Method    string            `yaml:"method"` // default GET
	Path      string            `yaml:"path"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Body      string            `yaml:"body,omitempty"`
	ThinkTime Duration          `yaml:"think_time,omitempty"` // sleep after the step
	Extract   []Extract         `yaml:"extract,omitempty"`
	Assert    []Assert          `yaml:"assert,omitempty"`
}

// ExtractSource is where an extracted value comes from.
type ExtractSource string

const (
	FromJSON   ExtractSource = "json"   // Expr: dot path, e.g. data.items[0].id
	FromHeader ExtractSource = "header" // Expr: header name
	FromRegex  ExtractSource = "regex"  // Expr: regex on body, first capture group
)

// Extract stores a value from the response into a variable.
type Extract struct {
	Var  string        `yaml:"var"`
	From ExtractSource `yaml:"from"`
	Expr string        `yaml:"expr"`
}

// AssertType selects what an assertion checks.
type AssertType string

const (
	AssertStatus       AssertType = "status"        // Value: status code
	AssertJSON         AssertType = "json"          // Expr: dot path, compared to Value
	AssertDuration     AssertType = "duration"      // Value: duration, e.g. 200ms
	AssertBodyContains AssertType = "body_contains" // Value: substring
)

// Assert is a per-request check. A failed assert marks the request failed.
type Assert struct {
	Type  AssertType `yaml:"type"`
	Expr  string     `yaml:"expr,omitempty"`
	Op    string     `yaml:"op,omitempty"` // eq (default), ne, lt, lte, gt, gte, contains
	Value string     `yaml:"value"`
}

// Threshold is a run-level pass/fail rule.
//
// Metric is one of:
//
//	latency.p50 latency.p90 latency.p95 latency.p99 latency.mean latency.max  (Value in milliseconds)
//	error_rate  (Value as fraction, 0.01 = 1%)
//	rps         (Value in requests per second)
//
// Op is one of lt, lte, gt, gte.
type Threshold struct {
	Metric string  `yaml:"metric"`
	Op     string  `yaml:"op"`
	Value  float64 `yaml:"value"`
}

// Duration is a time.Duration that unmarshals from YAML strings like "30s".
// UnmarshalYAML / MarshalYAML are implemented in duration.go.
type Duration struct{ time.Duration }
