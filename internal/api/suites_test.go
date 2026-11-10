package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
	"github.com/hakanngul/GoLoadTestingApp/internal/suite"
)

func newSuiteEnv(t *testing.T) (*env, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	rn := newFakeRunner(st)
	svc := suite.New(suite.Options{Store: st, Runner: rn, Poll: 5 * time.Millisecond})
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	s := newServer(Config{Store: st, Runner: rn, Version: "test", Suites: svc})
	return &env{h: s.handler(), srv: s, runner: rn}, st
}

func TestSuiteAPI(t *testing.T) {
	e, st := newSuiteEnv(t)
	ctx := context.Background()
	a, _ := st.CreateTest(ctx, model.Test{Name: "login", YAML: closedSchedYAML})
	b, _ := st.CreateTest(ctx, model.Test{Name: "checkout", YAML: closedSchedYAML})

	rec := e.json(t, "GET", "/api/suites", nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %s", rec.Body.String())
	}

	rec = e.json(t, "POST", "/api/suites", map[string]any{"name": "Smoke"})
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "invalid suite") || !strings.Contains(rec.Body.String(), "items:") {
		t.Fatalf("validation body = %s", rec.Body.String())
	}

	body := map[string]any{"name": "Smoke", "mode": "sequential", "stop_on_failure": true,
		"items": []map[string]any{{"test_id": a.ID}, {"test_id": b.ID, "workers": 1}}}
	rec = e.json(t, "POST", "/api/suites", body)
	wantStatus(t, rec, http.StatusCreated)
	su := decode[model.Suite](t, rec)
	if su.ID == "" || len(su.Items) != 2 || su.Items[1].TestName != "checkout" {
		t.Fatalf("created = %+v", su)
	}
	rec = e.json(t, "POST", "/api/suites", body)
	wantStatus(t, rec, http.StatusConflict)

	body["name"] = "Smoke 2"
	rec = e.json(t, "PUT", "/api/suites/"+su.ID, body)
	wantStatus(t, rec, http.StatusOK)
	rec = e.json(t, "GET", "/api/suites/"+su.ID, nil)
	if got := decode[model.Suite](t, rec); got.Name != "Smoke 2" {
		t.Fatalf("get = %+v", got)
	}
	wantStatus(t, e.json(t, "GET", "/api/suites/nope", nil), http.StatusNotFound)

	rec = e.json(t, "POST", "/api/suites/"+su.ID+"/runs", map[string]any{"label": "v1"})
	wantStatus(t, rec, http.StatusCreated)
	sr := decode[model.SuiteRun](t, rec)
	if sr.Status != model.RunRunning || sr.Label != "v1" || len(sr.Items) != 2 {
		t.Fatalf("started = %+v", sr)
	}
	wantStatus(t, e.json(t, "POST", "/api/suites/"+su.ID+"/runs", nil), http.StatusConflict)
	wantStatus(t, e.json(t, "DELETE", "/api/suites/"+su.ID, nil), http.StatusConflict)

	// Wait until the first test's run started, then stop the suite and
	// let the fake run end the way a stopped run does.
	var runID string
	for deadline := time.Now().Add(3 * time.Second); runID == "" && time.Now().Before(deadline); {
		got := decode[model.SuiteRun](t, e.json(t, "GET", "/api/suite-runs/"+sr.ID, nil))
		runID = got.Items[0].RunID
		time.Sleep(5 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("first test did not start")
	}
	wantStatus(t, e.json(t, "POST", "/api/suite-runs/"+sr.ID+"/stop", nil), http.StatusAccepted)
	run, _ := st.GetRun(ctx, runID)
	fin := time.Now()
	run.Status, run.FinishedAt = model.RunStopped, &fin
	if err := st.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	var final model.SuiteRun
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		final = decode[model.SuiteRun](t, e.json(t, "GET", "/api/suite-runs/"+sr.ID, nil))
		if final.Status.Finished() {
			break
		}
	}
	if final.Status != model.RunStopped || final.Items[0].Status != "stopped" || final.Items[1].Status != "skipped" {
		t.Fatalf("final = %+v", final)
	}
	wantStatus(t, e.json(t, "POST", "/api/suite-runs/"+sr.ID+"/stop", nil), http.StatusConflict)

	rec = e.json(t, "GET", "/api/suites/"+su.ID+"/runs?limit=5", nil)
	wantStatus(t, rec, http.StatusOK)
	if got := decode[[]model.SuiteRun](t, rec); len(got) != 1 || got[0].ID != sr.ID {
		t.Fatalf("suite runs = %+v", got)
	}
	wantStatus(t, e.json(t, "GET", "/api/suite-runs?limit=0", nil), http.StatusBadRequest)
	rec = e.json(t, "GET", "/api/suites", nil)
	if got := decode[[]model.Suite](t, rec); len(got) != 1 || got[0].LastRun == nil {
		t.Fatalf("list = %+v", got)
	}

	// The suite can be deleted once idle (a moment after it ended).
	time.Sleep(20 * time.Millisecond)
	wantStatus(t, e.json(t, "DELETE", "/api/suites/"+su.ID, nil), http.StatusNoContent)
	wantStatus(t, e.json(t, "GET", "/api/suite-runs/"+sr.ID, nil), http.StatusNotFound)
}

func TestSuiteAPIUnavailable(t *testing.T) {
	st := store.NewMemory()
	s := newServer(Config{Store: st, Runner: newFakeRunner(st), Version: "test"})
	e := &env{h: s.handler(), srv: s}
	wantStatus(t, e.json(t, "GET", "/api/suites", nil), http.StatusServiceUnavailable)
}
