// Package suite manages test suites (model.Suite): ordered groups of
// saved tests that run together, one after another (sequential) or all
// at once (parallel).
//
// Semantics:
//   - Each test of a suite starts as a normal run through the same
//     runner as the API (same validation, target allowlist and workers),
//     labelled with the suite run's label. Its outcome is copied into the
//     suite run item (status, error, summary without by_step).
//   - Sequential: tests run in order, with GapMs between them; with
//     StopOnFailure a test that does not pass skips the rest. Parallel:
//     every test starts at once (they share the load generator's CPU and
//     network; prefer workers or sequential for clean numbers).
//   - An item may override the test's load (model.LoadOverride, same
//     semantics as schedules): the override is applied to a copy of the
//     scenario when the item starts and noted on the run.
//   - A test that was deleted, or that cannot start (invalid scenario,
//     target not allowed, not enough workers), is recorded as an item
//     error; the suite carries on (unless StopOnFailure).
//   - A suite run ends passed only if every test passed; stopped when the
//     user stopped it; failed otherwise. A suite run that was active when
//     the server stopped is recorded as error on the next start (Recover),
//     like runs.
//   - One active run per suite.
//   - The suite run record is the source of truth for the UI: it is
//     written to the store on every item change.
package suite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// Limits on a suite definition.
const (
	MaxItems       = 50
	MaxNameLen     = 100
	MaxDescLen     = 2000
	MaxLabelLen    = 100
	MaxGapMs       = int64(time.Hour / time.Millisecond)
	MaxItemWorkers = 1000

	// DefaultPoll is how often an active item's run is re-read.
	DefaultPoll  = time.Second
	startTimeout = 30 * time.Second
)

// ErrActive is returned when a suite already has an active run (Start,
// Delete).
var ErrActive = errors.New("suite: suite is running")

// ErrNotRunning is returned by Stop for a suite run that is not active.
var ErrNotRunning = errors.New("suite: suite run is not active")

// ErrShuttingDown is returned by Start after Shutdown.
var ErrShuttingDown = errors.New("suite: server is shutting down")

// ValidationError lists problems with a suite request ("path: message").
type ValidationError struct{ Details []string }

func (e *ValidationError) Error() string {
	return "invalid suite: " + strings.Join(e.Details, "; ")
}

// Starter starts and stops runs (implemented by *runner.Manager). Stop
// returns runner.ErrNotRunning for a run that already ended.
type Starter interface {
	Start(ctx context.Context, test model.Test, req model.StartRunRequest) (model.Run, error)
	Stop(runID string) error
}

// Store is what the service needs from a store.
type Store interface {
	store.SuiteStore
	ListTests(ctx context.Context) ([]model.Test, error)
	GetTest(ctx context.Context, id string) (model.Test, error)
	GetRun(ctx context.Context, id string) (model.Run, error)
}

// Options configures a Service.
type Options struct {
	Store  Store   // required
	Runner Starter // required
	// Now is the clock (tests). nil = time.Now.
	Now    func() time.Time
	Logger *slog.Logger
	// Poll is how often an active item's run is re-read. 0 = DefaultPoll.
	Poll time.Duration
}

// Service manages suites and runs them. It is safe for concurrent use.
type Service struct {
	st     Store
	runner Starter
	now    func() time.Time
	log    *slog.Logger
	poll   time.Duration

	mu     sync.Mutex
	active map[string]*activeRun // suite run ID -> run
	closed bool
	done   chan struct{} // closed by Shutdown
	wg     sync.WaitGroup
}

// activeRun is a suite run in progress.
type activeRun struct {
	cancel context.CancelFunc // stops gaps and further starts

	mu      sync.Mutex // guards run, current, stopped; held while persisting
	run     model.SuiteRun
	current map[string]bool // run IDs of items in progress
	stopped bool
}

// New returns a Service.
func New(o Options) *Service {
	s := &Service{st: o.Store, runner: o.Runner, now: o.Now, log: o.Logger, poll: o.Poll,
		active: make(map[string]*activeRun), done: make(chan struct{})}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.poll <= 0 {
		s.poll = DefaultPoll
	}
	return s
}

// ---- definitions -----------------------------------------------------------

// List returns every suite with item test names and its last run.
func (s *Service) List(ctx context.Context) ([]model.Suite, error) {
	list, err := s.st.ListSuites(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.testNames(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		fillNames(&list[i], names)
		last, err := s.st.ListSuiteRuns(ctx, store.SuiteRunFilter{SuiteID: list[i].ID, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(last) > 0 {
			list[i].LastRun = &last[0]
		}
	}
	return list, nil
}

// Get returns one suite with item test names.
func (s *Service) Get(ctx context.Context, id string) (model.Suite, error) {
	su, err := s.st.GetSuite(ctx, id)
	if err != nil {
		return model.Suite{}, err
	}
	names, err := s.testNames(ctx)
	if err != nil {
		return model.Suite{}, err
	}
	fillNames(&su, names)
	return su, nil
}

// Create validates and stores a new suite.
func (s *Service) Create(ctx context.Context, req model.SuiteRequest) (model.Suite, error) {
	su, err := s.prepare(ctx, req)
	if err != nil {
		return model.Suite{}, err
	}
	out, err := s.st.CreateSuite(ctx, su)
	if err != nil {
		return model.Suite{}, err
	}
	return s.Get(ctx, out.ID)
}

// Update validates and replaces a suite's definition. A run in progress
// keeps the definition it started with.
func (s *Service) Update(ctx context.Context, id string, req model.SuiteRequest) (model.Suite, error) {
	if _, err := s.st.GetSuite(ctx, id); err != nil {
		return model.Suite{}, err
	}
	su, err := s.prepare(ctx, req)
	if err != nil {
		return model.Suite{}, err
	}
	su.ID = id
	if _, err := s.st.UpdateSuite(ctx, su); err != nil {
		return model.Suite{}, err
	}
	return s.Get(ctx, id)
}

// Delete removes a suite and its run history (the test runs stay).
// A suite with an active run: ErrActive.
func (s *Service) Delete(ctx context.Context, id string) error {
	if s.activeFor(id) != "" {
		return ErrActive
	}
	return s.st.DeleteSuite(ctx, id)
}

// prepare validates req and returns the suite to store.
func (s *Service) prepare(ctx context.Context, req model.SuiteRequest) (model.Suite, error) {
	var det []string
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		det = append(det, "name: required")
	case utf8.RuneCountInString(name) > MaxNameLen:
		det = append(det, fmt.Sprintf("name: at most %d characters", MaxNameLen))
	}
	desc := strings.TrimSpace(req.Description)
	if utf8.RuneCountInString(desc) > MaxDescLen {
		det = append(det, fmt.Sprintf("description: at most %d characters", MaxDescLen))
	}
	mode := req.Mode
	switch mode {
	case "":
		mode = model.SuiteSequential
	case model.SuiteSequential, model.SuiteParallel:
	default:
		det = append(det, "mode: must be sequential or parallel")
	}
	if req.GapMs < 0 || req.GapMs > MaxGapMs {
		det = append(det, "gap_ms: must be between 0 and 3600000 (1 hour)")
	}
	switch {
	case len(req.Items) == 0:
		det = append(det, "items: add at least one test")
	case len(req.Items) > MaxItems:
		det = append(det, fmt.Sprintf("items: at most %d tests", MaxItems))
	}
	items := make([]model.SuiteItem, 0, len(req.Items))
	for i, it := range req.Items {
		if it.Workers < 0 || it.Workers > MaxItemWorkers {
			det = append(det, fmt.Sprintf("items[%d].workers: must be between 0 and %d", i, MaxItemWorkers))
		}
		test, err := s.st.GetTest(ctx, it.TestID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			det = append(det, fmt.Sprintf("items[%d].test_id: test not found", i))
		case err != nil:
			return model.Suite{}, err
		}
		var load *model.LoadOverride
		if it.Load != nil {
			l := *it.Load
			load = &l
			for _, d := range scheduler.CheckLoad(load, test.YAML) {
				det = append(det, fmt.Sprintf("items[%d].%s", i, d))
			}
		}
		items = append(items, model.SuiteItem{TestID: it.TestID, Workers: it.Workers, Load: load})
	}
	if len(det) > 0 {
		return model.Suite{}, &ValidationError{Details: det}
	}
	su := model.Suite{Name: name, Description: desc, Mode: mode, StopOnFailure: req.StopOnFailure,
		GapMs: req.GapMs, Items: items}
	if mode == model.SuiteParallel {
		su.StopOnFailure, su.GapMs = false, 0
	}
	return su, nil
}

func (s *Service) testNames(ctx context.Context) (map[string]string, error) {
	tests, err := s.st.ListTests(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(tests))
	for _, t := range tests {
		out[t.ID] = t.Name
	}
	return out, nil
}

func fillNames(su *model.Suite, names map[string]string) {
	for i := range su.Items {
		su.Items[i].TestName = names[su.Items[i].TestID]
	}
}

// ---- runs --------------------------------------------------------------------

// GetRun returns one suite run.
func (s *Service) GetRun(ctx context.Context, id string) (model.SuiteRun, error) {
	return s.st.GetSuiteRun(ctx, id)
}

// ListRuns returns suite runs (of one suite when suiteID != ""), newest
// first.
func (s *Service) ListRuns(ctx context.Context, suiteID string, limit int) ([]model.SuiteRun, error) {
	return s.st.ListSuiteRuns(ctx, store.SuiteRunFilter{SuiteID: suiteID, Limit: limit})
}

// Recover records suite runs left active by a previous process as error
// (call on start, after the runs themselves were marked interrupted).
func (s *Service) Recover(ctx context.Context) (int, error) {
	list, err := s.st.ListSuiteRuns(ctx, store.SuiteRunFilter{Active: true, Limit: 1000})
	if err != nil {
		return 0, err
	}
	for _, r := range list {
		for i := range r.Items {
			switch it := &r.Items[i]; it.Status {
			case model.SuiteItemPending:
				it.Status = model.SuiteItemSkipped
			case string(model.RunQueued), string(model.RunRunning):
				it.Status = string(model.RunError)
				it.Error = "server restarted while the test was running"
			}
		}
		fin := s.now()
		r.Status, r.FinishedAt = model.RunError, &fin
		r.Error = "server restarted while the suite was running"
		if err := s.st.UpdateSuiteRun(ctx, r); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// Start starts a run of the suite in the background and returns it
// (status running).
var _ scheduler.SuiteStarter = (*Service)(nil)

func (s *Service) Start(ctx context.Context, id string, req model.StartSuiteRequest) (model.SuiteRun, error) {
	label := strings.TrimSpace(req.Label)
	if utf8.RuneCountInString(label) > MaxLabelLen {
		return model.SuiteRun{}, &ValidationError{Details: []string{fmt.Sprintf("label: at most %d characters", MaxLabelLen)}}
	}
	su, err := s.Get(ctx, id)
	if err != nil {
		return model.SuiteRun{}, err
	}
	if len(su.Items) == 0 {
		return model.SuiteRun{}, &ValidationError{Details: []string{"items: the suite has no tests"}}
	}

	run := model.SuiteRun{SuiteID: su.ID, SuiteName: su.Name, Status: model.RunRunning, Mode: su.Mode,
		StopOnFailure: su.StopOnFailure, GapMs: su.GapMs, Label: label, StartedAt: s.now(),
		Items: make([]model.SuiteRunItem, 0, len(su.Items))}
	for _, it := range su.Items {
		run.Items = append(run.Items, model.SuiteRunItem{TestID: it.TestID, TestName: it.TestName,
			Workers: it.Workers, Load: it.Load, Status: model.SuiteItemPending})
	}

	// Claim the suite before creating the record so two concurrent starts
	// cannot both run it.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return model.SuiteRun{}, ErrShuttingDown
	}
	claim := "claim:" + su.ID
	if s.activeForLocked(su.ID) != "" || s.active[claim] != nil {
		s.mu.Unlock()
		return model.SuiteRun{}, ErrActive
	}
	s.active[claim] = &activeRun{run: model.SuiteRun{SuiteID: su.ID}}
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		delete(s.active, claim)
		s.mu.Unlock()
	}

	run, err = s.st.CreateSuiteRun(ctx, run)
	if err != nil {
		release()
		return model.SuiteRun{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	// The background run owns its own copy of the items; the returned
	// value is the caller's.
	own := run
	own.Items = slices.Clone(run.Items)
	a := &activeRun{cancel: cancel, run: own, current: make(map[string]bool)}
	s.mu.Lock()
	delete(s.active, claim)
	s.active[run.ID] = a
	s.wg.Add(1)
	s.mu.Unlock()

	go s.execute(runCtx, a)
	return run, nil
}

// Stop stops an active suite run: no further test starts and the tests
// in progress are stopped (their partial results are kept).
func (s *Service) Stop(id string) error {
	s.mu.Lock()
	a := s.active[id]
	s.mu.Unlock()
	if a == nil || strings.HasPrefix(id, "claim:") {
		return ErrNotRunning
	}
	a.mu.Lock()
	a.stopped = true
	ids := make([]string, 0, len(a.current))
	for rid := range a.current {
		ids = append(ids, rid)
	}
	a.mu.Unlock()
	a.cancel()
	for _, rid := range ids {
		s.stopRun(rid)
	}
	return nil
}

// Active lists the IDs of suite runs in progress.
func (s *Service) Active() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.active))
	for id := range s.active {
		if !strings.HasPrefix(id, "claim:") {
			out = append(out, id)
		}
	}
	return out
}

// Shutdown stops starting tests, records active suite runs as error and
// waits (until ctx is done) for them to end. Call it before the runner's
// Shutdown.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		for _, a := range s.active {
			if a.cancel != nil {
				a.cancel()
			}
		}
	}
	s.mu.Unlock()
	ch := make(chan struct{})
	go func() { s.wg.Wait(); close(ch) }()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) activeFor(suiteID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeForLocked(suiteID)
}

func (s *Service) activeForLocked(suiteID string) string {
	for id, a := range s.active {
		if a.run.SuiteID == suiteID {
			return id
		}
	}
	return ""
}

func (s *Service) stopRun(runID string) {
	if err := s.runner.Stop(runID); err != nil && !errors.Is(err, runner.ErrNotRunning) {
		s.log.Warn("suite: stop run", slog.String("run_id", runID), slog.String("error", err.Error()))
	}
}

// ---- execution ---------------------------------------------------------------

func (s *Service) execute(ctx context.Context, a *activeRun) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.active, a.run.ID)
		s.mu.Unlock()
		a.cancel()
	}()

	n := len(a.run.Items)
	if a.run.Mode == model.SuiteParallel {
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runItem(ctx, a, i)
			}()
		}
		wg.Wait()
	} else {
		for i := range n {
			if i > 0 && a.run.GapMs > 0 && ctx.Err() == nil && !s.halt(a) {
				t := time.NewTimer(time.Duration(a.run.GapMs) * time.Millisecond)
				select {
				case <-ctx.Done():
				case <-t.C:
				}
				t.Stop()
			}
			if ctx.Err() != nil || s.halt(a) {
				break
			}
			s.runItem(ctx, a, i)
		}
	}
	s.finish(a)
}

// halt reports whether a sequential suite must stop starting tests
// (StopOnFailure and an item did not pass).
func (s *Service) halt(a *activeRun) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.run.StopOnFailure {
		return false
	}
	for _, it := range a.run.Items {
		if it.Status != model.SuiteItemPending && it.Status != string(model.RunPassed) {
			return true
		}
	}
	return false
}

// update applies fn to item i under the lock and persists the suite run.
func (s *Service) update(a *activeRun, fn func(r *model.SuiteRun)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(&a.run)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.st.UpdateSuiteRun(ctx, a.run); err != nil {
		s.log.Error("suite: save suite run", slog.String("suite_run_id", a.run.ID), slog.String("error", err.Error()))
	}
}

func (s *Service) runItem(ctx context.Context, a *activeRun, i int) {
	if ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	item := a.run.Items[i]
	label, suiteName := a.run.Label, a.run.SuiteName
	a.mu.Unlock()

	sctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	test, err := s.st.GetTest(sctx, item.TestID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		err = errors.New("test was deleted")
	case err == nil:
		err = s.startItem(sctx, &item, test, label, suiteName)
	}
	cancel()
	start := s.now()
	if err != nil {
		s.update(a, func(r *model.SuiteRun) {
			it := &r.Items[i]
			it.Status, it.Error, it.StartedAt, it.FinishedAt = string(model.RunError), err.Error(), &start, &start
		})
		return
	}

	stopNow := false
	s.update(a, func(r *model.SuiteRun) {
		it := &r.Items[i]
		it.RunID, it.Status, it.StartedAt = item.RunID, item.Status, &start
		a.current[item.RunID] = true
		stopNow = a.stopped
	})
	if stopNow { // Stop raced with the start
		s.stopRun(item.RunID)
	}
	s.wait(a, i, item.RunID)
	a.mu.Lock()
	delete(a.current, item.RunID)
	a.mu.Unlock()
}

// startItem starts the run of one item (with its load override applied
// to a copy of the scenario) and fills item.RunID and item.Status.
func (s *Service) startItem(ctx context.Context, item *model.SuiteRunItem, test model.Test, label, suiteName string) error {
	req := model.StartRunRequest{Workers: item.Workers, Label: label}
	if item.Load != nil {
		y, err := scheduler.ApplyLoad(test.YAML, item.Load)
		if err != nil {
			return fmt.Errorf("load override: %w", err)
		}
		test.YAML = y
		req.Notes = fmt.Sprintf("Started by suite %q with a load override: %s (the saved test's load was not used).",
			suiteName, scheduler.DescribeLoad(item.Load))
	}
	run, err := s.runner.Start(ctx, test, req)
	if err != nil {
		return err
	}
	item.RunID, item.Status = run.ID, string(run.Status)
	return nil
}

// wait polls the item's run until it finishes and copies its outcome.
func (s *Service) wait(a *activeRun, i int, runID string) {
	t := time.NewTicker(s.poll)
	defer t.Stop()
	last := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		run, err := s.st.GetRun(ctx, runID)
		cancel()
		switch {
		case errors.Is(err, store.ErrNotFound):
			fin := s.now()
			s.update(a, func(r *model.SuiteRun) {
				it := &r.Items[i]
				it.Status, it.Error, it.FinishedAt = string(model.RunError), "run was deleted", &fin
			})
			return
		case err != nil:
			s.log.Warn("suite: read run", slog.String("run_id", runID), slog.String("error", err.Error()))
		case run.Status.Finished():
			s.update(a, func(r *model.SuiteRun) {
				it := &r.Items[i]
				it.Status, it.Error = string(run.Status), run.Error
				fin := s.now()
				if run.FinishedAt != nil {
					fin = *run.FinishedAt
				}
				it.FinishedAt = &fin
				if run.Summary != nil {
					sum := *run.Summary
					sum.ByStep = nil
					it.Summary = &sum
				}
			})
			return
		case string(run.Status) != last:
			last = string(run.Status)
			s.update(a, func(r *model.SuiteRun) { r.Items[i].Status = last })
		}
		select {
		case <-t.C:
		case <-s.done:
			// Server shutting down: the runner stops the run; record it.
			fin := s.now()
			s.update(a, func(r *model.SuiteRun) {
				it := &r.Items[i]
				it.Status, it.Error, it.FinishedAt = string(model.RunError), "server stopped while the test was running", &fin
			})
			return
		}
	}
}

// finish sets the final status of the suite run and persists it.
func (s *Service) finish(a *activeRun) {
	closed := false
	select {
	case <-s.done:
		closed = true
	default:
	}
	s.update(a, func(r *model.SuiteRun) {
		allPassed := true
		for k := range r.Items {
			it := &r.Items[k]
			if it.Status == model.SuiteItemPending {
				it.Status = model.SuiteItemSkipped
			}
			if it.Status != string(model.RunPassed) {
				allPassed = false
			}
		}
		fin := s.now()
		r.FinishedAt = &fin
		switch {
		case closed:
			r.Status, r.Error = model.RunError, "server stopped while the suite was running"
		case a.stopped:
			r.Status = model.RunStopped
		case allPassed:
			r.Status = model.RunPassed
		default:
			r.Status = model.RunFailed
		}
	})
}
