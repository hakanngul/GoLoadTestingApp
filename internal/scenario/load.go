package scenario

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Default values applied by ApplyDefaults (see types.go for documentation).
const (
	DefaultMaxVUs   = 1000
	DefaultTimeout  = 30 * time.Second
	DefaultMethod   = "GET"
	DefaultAssertOp = "eq"
)

// LoadFile reads and parses the scenario file at path. A relative Data.File
// is resolved against the directory containing the scenario file.
//
// (Named LoadFile rather than Load because the Load type already occupies
// that identifier in this package.)
func LoadFile(path string) (*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	s, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("scenario %s: %w", path, err)
	}
	if s.Data != nil && s.Data.File != "" && !filepath.IsAbs(s.Data.File) {
		s.Data.File = filepath.Join(filepath.Dir(path), s.Data.File)
	}
	return s, nil
}

// Parse decodes a scenario from YAML, applies defaults and validates it.
// Decoding is strict: unknown keys (typos) are reported as errors.
func Parse(b []byte) (*Scenario, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty scenario document")
		}
		return nil, fmt.Errorf("decode: %w", err)
	}
	s.ApplyDefaults()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// ApplyDefaults fills in documented defaults for unset fields and
// normalises HTTP methods to upper case. It is idempotent.
func (s *Scenario) ApplyDefaults() {
	if s.Load.Model == "" {
		s.Load.Model = ModelOpen
	}
	if s.Load.MaxVUs == 0 {
		s.Load.MaxVUs = DefaultMaxVUs
	}
	if s.Target.Timeout.Duration == 0 {
		s.Target.Timeout.Duration = DefaultTimeout
	}
	if s.Data != nil && s.Data.Mode == "" {
		s.Data.Mode = DataSequential
	}
	for i := range s.Steps {
		st := &s.Steps[i]
		st.Method = strings.ToUpper(strings.TrimSpace(st.Method))
		if st.Method == "" {
			st.Method = DefaultMethod
		}
		if st.Name == "" {
			st.Name = st.Method + " " + st.Path
		}
		for j := range st.Assert {
			if st.Assert[j].Op == "" {
				st.Assert[j].Op = DefaultAssertOp
			}
		}
	}
}

// TotalDuration is the sum of all stage durations.
func (l Load) TotalDuration() time.Duration {
	var total time.Duration
	for _, st := range l.Stages {
		total += st.Duration.Duration
	}
	return total
}

// FieldError is a single validation problem located by a YAML field path
// such as "steps[2].extract[0].from".
type FieldError struct {
	Path string
	Msg  string
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Msg }

var (
	validMethods = map[string]bool{
		"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
		"DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
	}
	validThresholdMetrics = map[string]bool{
		"latency.p50": true, "latency.p90": true, "latency.p95": true,
		"latency.p99": true, "latency.mean": true, "latency.max": true,
		"error_rate": true, "rps": true,
	}
	orderingOps = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true}
	// assertOps lists the operators accepted for each assertion type.
	assertOps = map[AssertType]map[string]bool{
		AssertStatus:       {"eq": true, "ne": true, "lt": true, "lte": true, "gt": true, "gte": true},
		AssertDuration:     {"lt": true, "lte": true, "gt": true, "gte": true},
		AssertJSON:         {"eq": true, "ne": true, "lt": true, "lte": true, "gt": true, "gte": true, "contains": true},
		AssertBodyContains: {"eq": true, "ne": true, "contains": true},
	}
)

// Validate checks the scenario for semantic errors. It reports every
// problem found (joined with errors.Join), each as a *FieldError. Validate
// expects defaults to have been applied (Parse does this).
func (s *Scenario) Validate() error {
	var errs []error
	add := func(path, format string, args ...any) {
		errs = append(errs, &FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(s.Name) == "" {
		add("name", "must not be empty")
	}

	// target
	if s.Target.BaseURL == "" {
		add("target.base_url", "must not be empty")
	} else if u, err := url.Parse(s.Target.BaseURL); err != nil {
		add("target.base_url", "invalid URL: %v", err)
	} else if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		add("target.base_url", "must be an absolute http or https URL, got %q", s.Target.BaseURL)
	}
	if s.Target.Timeout.Duration < 0 {
		add("target.timeout", "must not be negative")
	}
	for k := range s.Target.Headers {
		if strings.TrimSpace(k) == "" {
			add("target.headers", "header name must not be empty")
		}
	}

	// load
	switch s.Load.Model {
	case ModelOpen, ModelClosed:
	default:
		add("load.model", "must be %q or %q, got %q", ModelOpen, ModelClosed, s.Load.Model)
	}
	if s.Load.MaxVUs <= 0 {
		add("load.max_vus", "must be greater than 0")
	}
	if len(s.Load.Stages) == 0 {
		add("load.stages", "at least one stage is required")
	} else {
		anyPositive := false
		for i, st := range s.Load.Stages {
			p := fmt.Sprintf("load.stages[%d]", i)
			if st.Duration.Duration <= 0 {
				add(p+".duration", "must be greater than 0")
			}
			if st.Target < 0 {
				add(p+".target", "must not be negative")
			}
			if st.Target > 0 {
				anyPositive = true
			}
		}
		if !anyPositive {
			add("load.stages", "at least one stage must have target > 0")
		}
	}

	// data
	if s.Data != nil {
		if strings.TrimSpace(s.Data.File) == "" {
			add("data.file", "must not be empty")
		}
		switch s.Data.Mode {
		case DataSequential, DataRandom, DataUnique:
		default:
			add("data.mode", "must be one of sequential, random, unique; got %q", s.Data.Mode)
		}
	}

	// steps
	if len(s.Steps) == 0 {
		add("steps", "at least one step is required")
	}
	seen := make(map[string]int, len(s.Steps))
	for i := range s.Steps {
		errs = append(errs, validateStep(fmt.Sprintf("steps[%d]", i), &s.Steps[i])...)
		name := s.Steps[i].Name
		if prev, dup := seen[name]; dup {
			add(fmt.Sprintf("steps[%d].name", i), "duplicate step name %q (also used by steps[%d]); names must be unique", name, prev)
		} else {
			seen[name] = i
		}
	}

	// thresholds
	for i, th := range s.Thresholds {
		p := fmt.Sprintf("thresholds[%d]", i)
		if !validThresholdMetrics[th.Metric] {
			add(p+".metric", "unknown metric %q; want one of latency.p50, latency.p90, latency.p95, latency.p99, latency.mean, latency.max, error_rate, rps", th.Metric)
		}
		if !orderingOps[th.Op] {
			add(p+".op", "must be one of lt, lte, gt, gte; got %q", th.Op)
		}
		if th.Value < 0 {
			add(p+".value", "must not be negative")
		} else if th.Metric == "error_rate" && th.Value > 1 {
			add(p+".value", "error_rate is a fraction between 0 and 1, got %v", th.Value)
		}
	}

	return errors.Join(errs...)
}

func validateStep(p string, st *Step) []error {
	var errs []error
	add := func(path, format string, args ...any) {
		errs = append(errs, &FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(st.Name) == "" {
		add(p+".name", "must not be empty")
	}
	if !validMethods[st.Method] {
		add(p+".method", "unsupported HTTP method %q", st.Method)
	}
	if !strings.HasPrefix(st.Path, "/") && !strings.HasPrefix(st.Path, "{{") {
		add(p+".path", "must start with \"/\" or a {{variable}}, got %q", st.Path)
	}
	if st.ThinkTime.Duration < 0 {
		add(p+".think_time", "must not be negative")
	}
	for k := range st.Headers {
		if strings.TrimSpace(k) == "" {
			add(p+".headers", "header name must not be empty")
		}
	}

	for j, ex := range st.Extract {
		ep := fmt.Sprintf("%s.extract[%d]", p, j)
		if strings.TrimSpace(ex.Var) == "" {
			add(ep+".var", "must not be empty")
		}
		if ex.Expr == "" {
			add(ep+".expr", "must not be empty")
		}
		switch ex.From {
		case FromJSON, FromHeader:
		case FromRegex:
			if ex.Expr != "" {
				if re, err := regexp.Compile(ex.Expr); err != nil {
					add(ep+".expr", "invalid regex: %v", err)
				} else if re.NumSubexp() < 1 {
					add(ep+".expr", "regex must contain at least one capture group")
				}
			}
		default:
			add(ep+".from", "must be one of json, header, regex; got %q", ex.From)
		}
	}

	for j, as := range st.Assert {
		ap := fmt.Sprintf("%s.assert[%d]", p, j)
		ops, ok := assertOps[as.Type]
		if !ok {
			add(ap+".type", "must be one of status, json, duration, body_contains; got %q", as.Type)
			continue
		}
		if !ops[as.Op] {
			add(ap+".op", "operator %q is not valid for %s assertions", as.Op, as.Type)
		}
		switch as.Type {
		case AssertStatus:
			if code, err := strconv.Atoi(as.Value); err != nil {
				add(ap+".value", "status must be an integer, got %q", as.Value)
			} else if code < 100 || code > 599 {
				add(ap+".value", "status must be between 100 and 599, got %d", code)
			}
		case AssertDuration:
			if d, err := time.ParseDuration(as.Value); err != nil {
				add(ap+".value", "invalid duration %q (want e.g. \"200ms\")", as.Value)
			} else if d <= 0 {
				add(ap+".value", "duration must be greater than 0")
			}
		case AssertJSON:
			if strings.TrimSpace(as.Expr) == "" {
				add(ap+".expr", "json assertions require a path expression")
			}
			if orderingOps[as.Op] {
				if _, err := strconv.ParseFloat(as.Value, 64); err != nil {
					add(ap+".value", "operator %q requires a numeric value, got %q", as.Op, as.Value)
				}
			}
		case AssertBodyContains:
			if as.Value == "" {
				add(ap+".value", "must not be empty")
			}
		}
	}
	return errs
}
