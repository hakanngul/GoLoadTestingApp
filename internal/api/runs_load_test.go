package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// A load override on a run applies to the run's frozen scenario only; the
// saved test keeps its own load.
func TestStartRunLoadOverride(t *testing.T) {
	e := newEnv(t)
	rec := e.json(t, "POST", "/api/tests", map[string]string{"name": "shop", "yaml": closedSchedYAML})
	wantStatus(t, rec, http.StatusCreated)
	test := decode[model.Test](t, rec)

	rec = e.json(t, "POST", "/api/tests/"+test.ID+"/runs", map[string]any{
		"label": "mcp: smoke",
		"load":  map[string]any{"mode": "profile", "users": 2, "ramp_up_ms": 1000, "hold_ms": 5000},
	})
	wantStatus(t, rec, http.StatusCreated)
	run := decode[model.Run](t, rec)
	sc, err := scenario.Parse([]byte(run.YAML))
	if err != nil {
		t.Fatal(err)
	}
	if got := sc.Load.TotalDuration().Seconds(); got != 6 {
		t.Errorf("run duration = %vs, want 6", got)
	}
	if sc.Load.Stages[0].Target != 2 {
		t.Errorf("run target = %d, want 2", sc.Load.Stages[0].Target)
	}
	got := decode[model.Test](t, e.json(t, "GET", "/api/tests/"+test.ID, nil))
	if got.YAML != closedSchedYAML {
		t.Errorf("saved test changed:\n%s", got.YAML)
	}

	// users on the closed model are fine; rate is not.
	rec = e.json(t, "POST", "/api/tests/"+test.ID+"/runs", map[string]any{
		"load": map[string]any{"mode": "scale", "rate": 5},
	})
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "invalid load override") {
		t.Errorf("body = %s", rec.Body.String())
	}
}
