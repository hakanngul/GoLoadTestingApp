package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

const closedYAML = `name: shop
target:
  base_url: https://shop.example.com
load:
  model: closed
  stages:
    - {duration: 10s, target: 5}
    - {duration: 30s, target: 10}
    - {duration: 10s, target: 0}
steps:
  # the home page
  - name: home
    path: /
    think_time: 1s
    assert:
      - {type: status, value: "200"}
thresholds:
  - {metric: latency.p95, op: lt, value: 500}
`

const openYAML = `name: api
target:
  base_url: https://api.example.com
load:
  model: open
  max_vus: 300
  stages:
    - {duration: 1m, target: 25}
    - {duration: 2m, target: 25}
steps:
  - {name: list, path: /items}
`

func parse(t *testing.T, y string) *scenario.Scenario {
	t.Helper()
	sc, err := scenario.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse:\n%s\n%v", y, err)
	}
	return sc
}

type stage struct {
	d      time.Duration
	target int
}

func wantStages(t *testing.T, sc *scenario.Scenario, mdl scenario.LoadModel, want ...stage) {
	t.Helper()
	if sc.Load.Model != mdl {
		t.Fatalf("model = %s, want %s", sc.Load.Model, mdl)
	}
	if len(sc.Load.Stages) != len(want) {
		t.Fatalf("stages = %+v, want %+v", sc.Load.Stages, want)
	}
	for i, w := range want {
		if g := sc.Load.Stages[i]; g.Duration.Duration != w.d || g.Target != w.target {
			t.Fatalf("stage %d = %v/%d, want %v/%d", i, g.Duration.Duration, g.Target, w.d, w.target)
		}
	}
}

func TestApplyLoadNil(t *testing.T) {
	got, err := ApplyLoad(closedYAML, nil)
	if err != nil || got != closedYAML {
		t.Fatalf("nil override changed the scenario: %v", err)
	}
}

func TestApplyLoadScaleClosed(t *testing.T) {
	src := closedYAML
	got, err := ApplyLoad(src, &model.LoadOverride{Mode: model.LoadScale, Users: 20})
	if err != nil {
		t.Fatal(err)
	}
	if src != closedYAML {
		t.Fatal("source modified")
	}
	sc := parse(t, got)
	wantStages(t, sc, scenario.ModelClosed, stage{10 * time.Second, 10}, stage{30 * time.Second, 20}, stage{10 * time.Second, 0})
	// Everything else is kept, including comments.
	if sc.Name != "shop" || len(sc.Steps) != 1 || sc.Steps[0].ThinkTime.Duration != time.Second ||
		len(sc.Thresholds) != 1 || sc.Target.BaseURL != "https://shop.example.com" {
		t.Fatalf("scenario changed: %+v", sc)
	}
	if !strings.Contains(got, "# the home page") {
		t.Fatalf("comment lost:\n%s", got)
	}
	// Scaling down never drops a positive stage to 0.
	got, err = ApplyLoad(src, &model.LoadOverride{Mode: model.LoadScale, Users: 1})
	if err != nil {
		t.Fatal(err)
	}
	wantStages(t, parse(t, got), scenario.ModelClosed, stage{10 * time.Second, 1}, stage{30 * time.Second, 1}, stage{10 * time.Second, 0})
}

func TestApplyLoadScaleOpen(t *testing.T) {
	got, err := ApplyLoad(openYAML, &model.LoadOverride{Mode: model.LoadScale, Rate: 60})
	if err != nil {
		t.Fatal(err)
	}
	sc := parse(t, got)
	wantStages(t, sc, scenario.ModelOpen, stage{time.Minute, 60}, stage{2 * time.Minute, 60})
	if sc.Load.MaxVUs != 300 {
		t.Fatalf("max_vus = %d", sc.Load.MaxVUs)
	}
}

func TestApplyLoadScaleModelMismatch(t *testing.T) {
	_, err := ApplyLoad(openYAML, &model.LoadOverride{Mode: model.LoadScale, Users: 10})
	if err == nil || !strings.Contains(err.Error(), "load.users: the scenario uses the open model") {
		t.Fatalf("users on open: %v", err)
	}
	_, err = ApplyLoad(closedYAML, &model.LoadOverride{Mode: model.LoadScale, Rate: 10})
	if err == nil || !strings.Contains(err.Error(), "load.rate: the scenario uses the closed model") {
		t.Fatalf("rate on closed: %v", err)
	}
}

func TestApplyLoadProfile(t *testing.T) {
	// users switches an open scenario to the closed model.
	got, err := ApplyLoad(openYAML, &model.LoadOverride{Mode: model.LoadProfile, Users: 20,
		RampUpMs: 30000, HoldMs: 600000, RampDownMs: 10000})
	if err != nil {
		t.Fatal(err)
	}
	wantStages(t, parse(t, got), scenario.ModelClosed,
		stage{30 * time.Second, 20}, stage{10 * time.Minute, 20}, stage{10 * time.Second, 0})

	// rate switches a closed scenario to open; no ramps.
	got, err = ApplyLoad(closedYAML, &model.LoadOverride{Mode: model.LoadProfile, Rate: 50, HoldMs: 120000})
	if err != nil {
		t.Fatal(err)
	}
	wantStages(t, parse(t, got), scenario.ModelOpen, stage{time.Second, 50}, stage{2 * time.Minute, 50})
}

func TestApplyLoadInsertsMissingModel(t *testing.T) {
	y := strings.Replace(openYAML, "  model: open\n", "", 1)
	got, err := ApplyLoad(y, &model.LoadOverride{Mode: model.LoadProfile, Users: 3, HoldMs: 5000})
	if err != nil {
		t.Fatal(err)
	}
	wantStages(t, parse(t, got), scenario.ModelClosed, stage{time.Second, 3}, stage{5 * time.Second, 3})
}

func TestValidateLoad(t *testing.T) {
	cases := []struct {
		l    model.LoadOverride
		want string
	}{
		{model.LoadOverride{Mode: "x", Users: 1}, "load.mode"},
		{model.LoadOverride{Mode: model.LoadScale}, "set users"},
		{model.LoadOverride{Mode: model.LoadScale, Users: 1, Rate: 1}, "not both"},
		{model.LoadOverride{Mode: model.LoadScale, Users: -1}, "load.users: must not be negative"},
		{model.LoadOverride{Mode: model.LoadScale, Users: 1, HoldMs: 1000}, "only apply to mode profile"},
		{model.LoadOverride{Mode: model.LoadProfile, Users: 1}, "load.hold_ms"},
		{model.LoadOverride{Mode: model.LoadProfile, Users: 1, HoldMs: 5000, RampUpMs: -1}, "load.ramp_up_ms"},
		{model.LoadOverride{Mode: model.LoadProfile, Users: 1, HoldMs: 5000, RampDownMs: -1}, "load.ramp_down_ms"},
		{model.LoadOverride{Mode: model.LoadProfile, Users: 1, HoldMs: 8 * 24 * 3600e3}, "at most 7 days"},
	}
	for _, c := range cases {
		l := c.l
		got := strings.Join(validateLoad(&l), "; ")
		if !strings.Contains(got, c.want) {
			t.Errorf("validateLoad(%+v) = %q, want %q", c.l, got, c.want)
		}
		if _, err := ApplyLoad(closedYAML, &l); err == nil {
			t.Errorf("ApplyLoad(%+v) accepted", c.l)
		}
	}
}

func TestDescribeLoad(t *testing.T) {
	cases := []struct {
		l    *model.LoadOverride
		want string
	}{
		{nil, ""},
		{&model.LoadOverride{Mode: model.LoadScale, Users: 50}, "peak 50 VUs (scaled)"},
		{&model.LoadOverride{Mode: model.LoadProfile, Rate: 20, RampUpMs: 30000, HoldMs: 300000, RampDownMs: 10000},
			"20 it/s, ramp 30s, hold 5m0s, down 10s"},
		// ramp_up 0 runs as the 1 s instant ramp ApplyLoad uses.
		{&model.LoadOverride{Mode: model.LoadProfile, Users: 5, HoldMs: 3000}, "5 VUs, ramp 1s, hold 3s"},
	}
	for _, c := range cases {
		if got := DescribeLoad(c.l); got != c.want {
			t.Errorf("DescribeLoad(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
}

// The web UI shows the same fallback (INSTANT_RAMP_MS in
// web/src/lib/schedule.ts); update both together.
func TestInstantRampMatchesWebUI(t *testing.T) {
	if instantRamp != time.Second {
		t.Fatalf("instantRamp = %s; update INSTANT_RAMP_MS in web/src/lib/schedule.ts to match", instantRamp)
	}
}
