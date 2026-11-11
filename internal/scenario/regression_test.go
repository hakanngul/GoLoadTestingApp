package scenario

import "testing"

func TestParseRegression(t *testing.T) {
	s, err := Parse([]byte(minimalYAML + `regression:
  - {metric: p95_ms, max_increase_pct: 20}
  - {metric: error_rate, max_increase: 0.01}
  - {metric: rps, max_decrease_pct: 10}
  - {metric: mean_ms, max_decrease: 3}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(s.Regression) != 4 {
		t.Fatalf("got %d rules", len(s.Regression))
	}
	r := s.Regression
	if *r[0].MaxIncreasePct != 20 || *r[1].MaxIncrease != 0.01 || *r[2].MaxDecreasePct != 10 || *r[3].MaxDecrease != 3 {
		t.Fatalf("rules = %+v", r)
	}
}

func TestValidateRegressionErrors(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name string
		rule RegressionRule
		path string
	}{
		{"unknown metric", RegressionRule{Metric: "latency.p95", MaxIncreasePct: f(1)}, "regression[0].metric"},
		{"empty metric", RegressionRule{MaxIncreasePct: f(1)}, "regression[0].metric"},
		{"no limit", RegressionRule{Metric: "rps"}, "regression[0]"},
		{"two limits", RegressionRule{Metric: "rps", MaxIncreasePct: f(1), MaxDecrease: f(1)}, "regression[0]"},
		{"negative", RegressionRule{Metric: "rps", MaxDecrease: f(-1)}, "regression[0].max_decrease"},
		{"decrease pct over 100", RegressionRule{Metric: "rps", MaxDecreasePct: f(101)}, "regression[0].max_decrease_pct"},
		{"error_rate abs over 1", RegressionRule{Metric: "error_rate", MaxIncrease: f(2)}, "regression[0].max_increase"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			s.Regression = []RegressionRule{tc.rule}
			err := s.Validate()
			if err == nil || !hasPath(err, tc.path) {
				t.Fatalf("Validate() = %v, want error at %s", err, tc.path)
			}
		})
	}
	s := valid()
	s.Regression = []RegressionRule{{Metric: "p99_ms", MaxIncreasePct: f(150)}, {Metric: "rps", MaxDecreasePct: f(100)}}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid rules rejected: %v", err)
	}
}

func TestParseRegressionRejectsUnknownKey(t *testing.T) {
	_, err := Parse([]byte(minimalYAML + "regression:\n  - {metric: rps, max_decrese_pct: 5}\n"))
	if err == nil {
		t.Fatal("expected error for typo")
	}
}
