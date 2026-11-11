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
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// fakeRunner records starts; active runs are set by the test.
type fakeRunner struct {
	mu     sync.Mutex
	active []string
	starts []started
	err    error
	n      int
}

type started struct {
	test model.Test
	req  model.StartRunRequest
}

func (r *fakeRunner) Start(_ context.Context, test model.Test, req model.StartRunRequest) (model.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return model.Run{}, r.err
	}
	if _, err := scenario.Parse([]byte(test.YAML)); err != nil {
		return model.Run{}, err
	}
	r.n++
	r.starts = append(r.starts, started{test, req})
	return model.Run{ID: fmt.Sprintf("run-%d", r.n), TestID: test.ID, ScheduleID: req.ScheduleID}, nil
}

func (r *fakeRunner) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.active...)
}

func (r *fakeRunner) setActive(ids ...string) {
	r.mu.Lock()
	r.active = ids
	r.mu.Unlock()
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *fakeRunner) last() started {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[len(r.starts)-1]
}

type env struct {
	st    *store.Memory
	clock *fakeClock
	run   *fakeRunner
	s     *Scheduler
	test  model.Test
}

// istanbul 2026-11-10 02:59:00 local = 23:59Z on the 9th.
var t0 = utc("2026-11-09T23:59:00Z")

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{st: store.NewMemory(), clock: &fakeClock{t: t0}, run: &fakeRunner{}}
	var err error
	e.test, err = e.st.CreateTest(context.Background(), model.Test{Name: "shop", YAML: closedYAML})
	if err != nil {
		t.Fatal(err)
	}
	e.s = e.restart()
	return e
}

// restart returns a new Scheduler on the same store and clock (a server
// restart).
func (e *env) restart() *Scheduler {
	return New(Options{Store: e.st, Runner: e.run, Now: e.clock.Now, QueuePoll: 2 * time.Millisecond})
}

func (e *env) create(t *testing.T, spec model.ScheduleSpec) model.Schedule {
	t.Helper()
	if spec.TestID == "" {
		spec.TestID = e.test.ID
	}
	if spec.Name == "" {
		spec.Name = "nightly"
	}
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: spec})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return sc
}

func (e *env) get(t *testing.T, id string) model.Schedule {
	t.Helper()
	sc, err := e.st.GetSchedule(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func daily3(extra func(*model.ScheduleSpec)) model.ScheduleSpec {
	s := model.ScheduleSpec{Kind: model.ScheduleRecurring, Cron: "0 3 * * *", Timezone: "Europe/Istanbul"}
	if extra != nil {
		extra(&s)
	}
	return s
}

func TestRecurringFiresOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sc := e.create(t, daily3(nil))
	if want := utc("2026-11-10T00:00:00Z"); sc.NextRunAt == nil || !sc.NextRunAt.Equal(want) || !sc.Enabled {
		t.Fatalf("next = %v, want %s", sc.NextRunAt, want)
	}
	if sc.Timezone != "Europe/Istanbul" || sc.Overlap != model.OverlapSkip {
		t.Fatalf("defaults: %+v", sc)
	}
	if w := e.s.Tick(ctx); w != time.Minute || e.run.count() != 0 {
		t.Fatalf("early tick: wait %s, starts %d", w, e.run.count())
	}
	e.clock.Advance(time.Minute + 300*time.Millisecond)
	e.s.Tick(ctx)
	if e.run.count() != 1 {
		t.Fatalf("starts = %d, want 1", e.run.count())
	}
	got := e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleStarted || got.LastRunID != "run-1" || got.LastRunAt == nil ||
		!got.LastRunAt.Equal(utc("2026-11-10T00:00:00Z")) || !got.NextRunAt.Equal(utc("2026-11-11T00:00:00Z")) || !got.Enabled {
		t.Fatalf("after fire: %+v", got)
	}
	st := e.run.last()
	if st.req.ScheduleID != sc.ID || st.req.Label != "scheduled: nightly" || st.test.YAML != closedYAML {
		t.Fatalf("start = %+v", st.req)
	}
	// No double fire, also not after a restart at the same instant.
	e.s.Tick(ctx)
	e.restart().Tick(ctx)
	if e.run.count() != 1 {
		t.Fatalf("starts = %d after re-tick/restart", e.run.count())
	}
	// Next day.
	e.clock.Set(utc("2026-11-11T00:00:05Z"))
	e.s.Tick(ctx)
	if e.run.count() != 2 {
		t.Fatalf("starts = %d, want 2", e.run.count())
	}
}

func TestOverridesApplied(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(func(s *model.ScheduleSpec) {
		s.Load = &model.LoadOverride{Mode: model.LoadProfile, Rate: 40, RampUpMs: 5000, HoldMs: 60000}
		s.Workers = 3
		s.Label = "nightly-big"
		s.Notes = "from schedule"
	}))
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	st := e.run.last()
	if st.req.Workers != 3 || st.req.Label != "nightly-big" || st.req.Notes != "from schedule" || st.req.ScheduleID != sc.ID {
		t.Fatalf("req = %+v", st.req)
	}
	wantStages(t, parse(t, st.test.YAML), scenario.ModelOpen, stage{5 * time.Second, 40}, stage{time.Minute, 40})
	// The stored test is unchanged.
	stored, _ := e.st.GetTest(context.Background(), e.test.ID)
	if stored.YAML != closedYAML {
		t.Fatal("stored test modified")
	}
}

func TestOnceFiresAndDisables(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, model.ScheduleSpec{Kind: model.ScheduleOnce, At: "2026-11-10T03:30", Timezone: "Europe/Istanbul",
		Load: &model.LoadOverride{Mode: model.LoadScale, Users: 4}})
	if !sc.NextRunAt.Equal(utc("2026-11-10T00:30:00Z")) {
		t.Fatalf("next = %v", sc.NextRunAt)
	}
	e.clock.Set(utc("2026-11-10T00:30:00Z"))
	e.s.Tick(context.Background())
	got := e.get(t, sc.ID)
	if e.run.count() != 1 || got.Enabled || got.NextRunAt != nil || got.LastStatus != model.ScheduleStarted {
		t.Fatalf("starts %d, schedule %+v", e.run.count(), got)
	}
	wantStages(t, parse(t, e.run.last().test.YAML), scenario.ModelClosed,
		stage{10 * time.Second, 2}, stage{30 * time.Second, 4}, stage{10 * time.Second, 0})
	e.clock.Advance(24 * time.Hour)
	e.s.Tick(context.Background())
	if e.run.count() != 1 {
		t.Fatal("once schedule fired again")
	}
}

func TestMissedAcrossRestart(t *testing.T) {
	e := newEnv(t)
	once := e.create(t, model.ScheduleSpec{Name: "once", Kind: model.ScheduleOnce, At: "2026-11-10T03:00", Timezone: "Europe/Istanbul"})
	every := e.create(t, model.ScheduleSpec{Name: "every", Kind: model.ScheduleRecurring, Cron: "*/10 * * * *", Timezone: "UTC"})
	// The server is down from t0 to 00:35Z; a new process starts.
	e.clock.Set(utc("2026-11-10T00:35:00Z"))
	s2 := e.restart()
	s2.Recover(context.Background())
	s2.Tick(context.Background())
	if e.run.count() != 0 {
		t.Fatalf("missed occurrences were run late: %d", e.run.count())
	}
	o := e.get(t, once.ID)
	if o.LastStatus != model.ScheduleMissed || o.Enabled || o.NextRunAt != nil ||
		!strings.Contains(o.LastMessage, "not running at 2026-11-10 03:00 +03") {
		t.Fatalf("once = %+v", o)
	}
	ev := e.get(t, every.ID)
	// Due 00:00, 00:10, 00:20, 00:30 were missed; next 00:40.
	if ev.LastStatus != model.ScheduleMissed || !ev.Enabled || !ev.NextRunAt.Equal(utc("2026-11-10T00:40:00Z")) ||
		!ev.LastRunAt.Equal(utc("2026-11-10T00:30:00Z")) || !strings.HasPrefix(ev.LastMessage, "4 occurrences missed") {
		t.Fatalf("every = %+v", ev)
	}
	e.clock.Set(utc("2026-11-10T00:40:01Z"))
	s2.Tick(context.Background())
	if e.run.count() != 1 {
		t.Fatalf("starts = %d after recovery", e.run.count())
	}
}

func TestSlightlyLateIsNotMissed(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(nil))
	e.clock.Set(sc.NextRunAt.Add(50 * time.Second)) // within the 1 min grace
	e.s.Tick(context.Background())
	if e.run.count() != 1 {
		t.Fatalf("starts = %d", e.run.count())
	}
}

func TestExactlyOnceConcurrentSchedulers(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(nil))
	e.clock.Set(*sc.NextRunAt)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.restart().Tick(context.Background())
		}()
	}
	wg.Wait()
	if e.run.count() != 1 {
		t.Fatalf("starts = %d, want exactly 1", e.run.count())
	}
}

func TestDisabledDoesNotFire(t *testing.T) {
	e := newEnv(t)
	off := false
	sc, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: daily3(func(s *model.ScheduleSpec) {
		s.TestID, s.Name = e.test.ID, "off"
	}), Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Enabled || sc.NextRunAt != nil {
		t.Fatalf("disabled schedule = %+v", sc)
	}
	e.clock.Advance(48 * time.Hour)
	e.s.Tick(context.Background())
	if e.run.count() != 0 {
		t.Fatal("disabled schedule fired")
	}
	// Re-enabling computes the next run from now.
	on := true
	up, err := e.s.Update(context.Background(), sc.ID, model.ScheduleRequest{ScheduleSpec: sc.ScheduleSpec, Enabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !up.Enabled || !up.NextRunAt.Equal(utc("2026-11-12T00:00:00Z")) {
		t.Fatalf("re-enabled = %+v", up)
	}
}

func TestSkipWhenBusy(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(nil))
	e.run.setActive("other")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	got := e.get(t, sc.ID)
	if e.run.count() != 0 || got.LastStatus != model.ScheduleSkipped || !got.NextRunAt.Equal(utc("2026-11-11T00:00:00Z")) {
		t.Fatalf("starts %d, schedule %+v", e.run.count(), got)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueueWaitsForActiveRun(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(func(s *model.ScheduleSpec) { s.Overlap = model.OverlapQueue }))
	e.run.setActive("other")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	got := e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleQueued || !strings.Contains(got.LastMessage, "30m0s") || !got.NextRunAt.Equal(utc("2026-11-11T00:00:00Z")) {
		t.Fatalf("queued = %+v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if e.run.count() != 0 {
		t.Fatal("started while busy")
	}
	e.clock.Advance(10 * time.Minute)
	e.run.setActive()
	waitFor(t, "queued start", func() bool { return e.run.count() == 1 })
	e.s.Wait()
	got = e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleStarted || got.LastRunID != "run-1" || !got.LastRunAt.Equal(utc("2026-11-10T00:00:00Z")) {
		t.Fatalf("after queue = %+v", got)
	}
}

func TestQueueTimeout(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(func(s *model.ScheduleSpec) {
		s.Overlap, s.QueueTimeoutMs = model.OverlapQueue, 5*60*1000
	}))
	e.run.setActive("other")
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	e.clock.Advance(5*time.Minute + time.Second)
	waitFor(t, "skip", func() bool { return e.get(t, sc.ID).LastStatus == model.ScheduleSkipped })
	e.s.Wait()
	if e.run.count() != 0 || !strings.Contains(e.get(t, sc.ID).LastMessage, "after waiting 5m0s") {
		t.Fatalf("starts %d, %+v", e.run.count(), e.get(t, sc.ID))
	}
}

func TestDeleteWhilePendingAndQueued(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pending := e.create(t, daily3(nil))
	queued := e.create(t, daily3(func(s *model.ScheduleSpec) { s.Name, s.Overlap = "q", model.OverlapQueue }))
	if err := e.s.Delete(ctx, pending.ID); err != nil {
		t.Fatal(err)
	}
	e.run.setActive("other")
	e.clock.Set(*queued.NextRunAt)
	e.s.Tick(ctx)
	if err := e.s.Delete(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	e.run.setActive()
	e.s.Wait() // the waiter notices the deletion and exits
	if e.run.count() != 0 {
		t.Fatalf("deleted schedules started %d runs", e.run.count())
	}
	if err := e.s.Delete(ctx, queued.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestShutdownWhileQueuedThenRecover(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(func(s *model.ScheduleSpec) { s.Overlap = model.OverlapQueue }))
	e.run.setActive("other")
	e.clock.Set(*sc.NextRunAt)
	ctx, cancel := context.WithCancel(context.Background())
	e.s.Tick(ctx)
	cancel()
	e.s.Wait()
	if e.get(t, sc.ID).LastStatus != model.ScheduleQueued {
		t.Fatal("expected queued")
	}
	e.restart().Recover(context.Background())
	got := e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleMissed || !strings.Contains(got.LastMessage, "stopped while") || e.run.count() != 0 {
		t.Fatalf("recovered = %+v", got)
	}
}

func TestStartErrorRecorded(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(nil))
	e.run.err = fmt.Errorf("%w: evil.example.com", runner.ErrTargetNotAllowed)
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	got := e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleError || !strings.Contains(got.LastMessage, "allowlist") || got.LastRunID != "" || !got.Enabled {
		t.Fatalf("schedule = %+v", got)
	}
}

func TestFireTimeOverrideMismatch(t *testing.T) {
	// The test changed to the open model after the schedule was saved.
	e := newEnv(t)
	sc := e.create(t, daily3(func(s *model.ScheduleSpec) { s.Load = &model.LoadOverride{Mode: model.LoadScale, Users: 5} }))
	if _, err := e.st.UpdateTest(context.Background(), model.Test{ID: e.test.ID, Name: "shop", YAML: openYAML}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(*sc.NextRunAt)
	e.s.Tick(context.Background())
	got := e.get(t, sc.ID)
	if got.LastStatus != model.ScheduleError || !strings.Contains(got.LastMessage, "open model") || e.run.count() != 0 {
		t.Fatalf("schedule = %+v", got)
	}
}

func TestRunNow(t *testing.T) {
	e := newEnv(t)
	sc := e.create(t, daily3(nil))
	e.run.setActive("other") // run-now ignores the overlap policy
	res, err := e.s.RunNow(context.Background(), sc.ID)
	if err != nil || res.Run == nil || res.SuiteRun != nil || res.Run.ScheduleID != sc.ID {
		t.Fatalf("RunNow = %+v, %v", res, err)
	}
	run := *res.Run
	got := e.get(t, sc.ID)
	if got.LastRunID != run.ID || !got.LastRunAt.Equal(t0) || !got.NextRunAt.Equal(*sc.NextRunAt) {
		t.Fatalf("after run-now: %+v", got)
	}
	if _, err := e.s.RunNow(context.Background(), "00000000-0000-4000-8000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		spec model.ScheduleSpec
		want string
	}{
		{model.ScheduleSpec{TestID: e.test.ID, Name: "x", Kind: "weekly"}, "kind: must be"},
		{daily3(func(s *model.ScheduleSpec) { s.Cron = "61 * * * *" }), "cron: minute: value 61"},
		{daily3(func(s *model.ScheduleSpec) { s.Cron = "" }), "cron: required"},
		{daily3(func(s *model.ScheduleSpec) { s.Cron = "0 0 30 2 *" }), "never matches"},
		{daily3(func(s *model.ScheduleSpec) { s.Timezone = "Mars/Base" }), "timezone: unknown time zone"},
		{model.ScheduleSpec{Kind: model.ScheduleOnce, At: "2026-11-01T10:00", Timezone: "UTC"}, "at: must be in the future"},
		{model.ScheduleSpec{Kind: model.ScheduleOnce, Timezone: "UTC"}, "at: required"},
		{model.ScheduleSpec{Kind: model.ScheduleOnce, At: "soon", Timezone: "UTC"}, "at: must be a local date"},
		{daily3(func(s *model.ScheduleSpec) { s.Name = strings.Repeat("n", 201) }), "name: must be between"},
		{daily3(func(s *model.ScheduleSpec) { s.Workers = -1 }), "workers"},
		{daily3(func(s *model.ScheduleSpec) { s.Label = strings.Repeat("l", 101) }), "label: must be at most 100"},
		{daily3(func(s *model.ScheduleSpec) { s.Overlap = "both" }), "overlap: must be"},
		{daily3(func(s *model.ScheduleSpec) { s.QueueTimeoutMs = 25 * 3600 * 1000 }), "queue_timeout_ms"},
		{daily3(func(s *model.ScheduleSpec) { s.Load = &model.LoadOverride{Mode: model.LoadScale, Rate: 5} }), "load.rate: the scenario uses the closed model"},
		{daily3(func(s *model.ScheduleSpec) { s.TestID = "00000000-0000-4000-8000-000000000000" }), "test_id: no such test"},
	}
	for _, c := range cases {
		if c.spec.TestID == "" {
			c.spec.TestID = e.test.ID
		}
		if c.spec.Name == "" {
			c.spec.Name = "n"
		}
		_, err := e.s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: c.spec})
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(strings.Join(ve.Details, "; "), c.want) {
			t.Errorf("Create(%+v) = %v, want %q", c.spec, err, c.want)
		}
	}
	// A disabled once schedule in the past is fine (e.g. after it fired).
	off := false
	if _, err := e.s.Create(context.Background(), model.ScheduleRequest{Enabled: &off, ScheduleSpec: model.ScheduleSpec{
		TestID: e.test.ID, Name: "old", Kind: model.ScheduleOnce, At: "2026-11-01T10:00", Timezone: "UTC"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPreview(t *testing.T) {
	now := utc("2026-03-27T12:00:00Z")
	p := Preview(model.SchedulePreviewRequest{Kind: model.ScheduleRecurring, Cron: "30 2 * * *", Timezone: "Europe/Berlin", Count: 3}, now)
	if !p.Valid || len(p.Times) != 3 || !p.Times[1].Equal(utc("2026-03-29T01:00:00Z")) {
		t.Fatalf("preview = %+v", p)
	}
	p = Preview(model.SchedulePreviewRequest{Kind: model.ScheduleOnce, At: "2026-03-28T09:00", Timezone: "UTC"}, now)
	if !p.Valid || len(p.Times) != 1 {
		t.Fatalf("once preview = %+v", p)
	}
	p = Preview(model.SchedulePreviewRequest{Kind: model.ScheduleOnce, At: "2026-03-20T09:00", Timezone: "UTC"}, now)
	if p.Valid || len(p.Errors) != 1 || p.Times == nil {
		t.Fatalf("past preview = %+v", p)
	}
	p = Preview(model.SchedulePreviewRequest{Kind: model.ScheduleRecurring, Cron: "* * * * *", Count: 500}, now)
	if len(p.Times) != 50 {
		t.Fatalf("count cap: %d", len(p.Times))
	}
	p = Preview(model.SchedulePreviewRequest{Kind: model.ScheduleRecurring, Cron: "bad", Timezone: "UTC"}, now)
	if p.Valid || len(p.Errors) == 0 {
		t.Fatalf("bad preview = %+v", p)
	}
}

func TestRunLoopAndKick(t *testing.T) {
	st := store.NewMemory()
	run := &fakeRunner{}
	test, _ := st.CreateTest(context.Background(), model.Test{Name: "t", YAML: closedYAML})
	s := New(Options{Store: st, Runner: run, SafetyTick: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	// Created while the loop sleeps (safety tick 1 h): Create kicks it.
	at := time.Now().Add(1500 * time.Millisecond).UTC()
	// Seconds are not part of At: fire at the next whole minute is too
	// slow for a test, so set the stored next run directly.
	sc, err := s.Create(context.Background(), model.ScheduleRequest{ScheduleSpec: model.ScheduleSpec{
		TestID: test.ID, Name: "soon", Kind: model.ScheduleRecurring, Cron: "0 0 1 1 *", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	sc.NextRunAt = &at
	if _, err := st.UpdateSchedule(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	s.Kick()
	waitFor(t, "loop fire", func() bool { return run.count() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
