package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

const closedSchedYAML = `
name: t
target:
  base_url: https://api.example.test
load:
  model: closed
  stages:
    - {duration: 10s, target: 5}
    - {duration: 20s, target: 5}
steps:
  - path: /health
`

// newSchedEnv is an API environment with the in-memory store, the fake
// runner and a real scheduler (its loop is not started).
func newSchedEnv(t *testing.T) (*env, *store.Memory, *scheduler.Scheduler) {
	t.Helper()
	st := store.NewMemory()
	rn := newFakeRunner(st)
	sch := scheduler.New(scheduler.Options{Store: st, Runner: rn})
	s := newServer(Config{Store: st, Runner: rn, Version: "test", Scheduler: sch})
	return &env{h: s.handler(), srv: s, runner: rn}, st, sch
}

func future(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(scheduler.AtLayout)
}

func TestScheduleCRUD(t *testing.T) {
	e, st, _ := newSchedEnv(t)
	test, err := st.CreateTest(context.Background(), model.Test{Name: "shop", YAML: closedSchedYAML})
	if err != nil {
		t.Fatal(err)
	}
	// Empty list is [].
	rec := e.json(t, "GET", "/api/schedules", nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %s", rec.Body.String())
	}

	body := map[string]any{
		"test_id": test.ID, "name": "Nightly", "kind": "recurring", "cron": "0 3 * * 1-5",
		"timezone": "Europe/Istanbul", "workers": 1, "overlap": "queue", "queue_timeout_ms": 600000,
		"load": map[string]any{"mode": "profile", "users": 20, "ramp_up_ms": 30000, "hold_ms": 600000},
	}
	rec = e.json(t, "POST", "/api/schedules", body)
	wantStatus(t, rec, http.StatusCreated)
	sc := decode[model.Schedule](t, rec)
	if sc.ID == "" || !sc.Enabled || sc.NextRunAt == nil || sc.TestName != "shop" || sc.Load == nil || sc.Load.Users != 20 {
		t.Fatalf("created = %+v", sc)
	}
	if h := sc.NextRunAt.In(time.FixedZone("TRT", 3*3600)); h.Hour() != 3 || h.Minute() != 0 || h.Weekday() == time.Saturday || h.Weekday() == time.Sunday {
		t.Fatalf("next run %s is not a weekday 03:00 Istanbul", h)
	}

	rec = e.json(t, "GET", "/api/schedules/"+sc.ID, nil)
	wantStatus(t, rec, http.StatusOK)
	if got := decode[model.Schedule](t, rec); got.ID != sc.ID || got.Cron != "0 3 * * 1-5" {
		t.Fatalf("get = %+v", got)
	}
	rec = e.json(t, "GET", "/api/schedules?test_id="+test.ID, nil)
	if got := decode[[]model.Schedule](t, rec); len(got) != 1 {
		t.Fatalf("list by test = %+v", got)
	}
	rec = e.json(t, "GET", "/api/schedules?test_id="+missingTestID, nil)
	if got := decode[[]model.Schedule](t, rec); len(got) != 0 {
		t.Fatalf("list other test = %+v", got)
	}

	// Disable via PUT.
	body["enabled"] = false
	body["name"] = "Nightly (off)"
	rec = e.json(t, "PUT", "/api/schedules/"+sc.ID, body)
	wantStatus(t, rec, http.StatusOK)
	up := decode[model.Schedule](t, rec)
	if up.Enabled || up.NextRunAt != nil || up.Name != "Nightly (off)" {
		t.Fatalf("updated = %+v", up)
	}

	rec = e.json(t, "DELETE", "/api/schedules/"+sc.ID, nil)
	wantStatus(t, rec, http.StatusNoContent)
	wantAPIError(t, e.json(t, "GET", "/api/schedules/"+sc.ID, nil), http.StatusNotFound, "not found")
	wantAPIError(t, e.json(t, "DELETE", "/api/schedules/"+sc.ID, nil), http.StatusNotFound, "not found")
	wantAPIError(t, e.json(t, "PUT", "/api/schedules/"+sc.ID, body), http.StatusNotFound, "not found")
}

const missingTestID = "00000000-0000-4000-8000-000000000000"

func TestScheduleValidationErrors(t *testing.T) {
	e, st, _ := newSchedEnv(t)
	test, _ := st.CreateTest(context.Background(), model.Test{Name: "shop", YAML: closedSchedYAML})
	rec := e.json(t, "POST", "/api/schedules", map[string]any{
		"test_id": test.ID, "name": "", "kind": "recurring", "cron": "0 25 * * *", "timezone": "Nowhere/City",
		"load": map[string]any{"mode": "scale", "users": 5, "rate": 5},
	})
	ae := wantAPIError(t, rec, http.StatusBadRequest, "invalid schedule")
	joined := strings.Join(ae.Details, "\n")
	for _, want := range []string{"name: must be between", "timezone: unknown time zone", "load: set either users"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details %q lack %q", joined, want)
		}
	}
	rec = e.json(t, "POST", "/api/schedules", map[string]any{
		"test_id": test.ID, "name": "x", "kind": "once", "at": "2020-01-01T00:00", "timezone": "UTC"})
	ae = wantAPIError(t, rec, http.StatusBadRequest, "invalid schedule")
	if !strings.Contains(strings.Join(ae.Details, ";"), "at: must be in the future") {
		t.Fatalf("details = %v", ae.Details)
	}
	rec = e.json(t, "POST", "/api/schedules", map[string]any{
		"test_id": missingTestID, "name": "x", "kind": "once", "at": future(time.Hour), "timezone": "UTC"})
	ae = wantAPIError(t, rec, http.StatusBadRequest, "invalid schedule")
	if ae.Details[0] != "test_id: no such test" {
		t.Fatalf("details = %v", ae.Details)
	}
	// Unknown field and malformed body.
	wantAPIError(t, e.json(t, "POST", "/api/schedules", `{"bogus":1}`), http.StatusBadRequest, "unknown field")
	wantAPIError(t, e.json(t, "POST", "/api/schedules", `{`), http.StatusBadRequest, "malformed JSON")
}

func TestSchedulePreviewAPI(t *testing.T) {
	e, _, _ := newSchedEnv(t)
	rec := e.json(t, "POST", "/api/schedules/preview", map[string]any{
		"kind": "recurring", "cron": "*/10 * * * *", "timezone": "Europe/Istanbul", "count": 3})
	wantStatus(t, rec, http.StatusOK)
	p := decode[model.SchedulePreview](t, rec)
	if !p.Valid || len(p.Times) != 3 || p.Times[1].Sub(p.Times[0]) != 10*time.Minute {
		t.Fatalf("preview = %+v", p)
	}
	rec = e.json(t, "POST", "/api/schedules/preview", map[string]any{"kind": "recurring", "cron": "nope"})
	wantStatus(t, rec, http.StatusOK)
	if p := decode[model.SchedulePreview](t, rec); p.Valid || len(p.Errors) == 0 || p.Times == nil {
		t.Fatalf("bad preview = %+v", p)
	}
}

func TestScheduleRunNowAPI(t *testing.T) {
	e, st, _ := newSchedEnv(t)
	test, _ := st.CreateTest(context.Background(), model.Test{Name: "shop", YAML: closedSchedYAML})
	rec := e.json(t, "POST", "/api/schedules", map[string]any{
		"test_id": test.ID, "name": "Smoke", "kind": "once", "at": future(48 * time.Hour), "timezone": "UTC",
		"workers": 2, "load": map[string]any{"mode": "scale", "users": 2}})
	wantStatus(t, rec, http.StatusCreated)
	sc := decode[model.Schedule](t, rec)

	rec = e.json(t, "POST", "/api/schedules/"+sc.ID+"/run-now", nil)
	wantStatus(t, rec, http.StatusCreated)
	run := decode[model.Run](t, rec)
	if run.ScheduleID != sc.ID || run.Label != "scheduled: Smoke" || !strings.Contains(run.YAML, "{duration: 10s, target: 2}") {
		t.Fatalf("run = %+v", run)
	}
	if e.runner.lastReq.Workers != 2 {
		t.Fatalf("workers = %d", e.runner.lastReq.Workers)
	}
	// The run records the schedule; the schedule records the run.
	stored, _ := st.GetRun(context.Background(), run.ID)
	got, _ := st.GetSchedule(context.Background(), sc.ID)
	if stored.ScheduleID != sc.ID || got.LastRunID != run.ID || got.LastStatus != model.ScheduleStarted || !got.Enabled {
		t.Fatalf("stored run %+v / schedule %+v", stored, got)
	}
	// Runner errors map like run starts (403 for the allowlist).
	e.runner.startErr = fmt.Errorf("%w: x", runner.ErrTargetNotAllowed)
	wantAPIError(t, e.json(t, "POST", "/api/schedules/"+sc.ID+"/run-now", nil), http.StatusForbidden, "target not allowed")
	wantAPIError(t, e.json(t, "POST", "/api/schedules/"+missingTestID+"/run-now", nil), http.StatusNotFound, "not found")
}

func TestScheduleCascadeWithTest(t *testing.T) {
	e, st, _ := newSchedEnv(t)
	test, _ := st.CreateTest(context.Background(), model.Test{Name: "shop", YAML: closedSchedYAML})
	rec := e.json(t, "POST", "/api/schedules", map[string]any{
		"test_id": test.ID, "name": "x", "kind": "recurring", "cron": "@daily", "timezone": "UTC"})
	wantStatus(t, rec, http.StatusCreated)
	sc := decode[model.Schedule](t, rec)
	wantStatus(t, e.json(t, "DELETE", "/api/tests/"+test.ID, nil), http.StatusNoContent)
	wantAPIError(t, e.json(t, "GET", "/api/schedules/"+sc.ID, nil), http.StatusNotFound, "not found")
}

func TestSchedulesUnavailable(t *testing.T) {
	e := newEnv(t)
	wantAPIError(t, e.json(t, "GET", "/api/schedules", nil), http.StatusServiceUnavailable, "scheduling is not available")
}

func TestSchedulesRequireAuth(t *testing.T) {
	st := store.NewMemory()
	rn := newFakeRunner(st)
	s := newServer(Config{Store: st, Runner: rn, AuthToken: "sekret",
		Scheduler: scheduler.New(scheduler.Options{Store: st, Runner: rn})})
	e := &env{h: s.handler(), srv: s, runner: rn}
	wantStatus(t, e.json(t, "GET", "/api/schedules", nil), http.StatusUnauthorized)
	rec := e.do(t, "GET", "/api/schedules", nil, "Authorization", "Bearer sekret")
	wantStatus(t, rec, http.StatusOK)
}
