package suite

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// fakeRunner creates run records in the store and finishes each after
// hold with the status configured for its test name (default passed).
type fakeRunner struct {
	st      *store.Memory
	hold    time.Duration
	outcome map[string]model.RunStatus // test name -> final status
	failAt  map[string]error           // test name -> Start error

	mu      sync.Mutex
	started []string // test names in start order
	maxLive int
	live    int
	stops   map[string]chan struct{}
}

func newFakeRunner(st *store.Memory, hold time.Duration) *fakeRunner {
	return &fakeRunner{st: st, hold: hold, outcome: map[string]model.RunStatus{},
		failAt: map[string]error{}, stops: map[string]chan struct{}{}}
}

func (f *fakeRunner) Start(ctx context.Context, t model.Test, req model.StartRunRequest) (model.Run, error) {
	if err := f.failAt[t.Name]; err != nil {
		return model.Run{}, err
	}
	run, err := f.st.CreateRun(ctx, model.Run{TestID: t.ID, TestName: t.Name, Status: model.RunRunning,
		StartedAt: time.Now(), Label: req.Label, Notes: req.Notes, Workers: req.Workers, YAML: t.YAML, StartedBy: req.StartedBy})
	if err != nil {
		return model.Run{}, err
	}
	stop := make(chan struct{})
	f.mu.Lock()
	f.started = append(f.started, t.Name)
	f.live++
	f.maxLive = max(f.maxLive, f.live)
	f.stops[run.ID] = stop
	f.mu.Unlock()
	go func(run model.Run) {
		status := model.RunPassed
		if s, ok := f.outcome[t.Name]; ok {
			status = s
		}
		select {
		case <-time.After(f.hold):
		case <-stop:
			status = model.RunStopped
		}
		f.mu.Lock()
		f.live--
		delete(f.stops, run.ID)
		f.mu.Unlock()
		fin := time.Now()
		run.Status, run.FinishedAt = status, &fin
		run.Summary = &model.Summary{Requests: 7, P95Ms: 3, ByStep: map[string]model.Summary{"x": {}}}
		_ = f.st.UpdateRun(context.Background(), run)
	}(run)
	return run, nil
}

func (f *fakeRunner) Stop(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.stops[id]
	if !ok {
		return runner.ErrNotRunning
	}
	close(ch)
	delete(f.stops, id)
	return nil
}

type fixture struct {
	st  *store.Memory
	fr  *fakeRunner
	svc *Service
	ids map[string]string // test name -> ID
}

func newFixture(t *testing.T, hold time.Duration, names ...string) *fixture {
	t.Helper()
	st := store.NewMemory()
	fr := newFakeRunner(st, hold)
	f := &fixture{st: st, fr: fr, ids: map[string]string{},
		svc: New(Options{Store: st, Runner: fr, Poll: 5 * time.Millisecond})}
	for _, n := range names {
		tt, err := st.CreateTest(context.Background(), model.Test{Name: n, YAML: "x"})
		if err != nil {
			t.Fatal(err)
		}
		f.ids[n] = tt.ID
	}
	t.Cleanup(func() { _ = f.svc.Shutdown(context.Background()) })
	return f
}

func (f *fixture) suite(t *testing.T, req model.SuiteRequest, names ...string) model.Suite {
	t.Helper()
	for _, n := range names {
		req.Items = append(req.Items, model.SuiteItem{TestID: f.ids[n]})
	}
	if req.Name == "" {
		req.Name = "suite"
	}
	su, err := f.svc.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return su
}

// waitDone waits for the suite run to finish and returns it.
func (f *fixture) waitDone(t *testing.T, id string) model.SuiteRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := f.svc.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status.Finished() && len(f.svc.Active()) == 0 {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("suite run did not finish")
	return model.SuiteRun{}
}

func statuses(r model.SuiteRun) []string {
	out := []string{}
	for _, it := range r.Items {
		out = append(out, it.Status)
	}
	return out
}

func TestSequentialAllPass(t *testing.T) {
	f := newFixture(t, 20*time.Millisecond, "a", "b", "c")
	su := f.suite(t, model.SuiteRequest{}, "a", "b", "c")
	if su.Mode != model.SuiteSequential || su.Items[1].TestName != "b" {
		t.Fatalf("suite = %+v", su)
	}
	r, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{Label: " v1 ", StartedBy: "token:ci"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != model.RunRunning || r.Label != "v1" || r.StartedBy != "token:ci" {
		t.Fatalf("started = %+v", r)
	}
	got := f.waitDone(t, r.ID)
	if got.Status != model.RunPassed || !slices.Equal(statuses(got), []string{"passed", "passed", "passed"}) {
		t.Fatalf("final = %+v", got)
	}
	if !slices.Equal(f.fr.started, []string{"a", "b", "c"}) || f.fr.maxLive != 1 {
		t.Fatalf("started %v, max live %d", f.fr.started, f.fr.maxLive)
	}
	for _, it := range got.Items {
		if it.RunID == "" || it.Summary == nil || it.Summary.Requests != 7 || it.Summary.ByStep != nil ||
			it.StartedAt == nil || it.FinishedAt == nil {
			t.Fatalf("item = %+v", it)
		}
		run, _ := f.st.GetRun(context.Background(), it.RunID)
		if run.Label != "v1" {
			t.Fatalf("run label = %q", run.Label)
		}
		if run.StartedBy != "token:ci" { // item runs inherit the suite run's
			t.Fatalf("run started_by = %q", run.StartedBy)
		}
	}
	if got.FinishedAt == nil {
		t.Fatal("no finished_at")
	}
	list, _ := f.svc.List(context.Background())
	if len(list) != 1 || list[0].LastRun == nil || list[0].LastRun.ID != r.ID {
		t.Fatalf("list = %+v", list)
	}
}

func TestSequentialStopOnFailure(t *testing.T) {
	f := newFixture(t, 5*time.Millisecond, "a", "b", "c")
	f.fr.outcome["b"] = model.RunFailed
	su := f.suite(t, model.SuiteRequest{StopOnFailure: true}, "a", "b", "c")
	r, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.waitDone(t, r.ID)
	if got.Status != model.RunFailed || !slices.Equal(statuses(got), []string{"passed", "failed", "skipped"}) {
		t.Fatalf("final = %v %v", got.Status, statuses(got))
	}
}

func TestSequentialContinuesWithoutStopOnFailure(t *testing.T) {
	f := newFixture(t, 5*time.Millisecond, "a", "b", "c")
	f.fr.failAt["a"] = errors.New("invalid scenario: boom")
	su := f.suite(t, model.SuiteRequest{GapMs: 10}, "a", "b", "c")
	r, _ := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	got := f.waitDone(t, r.ID)
	if got.Status != model.RunFailed || !slices.Equal(statuses(got), []string{"error", "passed", "passed"}) {
		t.Fatalf("final = %v %v", got.Status, statuses(got))
	}
	if got.Items[0].Error != "invalid scenario: boom" || got.Items[0].RunID != "" {
		t.Fatalf("item 0 = %+v", got.Items[0])
	}
}

func TestDeletedTestIsAnItemError(t *testing.T) {
	f := newFixture(t, 5*time.Millisecond, "a", "b")
	su := f.suite(t, model.SuiteRequest{}, "a", "b")
	if err := f.st.DeleteTest(context.Background(), f.ids["a"]); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(context.Background(), su.ID)
	if got.Items[0].TestName != "" || got.Items[1].TestName != "b" {
		t.Fatalf("items = %+v", got.Items)
	}
	r, _ := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	fin := f.waitDone(t, r.ID)
	if !slices.Equal(statuses(fin), []string{"error", "passed"}) || fin.Items[0].Error != "test was deleted" {
		t.Fatalf("final = %+v", fin.Items)
	}
}

func TestParallel(t *testing.T) {
	f := newFixture(t, 50*time.Millisecond, "a", "b", "c")
	f.fr.outcome["c"] = model.RunFailed
	su := f.suite(t, model.SuiteRequest{Mode: model.SuiteParallel, StopOnFailure: true, GapMs: 1000}, "a", "b", "c")
	if su.StopOnFailure || su.GapMs != 0 {
		t.Fatalf("parallel suite kept sequential options: %+v", su)
	}
	r, _ := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	got := f.waitDone(t, r.ID)
	if f.fr.maxLive != 3 {
		t.Fatalf("max live = %d, want 3", f.fr.maxLive)
	}
	if got.Status != model.RunFailed || !slices.Equal(statuses(got), []string{"passed", "passed", "failed"}) {
		t.Fatalf("final = %v %v", got.Status, statuses(got))
	}
}

func TestStop(t *testing.T) {
	f := newFixture(t, time.Hour, "a", "b")
	su := f.suite(t, model.SuiteRequest{}, "a", "b")
	r, _ := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	if _, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{}); !errors.Is(err, ErrActive) {
		t.Fatalf("second start: %v", err)
	}
	if err := f.svc.Delete(context.Background(), su.ID); !errors.Is(err, ErrActive) {
		t.Fatalf("delete active: %v", err)
	}
	// Wait for the first test to be running.
	for {
		cur, _ := f.svc.GetRun(context.Background(), r.ID)
		if cur.Items[0].Status == "running" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := f.svc.Stop(r.ID); err != nil {
		t.Fatal(err)
	}
	got := f.waitDone(t, r.ID)
	if got.Status != model.RunStopped || !slices.Equal(statuses(got), []string{"stopped", "skipped"}) {
		t.Fatalf("final = %v %v", got.Status, statuses(got))
	}
	if err := f.svc.Stop(r.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stop again: %v", err)
	}
	// Can run again and be deleted once idle.
	if err := f.svc.Delete(context.Background(), su.ID); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t, time.Millisecond, "a")
	cases := []struct {
		req  model.SuiteRequest
		want string
	}{
		{model.SuiteRequest{Items: []model.SuiteItem{{TestID: f.ids["a"]}}}, "name: required"},
		{model.SuiteRequest{Name: "x"}, "items: add at least one test"},
		{model.SuiteRequest{Name: "x", Mode: "random", Items: []model.SuiteItem{{TestID: f.ids["a"]}}}, "mode:"},
		{model.SuiteRequest{Name: "x", GapMs: -1, Items: []model.SuiteItem{{TestID: f.ids["a"]}}}, "gap_ms:"},
		{model.SuiteRequest{Name: "x", Items: []model.SuiteItem{{TestID: "nope"}}}, "items[0].test_id: test not found"},
		{model.SuiteRequest{Name: "x", Items: []model.SuiteItem{{TestID: f.ids["a"], Workers: -1}}}, "items[0].workers"},
		{model.SuiteRequest{Name: strings.Repeat("n", 101), Items: []model.SuiteItem{{TestID: f.ids["a"]}}}, "name: at most"},
	}
	for _, c := range cases {
		_, err := f.svc.Create(context.Background(), c.req)
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.req, err, c.want)
		}
	}
	su := f.suite(t, model.SuiteRequest{Name: "ok"}, "a")
	if _, err := f.svc.Create(context.Background(), model.SuiteRequest{Name: "ok",
		Items: []model.SuiteItem{{TestID: f.ids["a"]}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	up, err := f.svc.Update(context.Background(), su.ID, model.SuiteRequest{Name: "renamed", Mode: model.SuiteParallel,
		Items: []model.SuiteItem{{TestID: f.ids["a"], Workers: 2}, {TestID: f.ids["a"]}}})
	if err != nil || up.Name != "renamed" || len(up.Items) != 2 || up.Items[0].Workers != 2 || up.Items[1].TestName != "a" {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := f.svc.Update(context.Background(), "nope", model.SuiteRequest{Name: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if _, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{Label: strings.Repeat("l", 101)}); err == nil {
		t.Fatal("long label accepted")
	}
	if _, err := f.svc.Start(context.Background(), "nope", model.StartSuiteRequest{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("start missing: %v", err)
	}
}

func TestRecover(t *testing.T) {
	f := newFixture(t, time.Millisecond, "a", "b", "c")
	su := f.suite(t, model.SuiteRequest{}, "a", "b", "c")
	ctx := context.Background()
	r, err := f.st.CreateSuiteRun(ctx, model.SuiteRun{SuiteID: su.ID, SuiteName: su.Name, Status: model.RunRunning,
		Mode: model.SuiteSequential, StartedAt: time.Now(), Items: []model.SuiteRunItem{
			{TestID: f.ids["a"], Status: "passed"}, {TestID: f.ids["b"], Status: "running"}, {TestID: f.ids["c"], Status: "pending"}}})
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.Recover(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	got, _ := f.svc.GetRun(ctx, r.ID)
	if got.Status != model.RunError || got.FinishedAt == nil || !slices.Equal(statuses(got), []string{"passed", "error", "skipped"}) {
		t.Fatalf("recovered = %+v", got)
	}
}

func TestShutdown(t *testing.T) {
	f := newFixture(t, time.Hour, "a", "b")
	su := f.suite(t, model.SuiteRequest{}, "a", "b")
	r, _ := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.svc.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.GetRun(context.Background(), r.ID)
	if got.Status != model.RunError || !slices.Equal(statuses(got), []string{"error", "skipped"}) {
		t.Fatalf("after shutdown = %v %v", got.Status, statuses(got))
	}
	if _, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{}); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("start after shutdown: %v", err)
	}
}

const closedYAML = `
name: t
target:
  base_url: https://api.example.test
load:
  model: closed
  stages:
    - {duration: 10s, target: 5}
steps:
  - path: /health
`

func TestItemLoadOverride(t *testing.T) {
	f := newFixture(t, 5*time.Millisecond)
	ctx := context.Background()
	tt, err := f.st.CreateTest(ctx, model.Test{Name: "home", YAML: closedYAML})
	if err != nil {
		t.Fatal(err)
	}
	over := &model.LoadOverride{Mode: model.LoadProfile, Users: 50, RampUpMs: 2000, HoldMs: 5000}

	// Invalid overrides are reported per item.
	_, err = f.svc.Create(ctx, model.SuiteRequest{Name: "bad", Items: []model.SuiteItem{
		{TestID: tt.ID}, {TestID: tt.ID, Load: &model.LoadOverride{Mode: model.LoadScale, Rate: 10}}}})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Details) != 1 || !strings.HasPrefix(ve.Details[0], "items[1].load") {
		t.Fatalf("bad override: %v", err)
	}

	su, err := f.svc.Create(ctx, model.SuiteRequest{Name: "steps", Items: []model.SuiteItem{
		{TestID: tt.ID}, {TestID: tt.ID, Load: over}}})
	if err != nil {
		t.Fatal(err)
	}
	if su.Items[0].Load != nil || su.Items[1].Load == nil || su.Items[1].Load.Users != 50 {
		t.Fatalf("items = %+v", su.Items)
	}
	r, err := f.svc.Start(ctx, su.ID, model.StartSuiteRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := f.waitDone(t, r.ID)
	if got.Status != model.RunPassed || got.Items[1].Load == nil {
		t.Fatalf("final = %+v", got)
	}
	plain, _ := f.st.GetRun(ctx, got.Items[0].RunID)
	loaded, _ := f.st.GetRun(ctx, got.Items[1].RunID)
	if plain.YAML != closedYAML || plain.Notes != "" {
		t.Fatalf("item without override changed the scenario: %q / %q", plain.YAML, plain.Notes)
	}
	if loaded.YAML == closedYAML || !strings.Contains(loaded.YAML, "target: 50") ||
		!strings.Contains(loaded.Notes, "load override: 50 VUs, ramp 2s, hold 5s") {
		t.Fatalf("override run yaml=%q notes=%q", loaded.YAML, loaded.Notes)
	}
	if cur, _ := f.st.GetTest(ctx, tt.ID); cur.YAML != closedYAML {
		t.Fatal("saved test was modified")
	}
}
