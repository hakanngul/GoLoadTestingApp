package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// TestWithRunnerManager fires a schedule through the real runner and
// checks the stored run: schedule ID, label and the overridden load.
func TestWithRunnerManager(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	st := store.NewMemory()
	ctx := context.Background()
	y := `name: it
target: {base_url: ` + target.URL + `}
load:
  model: open
  stages: [{duration: 1s, target: 1}]
steps:
  - {name: home, path: /, think_time: 100ms, assert: [{type: status, value: "200"}]}
`
	test, err := st.CreateTest(ctx, model.Test{Name: "it", YAML: y})
	if err != nil {
		t.Fatal(err)
	}
	mgr := runner.NewManager(runner.Options{Store: st})
	defer mgr.Shutdown(context.Background()) //nolint:errcheck
	clock := &fakeClock{t: time.Now().UTC()}
	s := New(Options{Store: st, Runner: mgr, Now: clock.Now})
	sc, err := s.Create(ctx, model.ScheduleRequest{ScheduleSpec: model.ScheduleSpec{
		TestID: test.ID, Name: "every minute", Kind: model.ScheduleRecurring, Cron: "* * * * *", Timezone: "UTC",
		Load: &model.LoadOverride{Mode: model.LoadProfile, Users: 2, RampUpMs: 200, HoldMs: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(*sc.NextRunAt)
	s.Tick(ctx)
	got, _ := st.GetSchedule(ctx, sc.ID)
	if got.LastStatus != model.ScheduleStarted || got.LastRunID == "" {
		t.Fatalf("schedule = %+v", got)
	}
	run, err := st.GetRun(ctx, got.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ScheduleID != sc.ID || run.Label != "scheduled: every minute" {
		t.Fatalf("run = %+v", run)
	}
	frozen, err := scenario.Parse([]byte(run.YAML))
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Load.Model != scenario.ModelClosed || frozen.Load.Stages[1].Target != 2 || !strings.Contains(run.YAML, "think_time: 100ms") {
		t.Fatalf("frozen yaml:\n%s", run.YAML)
	}
	deadline := time.Now().Add(15 * time.Second)
	for len(mgr.Active()) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	run, _ = st.GetRun(ctx, run.ID)
	if !run.Status.Finished() || run.ScheduleID != sc.ID || hits.Load() == 0 {
		t.Fatalf("finished run = %s %q hits=%d", run.Status, run.ScheduleID, hits.Load())
	}
}
