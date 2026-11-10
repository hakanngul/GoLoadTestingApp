package scenario

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalYAML = `
name: t
target:
  base_url: https://api.example.test
load:
  stages:
    - duration: 10s
      target: 5
steps:
  - path: /health
`

func TestParseDefaults(t *testing.T) {
	s, err := Parse([]byte(minimalYAML + `
  - method: post
    path: /items
    assert:
      - type: status
        value: "201"
data:
  file: users.csv
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"model", s.Load.Model, ModelOpen},
		{"max_vus", s.Load.MaxVUs, 1000},
		{"timeout", s.Target.Timeout.Duration, 30 * time.Second},
		{"step0 method", s.Steps[0].Method, "GET"},
		{"step0 name", s.Steps[0].Name, "GET /health"},
		{"step1 method uppercased", s.Steps[1].Method, "POST"},
		{"step1 name", s.Steps[1].Name, "POST /items"},
		{"assert op", s.Steps[1].Assert[0].Op, "eq"},
		{"data mode", s.Data.Mode, DataSequential},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseKeepsExplicitValues(t *testing.T) {
	s, err := Parse([]byte(`
name: t
target:
  base_url: http://localhost:8080
  timeout: 5s
load:
  model: closed
  max_vus: 7
  stages:
    - {duration: 10s, target: 5}
    - {duration: 1m, target: 0}
steps:
  - name: custom
    method: DELETE
    path: /x
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Load.Model != ModelClosed || s.Load.MaxVUs != 7 || s.Target.Timeout.Duration != 5*time.Second || s.Steps[0].Name != "custom" {
		t.Fatalf("explicit values overwritten: %+v", s)
	}
	if got := s.Load.TotalDuration(); got != 70*time.Second {
		t.Fatalf("TotalDuration = %v, want 70s", got)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	tests := map[string]string{
		"top level": minimalYAML + "tresholds: []\n",
		"in step":   strings.Replace(minimalYAML, "  - path: /health", "  - path: /health\n    methd: GET", 1),
		"in target": strings.Replace(minimalYAML, "  base_url:", "  baseurl: x\n  base_url:", 1),
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("err = %v, want unknown-field error", err)
			}
		})
	}
}

func TestParseEmptyAndMalformed(t *testing.T) {
	for _, in := range []string{"", "   \n", "name: [unclosed"} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q): expected error", in)
		}
	}
}

// valid returns a fully valid, defaulted scenario for mutation in tests.
func valid() *Scenario {
	s := &Scenario{
		Name:   "ok",
		Target: Target{BaseURL: "https://api.example.test"},
		Load:   Load{Stages: []Stage{{Duration: Duration{time.Second}, Target: 1}}},
		Steps: []Step{{
			Path:    "/a",
			Extract: []Extract{{Var: "v", From: FromRegex, Expr: `id=(\d+)`}},
			Assert:  []Assert{{Type: AssertStatus, Value: "200"}},
		}},
		Thresholds: []Threshold{{Metric: "latency.p95", Op: "lt", Value: 100}},
		Data:       &Data{File: "x.csv"},
	}
	s.ApplyDefaults()
	return s
}

func TestValidateOK(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid scenario rejected: %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	d := func(v time.Duration) Duration { return Duration{v} }
	tests := []struct {
		name   string
		mutate func(s *Scenario)
		path   string
	}{
		{"empty name", func(s *Scenario) { s.Name = " " }, "name"},
		{"empty base_url", func(s *Scenario) { s.Target.BaseURL = "" }, "target.base_url"},
		{"relative base_url", func(s *Scenario) { s.Target.BaseURL = "/api" }, "target.base_url"},
		{"ftp base_url", func(s *Scenario) { s.Target.BaseURL = "ftp://x.test" }, "target.base_url"},
		{"bad base_url", func(s *Scenario) { s.Target.BaseURL = "http://[::1" }, "target.base_url"},
		{"negative timeout", func(s *Scenario) { s.Target.Timeout = d(-1) }, "target.timeout"},
		{"empty header name", func(s *Scenario) { s.Target.Headers = map[string]string{"": "x"} }, "target.headers"},
		{"no stages", func(s *Scenario) { s.Load.Stages = nil }, "load.stages"},
		{"zero duration", func(s *Scenario) { s.Load.Stages[0].Duration = d(0) }, "load.stages[0].duration"},
		{"negative target", func(s *Scenario) {
			s.Load.Stages = append(s.Load.Stages, Stage{Duration: d(time.Second), Target: -1})
		}, "load.stages[1].target"},
		{"all targets zero", func(s *Scenario) { s.Load.Stages[0].Target = 0 }, "load.stages"},
		{"bad model", func(s *Scenario) { s.Load.Model = "burst" }, "load.model"},
		{"max_vus zero", func(s *Scenario) { s.Load.MaxVUs = 0 }, "load.max_vus"},
		{"max_vus negative", func(s *Scenario) { s.Load.MaxVUs = -5 }, "load.max_vus"},
		{"no steps", func(s *Scenario) { s.Steps = nil }, "steps"},
		{"empty step name", func(s *Scenario) { s.Steps[0].Name = "" }, "steps[0].name"},
		{"bad path", func(s *Scenario) { s.Steps[0].Path = "api/x" }, "steps[0].path"},
		{"bad method", func(s *Scenario) { s.Steps[0].Method = "FETCH" }, "steps[0].method"},
		{"negative think", func(s *Scenario) { s.Steps[0].ThinkTime = d(-time.Second) }, "steps[0].think_time"},
		{"empty step header", func(s *Scenario) { s.Steps[0].Headers = map[string]string{" ": "x"} }, "steps[0].headers"},
		{"duplicate names", func(s *Scenario) {
			s.Steps = append(s.Steps, Step{Name: s.Steps[0].Name, Method: "GET", Path: "/b"})
		}, "steps[1].name"},
		{"extract var", func(s *Scenario) { s.Steps[0].Extract[0].Var = "" }, "steps[0].extract[0].var"},
		{"extract from", func(s *Scenario) { s.Steps[0].Extract[0].From = "xml" }, "steps[0].extract[0].from"},
		{"extract expr empty", func(s *Scenario) { s.Steps[0].Extract[0].Expr = "" }, "steps[0].extract[0].expr"},
		{"extract regex invalid", func(s *Scenario) { s.Steps[0].Extract[0].Expr = "(" }, "steps[0].extract[0].expr"},
		{"extract regex no group", func(s *Scenario) { s.Steps[0].Extract[0].Expr = `id=\d+` }, "steps[0].extract[0].expr"},
		{"assert type", func(s *Scenario) { s.Steps[0].Assert[0].Type = "header" }, "steps[0].assert[0].type"},
		{"assert op unknown", func(s *Scenario) { s.Steps[0].Assert[0].Op = "approx" }, "steps[0].assert[0].op"},
		{"status not int", func(s *Scenario) { s.Steps[0].Assert[0].Value = "OK" }, "steps[0].assert[0].value"},
		{"status range", func(s *Scenario) { s.Steps[0].Assert[0].Value = "999" }, "steps[0].assert[0].value"},
		{"status contains", func(s *Scenario) { s.Steps[0].Assert[0].Op = "contains" }, "steps[0].assert[0].op"},
		{"duration bad value", func(s *Scenario) {
			s.Steps[0].Assert[0] = Assert{Type: AssertDuration, Op: "lt", Value: "200"}
		}, "steps[0].assert[0].value"},
		{"duration eq op", func(s *Scenario) {
			s.Steps[0].Assert[0] = Assert{Type: AssertDuration, Op: "eq", Value: "200ms"}
		}, "steps[0].assert[0].op"},
		{"json no expr", func(s *Scenario) {
			s.Steps[0].Assert[0] = Assert{Type: AssertJSON, Op: "eq", Value: "x"}
		}, "steps[0].assert[0].expr"},
		{"json ordering non-numeric", func(s *Scenario) {
			s.Steps[0].Assert[0] = Assert{Type: AssertJSON, Expr: "a", Op: "gt", Value: "abc"}
		}, "steps[0].assert[0].value"},
		{"body_contains lt", func(s *Scenario) {
			s.Steps[0].Assert[0] = Assert{Type: AssertBodyContains, Op: "lt", Value: "x"}
		}, "steps[0].assert[0].op"},
		{"body_contains empty", func(s *Scenario) {
			s.Steps[0].Assert[0] = Assert{Type: AssertBodyContains, Op: "eq"}
		}, "steps[0].assert[0].value"},
		{"threshold metric", func(s *Scenario) { s.Thresholds[0].Metric = "latency.p42" }, "thresholds[0].metric"},
		{"threshold op", func(s *Scenario) { s.Thresholds[0].Op = "eq" }, "thresholds[0].op"},
		{"threshold negative", func(s *Scenario) { s.Thresholds[0].Value = -1 }, "thresholds[0].value"},
		{"error_rate > 1", func(s *Scenario) {
			s.Thresholds[0] = Threshold{Metric: "error_rate", Op: "lt", Value: 5}
		}, "thresholds[0].value"},
		{"data file", func(s *Scenario) { s.Data.File = "" }, "data.file"},
		{"data mode", func(s *Scenario) { s.Data.Mode = "shuffle" }, "data.mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid()
			tt.mutate(s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("expected error at %s", tt.path)
			}
			if !hasPath(err, tt.path) {
				t.Fatalf("no error at path %q; got: %v", tt.path, err)
			}
		})
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	s := valid()
	s.Name = ""
	s.Load.Model = "x"
	s.Steps[0].Extract[0].From = "xml"
	s.Thresholds[0].Op = "eq"
	err := s.Validate()
	for _, p := range []string{"name", "load.model", "steps[0].extract[0].from", "thresholds[0].op"} {
		if !hasPath(err, p) {
			t.Errorf("missing error for %s in: %v", p, err)
		}
	}
	if !strings.Contains(err.Error(), "steps[0].extract[0].from: ") {
		t.Errorf("message should be prefixed by field path: %v", err)
	}
}

func TestValidateAcceptsAllowedAssertOps(t *testing.T) {
	asserts := []Assert{
		{Type: AssertStatus, Op: "lt", Value: "500"},
		{Type: AssertDuration, Op: "lte", Value: "1s"},
		{Type: AssertJSON, Expr: "a.b", Op: "gte", Value: "1.5"},
		{Type: AssertJSON, Expr: "a.b", Op: "contains", Value: "x"},
		{Type: AssertJSON, Expr: "a.b", Op: "ne", Value: "x"},
		{Type: AssertBodyContains, Op: "contains", Value: "ok"},
	}
	s := valid()
	s.Steps[0].Assert = asserts
	s.Steps[0].Path = "{{base}}/x"
	if err := s.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func hasPath(err error, path string) bool {
	var stack = []error{err}
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var fe *FieldError
		if errors.As(e, &fe) && fe.Path == path {
			return true
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			stack = append(stack, j.Unwrap()...)
		}
	}
	return false
}

func TestLoadFileResolvesDataPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(p, []byte(minimalYAML+"data:\n  file: sub/users.csv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "sub", "users.csv"); s.Data.File != want {
		t.Fatalf("Data.File = %q, want %q", s.Data.File, want)
	}

	abs := filepath.Join(dir, "abs.csv")
	if err := os.WriteFile(p, []byte(minimalYAML+"data:\n  file: "+abs+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err = LoadFile(p); err != nil {
		t.Fatal(err)
	}
	if s.Data.File != abs {
		t.Fatalf("absolute Data.File changed to %q", s.Data.File)
	}

	if _, err := LoadFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestExamples is the Phase 0 exit criterion: every shipped example parses.
func TestExamples(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Fatalf("expected at least 3 examples, found %d", len(files))
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			s, err := LoadFile(f)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if s.Data != nil {
				if _, err := os.Stat(s.Data.File); err != nil {
					t.Fatalf("data file: %v", err)
				}
			}
		})
	}
}
