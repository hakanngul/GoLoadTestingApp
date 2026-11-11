package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// fakeSuites records suite starts; active suite runs are set by the test.
type fakeSuites struct {
	mu     sync.Mutex
	active []string
	starts []suiteStart
	err    error
}

type suiteStart struct {
	id  string
	req model.StartSuiteRequest
}

func (f *fakeSuites) Start(_ context.Context, id string, req model.StartSuiteRequest) (model.SuiteRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return model.SuiteRun{}, f.err
	}
	f.starts = append(f.starts, suiteStart{id, req})
	return model.SuiteRun{ID: fmt.Sprintf("suite-run-%d", len(f.starts)), SuiteID: id}, nil
}

func (f *fakeSuites) Active() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.active...)
}

func (f *fakeSuites) setActive(ids ...string) {
	f.mu.Lock()
	f.active = ids
	f.mu.Unlock()
}

func (f *fakeSuites) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

// suiteEnv is an env whose scheduler can start suites, with one suite.
func suiteEnv(t *testing.T) (*env, *fakeSuites, model.Suite) {
	t.Helper()
	e := newEnv(t)
	fs := &fakeSuites{}
	e.s = New(Options{Store: e.st, Runner: e.run, Suites: fs, Now: e.clock.Now, QueuePoll: 2 * time.Millisecond})
	su, err := e.st.CreateSuite(context.Background(), model.Suite{Name: "smoke", Mode: model.SuiteSequential,
		Items: []model.SuiteItem{{TestID: e.test.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	return e, fs, su
}

func suiteSpec(su model.Suite, extra func(*model.ScheduleSpec)) model.ScheduleSpec {
	s := daily3(extra)
	s.SuiteID = su.ID
	s.Name = "nightly suite"
	return s
}

func TestSuiteScheduleFires(t *testing.T) {
	e, fs, su := suiteEnv(t)
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if sc.SuiteID != su.ID || sc.SuiteName != "smoke" || sc.TestID != "" {
		t.Fatalf("created = %+v", sc)
	}
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	if fs.count() != 1 || e.run.count() != 0 {
		t.Fatalf("suite starts = %d, test starts = %d", fs.count(), e.run.count())
	}
	if st := fs.starts[0]; st.id != su.ID || st.req.StartedBy != "schedule:nightly suite" || st.req.Label != "scheduled: nightly suite" {
		t.Fatalf("start = %+v", st)
	}
	got := e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleStarted || got.LastRunID != "suite-run-1" || got.SuiteName != "smoke" {
		t.Fatalf("after fire: %+v", got)
	}

	// An explicit label wins.
	sc2, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su, func(s *model.ScheduleSpec) { s.Label = "custom" })})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(*sc2.NextRunAt)
	e.s.Tick(context.Background())
	if fs.starts[len(fs.starts)-1].req.Label != "custom" {
		t.Fatalf("label = %q", fs.starts[len(fs.starts)-1].req.Label)
	}
}

func TestSuiteScheduleOverlapSkip(t *testing.T) {
	e, fs, su := suiteEnv(t)
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su, nil)})
	if err != nil {
		t.Fatal(err)
	}
	fs.setActive("running-suite-run")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	got := e.get(t, sc.ID)
	if fs.count() != 0 || got.LastStatus != model.ScheduleSkipped {
		t.Fatalf("starts = %d, schedule = %+v", fs.count(), got)
	}
}

func TestTestScheduleSkipsWhileSuiteActive(t *testing.T) {
	e, fs, _ := suiteEnv(t)
	sc := e.create(t, daily3(nil))
	fs.setActive("running-suite-run")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	if e.run.count() != 0 || e.get(t, sc.ID).LastStatus != model.ScheduleSkipped {
		t.Fatalf("test schedule started while a suite run was active")
	}
}

func TestSuiteScheduleOverlapQueue(t *testing.T) {
	e, fs, su := suiteEnv(t)
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su,
		func(s *model.ScheduleSpec) { s.Overlap = model.OverlapQueue })})
	if err != nil {
		t.Fatal(err)
	}
	fs.setActive("running-suite-run")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	if got := e.get(t, sc.ID); got.LastStatus != model.ScheduleQueued || fs.count() != 0 {
		t.Fatalf("queued = %+v, starts %d", got, fs.count())
	}
	fs.setActive()
	e.s.Wait()
	if got := e.get(t, sc.ID); got.LastStatus != model.ScheduleStarted || got.LastRunID != "suite-run-1" {
		t.Fatalf("after queue = %+v", got)
	}
}

func TestSuiteScheduleRunNow(t *testing.T) {
	e, fs, su := suiteEnv(t)
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su, nil)})
	if err != nil {
		t.Fatal(err)
	}
	fs.setActive("other") // run-now ignores the overlap policy
	res, err := e.s.RunNow(context.Background(), sc.ID)
	if err != nil || res.SuiteRun == nil || res.Run != nil || res.SuiteRun.SuiteID != su.ID {
		t.Fatalf("RunNow = %+v, %v", res, err)
	}
	if got := e.get(t, sc.ID); got.LastRunID != res.SuiteRun.ID || got.LastStatus != model.ScheduleStarted {
		t.Fatalf("after run-now: %+v", got)
	}
}

func TestSuiteScheduleStartError(t *testing.T) {
	e, fs, su := suiteEnv(t)
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su, nil)})
	if err != nil {
		t.Fatal(err)
	}
	fs.err = errors.New("suite is running")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	if got := e.get(t, sc.ID); got.LastStatus != model.ScheduleError || !strings.Contains(got.LastMessage, "suite is running") {
		t.Fatalf("schedule = %+v", got)
	}
}

func TestSuiteScheduleWithoutSuiteService(t *testing.T) {
	e, _, su := suiteEnv(t)
	e.s = e.restart() // no Options.Suites
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: suiteSpec(su, nil)})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	if got := e.get(t, sc.ID); got.LastStatus != model.ScheduleError || !strings.Contains(got.LastMessage, "suites are not available") {
		t.Fatalf("schedule = %+v", got)
	}
}

func TestSuiteScheduleValidation(t *testing.T) {
	e, _, su := suiteEnv(t)
	cases := []struct {
		spec model.ScheduleSpec
		want string
	}{
		{daily3(func(s *model.ScheduleSpec) { s.Name = "n" }), "test_id: required"},
		{suiteSpec(su, func(s *model.ScheduleSpec) { s.TestID = e.test.ID }), "set either test_id or suite_id"},
		{suiteSpec(su, func(s *model.ScheduleSpec) { s.Load = &model.LoadOverride{Mode: model.LoadScale, Users: 5} }),
			"load: not supported for suite schedules; set load per test in the suite"},
		{suiteSpec(su, func(s *model.ScheduleSpec) { s.Workers = 2 }), "workers: not supported for suite schedules"},
	}
	missing := suiteSpec(su, nil)
	missing.SuiteID = "00000000-0000-4000-8000-000000000000"
	cases = append(cases, struct {
		spec model.ScheduleSpec
		want string
	}{missing, "items/suite_id: no such suite"})
	for _, c := range cases {
		_, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: c.spec})
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(strings.Join(ve.Details, "; "), c.want) {
			t.Errorf("Create(%+v) = %v, want %q", c.spec, err, c.want)
		}
	}
}
