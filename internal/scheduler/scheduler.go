// Package scheduler starts runs of saved tests at planned times
// (model.Schedule): once at a local date and time, or repeatedly on a
// 5-field cron expression, both in an IANA time zone.
//
// Semantics:
//   - The server must be running at fire time. An occurrence noticed more
//     than Options.MissedGrace (default 1 min) after it was due - because
//     the server was down - is NOT run late: it is recorded as missed and
//     the schedule moves on to its next occurrence.
//   - Exactly once: before a run starts, the occurrence is claimed by
//     advancing next_run_at in the store with a compare-and-set
//     (ScheduleStore.AdvanceSchedule). A restart, a second scheduler on
//     the same database or a concurrent edit can never start the same
//     occurrence twice. (A crash between the claim and the run start
//     loses that occurrence instead.)
//   - Overlap: "skip" skips an occurrence while any run or suite run is
//     active on this server; "queue" waits until none is active (at most the queue
//     timeout, default 30 min), then starts it or records it as skipped.
//     An occurrence still queued when the server stops is recorded as
//     missed on the next start.
//   - Once schedules disable themselves after their occurrence (whatever
//     its outcome).
//   - Runs start through the same runner.Manager.Start as the API (same
//     target allowlist and validation) with the test's scenario after the
//     schedule's load override (ApplyLoad) and record Run.ScheduleID.
//   - A schedule may target a test suite instead (ScheduleSpec.SuiteID):
//     the occurrence then starts a suite run through Options.Suites and
//     Schedule.LastRunID is the suite run ID. Suite schedules carry no load
//     override or workers (the suite items do).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// Defaults for Options.
const (
	DefaultMissedGrace = time.Minute
	DefaultSafetyTick  = time.Minute
	DefaultQueuePoll   = 2 * time.Second
	startTimeout       = 30 * time.Second
)

// Starter starts runs (implemented by *runner.Manager).
type Starter interface {
	Start(ctx context.Context, test model.Test, req model.StartRunRequest) (model.Run, error)
	Active() []string
}

// SuiteStarter starts suite runs (implemented by *suite.Service).
type SuiteStarter interface {
	Start(ctx context.Context, id string, req model.StartSuiteRequest) (model.SuiteRun, error)
	Active() []string
}

// RunResult is what RunNow started: a Run for a test schedule, a
// SuiteRun for a suite schedule (exactly one is set).
type RunResult struct {
	Run      *model.Run
	SuiteRun *model.SuiteRun
}

// Store is what the scheduler needs from a store.
type Store interface {
	store.ScheduleStore
	GetTest(ctx context.Context, id string) (model.Test, error)
	// GetSuite checks that a suite schedule's suite exists.
	GetSuite(ctx context.Context, id string) (model.Suite, error)
}

// Options configures a Scheduler.
type Options struct {
	Store  Store   // required
	Runner Starter // required
	// Suites starts suite runs for suite schedules; nil = suite schedules
	// fail with an error.
	Suites SuiteStarter
	// Now is the clock (tests). nil = time.Now.
	Now    func() time.Time
	Logger *slog.Logger
	// MissedGrace: an occurrence handled later than this after its due
	// time counts as missed. 0 = DefaultMissedGrace.
	MissedGrace time.Duration
	// SafetyTick is the longest the loop sleeps without re-reading the
	// store. 0 = DefaultSafetyTick.
	SafetyTick time.Duration
	// QueuePoll is how often a queued occurrence checks for active runs.
	// 0 = DefaultQueuePoll.
	QueuePoll time.Duration
}

// Scheduler fires due schedules. Create it with New, start the loop
// with Run; the CRUD methods may be used before and while it runs.
type Scheduler struct {
	st      Store
	runner  Starter
	suites  SuiteStarter
	now     func() time.Time
	log     *slog.Logger
	grace   time.Duration
	tick    time.Duration
	poll    time.Duration
	kick    chan struct{}
	startMu sync.Mutex // serialises "check active runs + start"
	tickMu  sync.Mutex // one Tick at a time
	wg      sync.WaitGroup
}

// New returns a Scheduler.
func New(o Options) *Scheduler {
	s := &Scheduler{st: o.Store, runner: o.Runner, suites: o.Suites, now: o.Now, log: o.Logger,
		grace: o.MissedGrace, tick: o.SafetyTick, poll: o.QueuePoll, kick: make(chan struct{}, 1)}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.grace <= 0 {
		s.grace = DefaultMissedGrace
	}
	if s.tick <= 0 {
		s.tick = DefaultSafetyTick
	}
	if s.poll <= 0 {
		s.poll = DefaultQueuePoll
	}
	return s
}

// Kick makes the loop re-read the schedules now (after an edit).
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run recovers state left by a previous process, then fires schedules
// until ctx is done. It returns after queued occurrences have stopped.
func (s *Scheduler) Run(ctx context.Context) {
	s.Recover(ctx)
	for {
		wait := s.Tick(ctx)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			s.wg.Wait()
			return
		case <-t.C:
		case <-s.kick:
			t.Stop()
		}
	}
}

// Wait blocks until every queued occurrence goroutine has finished.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Recover records occurrences that were queued when the previous process
// stopped as missed. Run calls it once at start.
func (s *Scheduler) Recover(ctx context.Context) {
	list, err := s.st.ListSchedules(ctx, "")
	if err != nil {
		s.log.Warn("scheduler: list schedules", "error", err)
		return
	}
	for _, sc := range list {
		if sc.LastStatus != model.ScheduleQueued {
			continue
		}
		r := store.ScheduleResult{Status: model.ScheduleMissed,
			Message: "the server stopped while this occurrence was queued"}
		if _, err := s.st.SetScheduleResult(ctx, sc.ID, r); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("scheduler: record result", "schedule", sc.ID, "error", err)
		}
	}
}

// Tick handles every due schedule once and returns how long to sleep
// until the next one is due (at most the safety tick).
func (s *Scheduler) Tick(ctx context.Context) time.Duration {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	list, err := s.st.ListSchedules(ctx, "")
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("scheduler: list schedules", "error", err)
		}
		return min(s.tick, 5*time.Second)
	}
	now := s.now()
	wait := s.tick
	for _, sc := range list {
		if !sc.Enabled || sc.NextRunAt == nil {
			continue
		}
		next := sc.NextRunAt
		if !next.After(now) {
			next = s.handleDue(ctx, sc, now)
		}
		if next != nil {
			wait = min(wait, max(next.Sub(now), 0))
		}
	}
	return wait
}

// handleDue claims and handles the due occurrence of sc. It returns the
// schedule's new next run (nil = none).
func (s *Scheduler) handleDue(ctx context.Context, sc model.Schedule, now time.Time) *time.Time {
	due := *sc.NextRunAt
	var details []string
	t := parseTiming(sc.Kind, sc.At, sc.Cron, sc.Timezone, &details)
	if t == nil {
		s.advance(ctx, sc, due, store.ScheduleAdvance{Result: store.ScheduleResult{At: due,
			Status: model.ScheduleError, Message: "invalid schedule: " + strings.Join(details, "; ")}})
		return nil
	}
	var next *time.Time
	if sc.Kind == model.ScheduleRecurring {
		if n, ok := t.next(now); ok {
			n = n.UTC()
			next = &n
		}
	}
	adv := store.ScheduleAdvance{NextRunAt: next, Enabled: next != nil}

	if late := now.Sub(due); late > s.grace {
		missed, last := 1, due
		for n, ok := t.next(due); ok && !n.After(now) && missed < 1000; n, ok = t.next(n) {
			missed, last = missed+1, n
		}
		msg := fmt.Sprintf("the server was not running at %s", fmtLocal(last, t))
		if missed > 1 {
			msg = fmt.Sprintf("%d occurrences missed while the server was not running (last %s)", missed, fmtLocal(last, t))
		}
		adv.Result = store.ScheduleResult{At: last, Status: model.ScheduleMissed, Message: msg}
		if s.advance(ctx, sc, due, adv) {
			s.log.Info("scheduler: missed", "schedule", sc.ID, "name", sc.Name, "due", due, "count", missed)
		}
		return next
	}

	s.startMu.Lock()
	busy := s.busy()
	switch {
	case busy && sc.Overlap == model.OverlapQueue:
		timeout := QueueTimeout(sc.ScheduleSpec)
		adv.Result = store.ScheduleResult{At: due, Status: model.ScheduleQueued,
			Message: fmt.Sprintf("waiting up to %s for active runs to finish", timeout)}
		ok := s.advance(ctx, sc, due, adv)
		s.startMu.Unlock()
		if ok {
			s.log.Info("scheduler: queued", "schedule", sc.ID, "name", sc.Name, "due", due)
			s.wg.Add(1)
			go s.waitAndStart(ctx, sc, due, now.Add(timeout))
		}
	case busy:
		adv.Result = store.ScheduleResult{At: due, Status: model.ScheduleSkipped,
			Message: "another run was active (overlap policy: skip)"}
		ok := s.advance(ctx, sc, due, adv)
		s.startMu.Unlock()
		if ok {
			s.log.Info("scheduler: skipped, another run is active", "schedule", sc.ID, "name", sc.Name)
		}
	default:
		adv.Result = store.ScheduleResult{At: due, Status: model.ScheduleStarted}
		if s.advance(ctx, sc, due, adv) {
			_, _ = s.start(ctx, sc, due)
		}
		s.startMu.Unlock()
	}
	return next
}

// busy reports whether any run or suite run is active.
func (s *Scheduler) busy() bool {
	return len(s.runner.Active()) > 0 || (s.suites != nil && len(s.suites.Active()) > 0)
}

func fmtLocal(t time.Time, tm *timing) string {
	return t.In(tm.loc).Format("2006-01-02 15:04 MST")
}

// advance claims the occurrence due of sc; false when it was handled or
// changed elsewhere (or on a store error, which is logged).
func (s *Scheduler) advance(ctx context.Context, sc model.Schedule, due time.Time, a store.ScheduleAdvance) bool {
	_, err := s.st.AdvanceSchedule(ctx, sc.ID, due, a)
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
		return false
	default:
		if ctx.Err() == nil {
			s.log.Warn("scheduler: advance schedule", "schedule", sc.ID, "error", err)
		}
		return false
	}
}

// waitAndStart runs a queued occurrence once no run is active, or gives
// up at deadline.
func (s *Scheduler) waitAndStart(ctx context.Context, sc model.Schedule, due, deadline time.Time) {
	defer s.wg.Done()
	for {
		cur, err := s.st.GetSchedule(ctx, sc.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return // deleted while queued
		case err != nil:
			if ctx.Err() != nil {
				return // shutting down; Recover marks it missed
			}
		case cur.LastStatus != model.ScheduleQueued || (cur.Kind == model.ScheduleRecurring && !cur.Enabled):
			if cur.LastStatus == model.ScheduleQueued {
				s.result(ctx, sc.ID, store.ScheduleResult{Status: model.ScheduleSkipped,
					Message: "the schedule was disabled while queued"})
			}
			return
		default:
			s.startMu.Lock()
			if !s.busy() {
				_, _ = s.start(ctx, cur, due)
				s.startMu.Unlock()
				return
			}
			s.startMu.Unlock()
			if !s.now().Before(deadline) {
				s.result(ctx, sc.ID, store.ScheduleResult{Status: model.ScheduleSkipped,
					Message: fmt.Sprintf("another run was still active after waiting %s", QueueTimeout(sc.ScheduleSpec))})
				return
			}
		}
		t := time.NewTimer(s.poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (s *Scheduler) result(ctx context.Context, id string, r store.ScheduleResult) {
	if _, err := s.st.SetScheduleResult(ctx, id, r); err != nil && !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
		s.log.Warn("scheduler: record result", "schedule", id, "error", err)
	}
}

// start starts a run of sc's test (or a suite run of its suite) with its
// overrides and records the outcome as the schedule's last result (at =
// due time or run-now time).
func (s *Scheduler) start(ctx context.Context, sc model.Schedule, at time.Time) (RunResult, error) {
	var (
		res RunResult
		id  string
		err error
	)
	if sc.SuiteID != "" {
		var sr model.SuiteRun
		if sr, err = s.startSuite(ctx, sc); err == nil {
			res.SuiteRun, id = &sr, sr.ID
		}
	} else {
		var run model.Run
		if run, err = s.startRun(ctx, sc); err == nil {
			res.Run, id = &run, run.ID
		}
	}
	r := store.ScheduleResult{At: at, Status: model.ScheduleStarted, RunID: id}
	if err != nil {
		r = store.ScheduleResult{At: at, Status: model.ScheduleError, Message: err.Error()}
		s.log.Warn("scheduler: start run", "schedule", sc.ID, "name", sc.Name, "error", r.Message)
	} else {
		s.log.Info("scheduler: started run", "schedule", sc.ID, "name", sc.Name, "run", id)
	}
	s.result(context.WithoutCancel(ctx), sc.ID, r)
	return res, err
}

func (s *Scheduler) startSuite(ctx context.Context, sc model.Schedule) (model.SuiteRun, error) {
	if s.suites == nil {
		return model.SuiteRun{}, errors.New("test suites are not available on this server")
	}
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	return s.suites.Start(sctx, sc.SuiteID, model.StartSuiteRequest{Label: RunLabel(sc.ScheduleSpec)})
}

func (s *Scheduler) startRun(ctx context.Context, sc model.Schedule) (model.Run, error) {
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	test, err := s.st.GetTest(sctx, sc.TestID)
	if err != nil {
		return model.Run{}, fmt.Errorf("load test: %w", err)
	}
	y, err := ApplyLoad(test.YAML, sc.Load)
	if err != nil {
		return model.Run{}, err
	}
	test.YAML = y
	return s.runner.Start(sctx, test, model.StartRunRequest{
		Workers: sc.Workers, Label: RunLabel(sc.ScheduleSpec), Notes: sc.Notes, ScheduleID: sc.ID,
	})
}

// ---- CRUD used by the API ---------------------------------------------------

// List returns schedules (of one test when testID != "").
func (s *Scheduler) List(ctx context.Context, testID string) ([]model.Schedule, error) {
	return s.st.ListSchedules(ctx, testID)
}

// Get returns one schedule.
func (s *Scheduler) Get(ctx context.Context, id string) (model.Schedule, error) {
	return s.st.GetSchedule(ctx, id)
}

// prepare normalizes and validates req and computes NextRunAt.
func (s *Scheduler) prepare(ctx context.Context, req model.ScheduleRequest) (model.Schedule, error) {
	spec := req.ScheduleSpec
	Normalize(&spec)
	enabled := req.Enabled == nil || *req.Enabled
	var yaml string
	if spec.SuiteID != "" && spec.TestID == "" {
		_, err := s.st.GetSuite(ctx, spec.SuiteID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return model.Schedule{}, &ValidationError{Details: []string{"items/suite_id: no such suite"}}
		case err != nil:
			return model.Schedule{}, err
		}
	} else if spec.TestID != "" && spec.SuiteID == "" {
		test, err := s.st.GetTest(ctx, spec.TestID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return model.Schedule{}, &ValidationError{Details: []string{"test_id: no such test"}}
		case err != nil:
			return model.Schedule{}, err
		}
		yaml = test.YAML
	}
	now := s.now()
	if err := Validate(spec, enabled, yaml, now); err != nil {
		return model.Schedule{}, err
	}
	out := model.Schedule{ScheduleSpec: spec, Enabled: enabled}
	if enabled {
		next, err := NextRun(spec, now)
		if err != nil {
			return model.Schedule{}, err
		}
		out.NextRunAt = next
		out.Enabled = next != nil
	}
	return out, nil
}

// Create validates and stores a new schedule.
func (s *Scheduler) Create(ctx context.Context, req model.ScheduleRequest) (model.Schedule, error) {
	sc, err := s.prepare(ctx, req)
	if err != nil {
		return model.Schedule{}, err
	}
	out, err := s.st.CreateSchedule(ctx, sc)
	if err == nil {
		s.Kick()
	}
	return out, err
}

// Update validates and replaces a schedule's definition; the next run is
// recomputed from now.
func (s *Scheduler) Update(ctx context.Context, id string, req model.ScheduleRequest) (model.Schedule, error) {
	if _, err := s.st.GetSchedule(ctx, id); err != nil {
		return model.Schedule{}, err
	}
	sc, err := s.prepare(ctx, req)
	if err != nil {
		return model.Schedule{}, err
	}
	sc.ID = id
	out, err := s.st.UpdateSchedule(ctx, sc)
	if err == nil {
		s.Kick()
	}
	return out, err
}

// Delete removes a schedule. A queued occurrence of it is dropped.
func (s *Scheduler) Delete(ctx context.Context, id string) error {
	err := s.st.DeleteSchedule(ctx, id)
	if err == nil {
		s.Kick()
	}
	return err
}

// RunNow starts a run (or, for a suite schedule, a suite run) of the
// schedule immediately, regardless of its timing, enabled state and
// overlap policy; next_run_at is unchanged.
func (s *Scheduler) RunNow(ctx context.Context, id string) (RunResult, error) {
	sc, err := s.st.GetSchedule(ctx, id)
	if err != nil {
		return RunResult{}, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	return s.start(ctx, sc, s.now())
}

// Preview returns the next fire times of a definition.
func (s *Scheduler) Preview(req model.SchedulePreviewRequest) model.SchedulePreview {
	return Preview(req, s.now())
}
