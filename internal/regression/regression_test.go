package regression

import (
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func f(v float64) *float64 { return &v }

func TestEvaluate(t *testing.T) {
	base := model.Summary{P95Ms: 45, ErrorRate: 0.01, RPS: 100, MeanMs: 0}
	tests := []struct {
		name   string
		rule   scenario.RegressionRule
		cur    model.Summary
		op     string
		limit  float64
		passed bool
	}{
		{"increase pct pass at limit", scenario.RegressionRule{Metric: "p95_ms", MaxIncreasePct: f(20)}, model.Summary{P95Ms: 54}, "lte", 54, true},
		{"increase pct fail", scenario.RegressionRule{Metric: "p95_ms", MaxIncreasePct: f(20)}, model.Summary{P95Ms: 54.5}, "lte", 54, false},
		{"increase abs pass", scenario.RegressionRule{Metric: "error_rate", MaxIncrease: f(0.01)}, model.Summary{ErrorRate: 0.02}, "lte", 0.02, true},
		{"increase abs fail", scenario.RegressionRule{Metric: "error_rate", MaxIncrease: f(0.01)}, model.Summary{ErrorRate: 0.03}, "lte", 0.02, false},
		{"decrease pct pass", scenario.RegressionRule{Metric: "rps", MaxDecreasePct: f(10)}, model.Summary{RPS: 91}, "gte", 90, true},
		{"decrease pct fail", scenario.RegressionRule{Metric: "rps", MaxDecreasePct: f(10)}, model.Summary{RPS: 89.9}, "gte", 90, false},
		{"decrease abs", scenario.RegressionRule{Metric: "rps", MaxDecrease: f(5)}, model.Summary{RPS: 95}, "gte", 95, true},
		{"decrease abs clamps at 0", scenario.RegressionRule{Metric: "rps", MaxDecrease: f(500)}, model.Summary{RPS: 0}, "gte", 0, true},
		{"zero baseline pct: no movement allowed", scenario.RegressionRule{Metric: "mean_ms", MaxIncreasePct: f(50)}, model.Summary{MeanMs: 0.001}, "lte", 0, false},
		{"zero baseline pct: equal passes", scenario.RegressionRule{Metric: "mean_ms", MaxIncreasePct: f(50)}, model.Summary{MeanMs: 0}, "lte", 0, true},
		{"zero baseline abs", scenario.RegressionRule{Metric: "mean_ms", MaxIncrease: f(2)}, model.Summary{MeanMs: 2}, "lte", 2, true},
		{"rounding to 3 decimals", scenario.RegressionRule{Metric: "p95_ms", MaxIncreasePct: f(33.333)}, model.Summary{P95Ms: 59.999}, "lte", 60, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate([]scenario.RegressionRule{tc.rule}, tc.cur, base)
			if len(got) != 1 {
				t.Fatalf("got %d results", len(got))
			}
			r := got[0]
			if r.Metric != tc.rule.Metric+" vs baseline" || r.Op != tc.op || r.Value != tc.limit || r.Passed != tc.passed {
				t.Fatalf("got %+v, want op %s limit %v passed %v", r, tc.op, tc.limit, tc.passed)
			}
			if r.Baseline == nil {
				t.Fatal("Baseline not set")
			}
		})
	}
}

func TestCheck(t *testing.T) {
	rules := []scenario.RegressionRule{{Metric: "p95_ms", MaxIncreasePct: f(10)}}
	cur := model.Summary{P95Ms: 100}
	if res, w := Check(nil, "r", cur, nil); res != nil || w != "" {
		t.Fatalf("no rules: %v %q", res, w)
	}
	if res, w := Check(rules, "r", cur, nil); res != nil || w != WarnNoBaseline {
		t.Fatalf("nil baseline: %v %q", res, w)
	}
	if _, w := Check(rules, "r", cur, &model.Run{ID: "r", Summary: &model.Summary{}}); w != WarnNoBaseline {
		t.Fatalf("self baseline warning = %q", w)
	}
	if res, w := Check(rules, "r", cur, &model.Run{ID: "b"}); res != nil || w != WarnBaselineNoSummary {
		t.Fatalf("no summary: %v %q", res, w)
	}
	res, w := Check(rules, "r", cur, &model.Run{ID: "b", Summary: &model.Summary{P95Ms: 80}})
	if w != "" || len(res) != 1 || res[0].Passed || res[0].Value != 88 {
		t.Fatalf("got %+v %q", res, w)
	}
}
