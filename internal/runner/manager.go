package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/executor"
	"github.com/hakanngul/GoLoadTestingApp/internal/provision"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// DefaultMaxErrorSamples is used when Options.MaxErrorSamples is 0.
const DefaultMaxErrorSamples = 100

// defaultSubscriberBuffer is the per-subscriber event buffer. A subscriber
// that falls further behind loses metric events (never the done event).
const defaultSubscriberBuffer = 64

// ErrDistributedUnavailable is returned by Start when more than one worker
// is requested; only the in-process worker exists until Phase 4.
var ErrDistributedUnavailable = errors.New("runner: distributed workers not available yet")

// ErrShuttingDown is returned by Start after Shutdown has been called.
var ErrShuttingDown = errors.New("runner: shutting down")

// ValidationError reports why a test cannot be started because of its
// content (scenario, data file, request). Details are "path: message"
// strings suitable for model.APIError.Details.
type ValidationError struct {
	Details []string
}

func (e *ValidationError) Error() string {
	if len(e.Details) == 0 {
		return "runner: invalid scenario"
	}
	return "runner: invalid scenario: " + strings.Join(e.Details, "; ")
}

// ValidationDetails returns Details. The API layer detects validation
// errors through this method (errors.As on an interface) to answer 400.
func (e *ValidationError) ValidationDetails() []string { return e.Details }

// newValidationError flattens err (possibly an errors.Join of
// *scenario.FieldError values) into a ValidationError.
func newValidationError(err error) *ValidationError {
	var details []string
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			details = append(details, e.Error())
		}
	}
	if len(details) == 0 {
		details = []string{err.Error()}
	}
	return &ValidationError{Details: details}
}

// Options configures a Manager.
type Options struct {
	// Store persists runs, metric points and error samples. Required.
	Store store.Store
	// MaxErrorSamples caps the failed requests kept per run. 0 means
	// DefaultMaxErrorSamples; a negative value disables sampling.
	MaxErrorSamples int
	// Allow vets the scenario's target base URL before a run starts. A
	// non-nil error rejects the run (wrapped in ErrTargetNotAllowed).
	// nil allows every target.
	Allow func(baseURL string) error
	// Now supplies timestamps for run records, metric points and error
	// samples (tests). nil means time.Now. Load timing always uses the
	// real clock.
	Now func() time.Time
	// Version is reported for the local pseudo-worker.
	Version string
	// Logger receives store write failures and other non-fatal problems.
	// nil means slog.Default().
	Logger *slog.Logger

	// Dispatcher runs tests on remote workers (Phase 4). nil = local only.
	Dispatcher cluster.Dispatcher
	// Provisioner launches workers on demand when too few are idle (e.g.
	// AWS spot). nil = never launch; use connected workers only.
	Provisioner provision.Provisioner
	// MasterAddr is the gRPC address launched workers dial.
	MasterAddr string
	// WorkerToken authenticates launched workers.
	WorkerToken string
	// ProvisionTimeout bounds waiting for launched workers to connect.
	// 0 means DefaultProvisionTimeout.
	ProvisionTimeout time.Duration
}

// Manager runs tests in-process. It implements Runner and is safe for
// concurrent use; any number of runs may be active at once, each fully
// independent.
type Manager struct {
	store      store.Store
	maxSamples int
	allow      func(string) error
	now        func() time.Time
	version    string
	log        *slog.Logger
	subBuffer  int // per-subscriber channel capacity (tests shrink it)
	cpuBits    atomic.Uint64

	dispatcher       cluster.Dispatcher
	provisioner      provision.Provisioner
	masterAddr       string
	workerToken      string
	provisionTimeout time.Duration

	mu     sync.Mutex
	runs   map[string]*activeRun
	closed bool
	wg     sync.WaitGroup // one per run goroutine
}

var _ Runner = (*Manager)(nil)

// NewManager returns a Manager using opts (see Options for defaults).
func NewManager(opts Options) *Manager {
	m := &Manager{
		store:      opts.Store,
		maxSamples: opts.MaxErrorSamples,
		allow:      opts.Allow,
		now:        opts.Now,
		version:    opts.Version,
		log:        opts.Logger,
		subBuffer:  defaultSubscriberBuffer,
		runs:       make(map[string]*activeRun),

		dispatcher:       opts.Dispatcher,
		provisioner:      opts.Provisioner,
		masterAddr:       opts.MasterAddr,
		workerToken:      opts.WorkerToken,
		provisionTimeout: opts.ProvisionTimeout,
	}
	if m.provisionTimeout <= 0 {
		m.provisionTimeout = DefaultProvisionTimeout
	}
	switch {
	case m.maxSamples == 0:
		m.maxSamples = DefaultMaxErrorSamples
	case m.maxSamples < 0:
		m.maxSamples = 0
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	return m
}

// Start validates the test's scenario, loads its data file, creates the
// run record (status running) and starts the run in the background.
//
// Errors returned before a run record is created:
//   - *ValidationError for an invalid scenario, a data file that is not
//     attached to the test or is not valid CSV, or a negative worker count;
//   - an error wrapping ErrTargetNotAllowed when Options.Allow rejects the
//     target;
//   - ErrDistributedUnavailable when req.Workers > 1 and no Dispatcher
//     is configured; cluster.ErrNotEnoughWorkers when too few workers are
//     idle and no Provisioner can launch more;
//   - ErrShuttingDown after Shutdown;
//   - store errors (wrapped).
//
// ctx bounds only the setup (store reads/writes); the run itself lives
// until it completes, Stop is called or the Manager shuts down.
func (m *Manager) Start(ctx context.Context, test model.Test, req model.StartRunRequest) (model.Run, error) {
	if m.isClosed() {
		return model.Run{}, ErrShuttingDown
	}
	sc, err := scenario.Parse([]byte(test.YAML))
	if err != nil {
		return model.Run{}, newValidationError(err)
	}
	if m.allow != nil {
		for _, u := range targetURLs(sc) {
			if err := m.allow(u); err != nil {
				if errors.Is(err, ErrTargetNotAllowed) {
					return model.Run{}, err
				}
				return model.Run{}, fmt.Errorf("%w: %w", ErrTargetNotAllowed, err)
			}
		}
	}
	workers := req.Workers
	switch {
	case workers < 0:
		return model.Run{}, &ValidationError{Details: []string{"workers: must not be negative"}}
	case workers == 0:
		workers = 1
	}
	plan, err := m.planRemote(workers)
	if err != nil {
		return model.Run{}, err
	}

	// Building the feeder and executor validates the data file and the
	// steps up front, also for remote runs (workers repeat it).
	feeder, err := m.loadFeeder(ctx, test, sc)
	if err != nil {
		return model.Run{}, err
	}
	exec, closeExec, err := executor.New(sc, executor.Options{})
	if err != nil {
		return model.Run{}, &ValidationError{Details: []string{err.Error()}}
	}

	status := model.RunRunning
	if plan != nil {
		closeExec()
		if plan.files, _, err = m.dataFiles(ctx, test, sc); err != nil {
			return model.Run{}, err
		}
		workers = plan.n
		if plan.provision > 0 {
			status = model.RunQueued
		}
	}

	run, err := m.store.CreateRun(ctx, model.Run{
		TestID:     test.ID,
		TestName:   test.Name,
		Status:     status,
		YAML:       test.YAML,
		Workers:    workers,
		StartedAt:  m.now(),
		Thresholds: []model.ThresholdResult{},
		Warnings:   []string{},
	})
	if err != nil {
		if plan == nil {
			closeExec()
		}
		return model.Run{}, fmt.Errorf("runner: create run: %w", err)
	}
	if run.Thresholds == nil {
		run.Thresholds = []model.ThresholdResult{}
	}
	if run.Warnings == nil {
		run.Warnings = []string{}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	ar := &activeRun{
		cancel: cancel,
		remote: plan != nil,
		run:    run,
		subs:   make(map[*subscriber]struct{}),
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		if plan == nil {
			closeExec()
		}
		run.Status = model.RunError
		run.Error = "server shutting down"
		fin := m.now()
		run.FinishedAt = &fin
		m.updateRun(run)
		return model.Run{}, ErrShuttingDown
	}
	m.runs[run.ID] = ar
	m.wg.Add(1)
	m.mu.Unlock()

	if plan != nil {
		go m.executeRemote(runCtx, ar, sc, test.YAML, plan)
	} else {
		go m.execute(runCtx, ar, sc, exec, closeExec, feeder)
	}
	return run, nil
}

// loadFeeder returns the data feeder for sc. A data file is fetched from
// the store, written to a private temp dir and loaded fully into memory by
// data.NewCSV; the temp dir is removed before returning since the CSV
// feeder no longer needs it.
func (m *Manager) loadFeeder(ctx context.Context, test model.Test, sc *scenario.Scenario) (data.Feeder, error) {
	if sc.Data == nil {
		return data.None{}, nil
	}
	// Parse leaves data.file as written; files are stored by base name.
	name := filepath.Base(sc.Data.File)
	content, err := m.store.GetFile(ctx, test.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &ValidationError{Details: []string{
			fmt.Sprintf("data.file: file %q is not attached to this test; upload it first", name),
		}}
	}
	if err != nil {
		return nil, fmt.Errorf("runner: load data file %q: %w", name, err)
	}

	dir, err := os.MkdirTemp("", "goloadtest-run-*")
	if err != nil {
		return nil, fmt.Errorf("runner: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("runner: write data file: %w", err)
	}
	feeder, err := data.NewCSV(path, sc.Data.Mode)
	if err != nil {
		// Do not leak the temp path into user-facing messages.
		msg := strings.ReplaceAll(err.Error(), path, name)
		return nil, &ValidationError{Details: []string{"data.file: " + msg}}
	}
	return feeder, nil
}

// Stop requests a graceful stop of an active run. The run ends with status
// stopped and a partial summary. It returns ErrNotRunning for unknown or
// finished runs; a repeated Stop on a run still stopping returns nil.
func (m *Manager) Stop(runID string) error {
	ar := m.lookup(runID)
	if ar == nil {
		return ErrNotRunning
	}
	if !ar.requestStop() {
		return ErrNotRunning
	}
	return nil
}

// Subscribe returns live events for an active run: first an EventStatus
// with the current Run, then EventMetric once per second and finally
// EventDone, after which the channel is closed. A subscriber that does not
// keep up loses metric/status events but always receives EventDone.
// cancel unsubscribes and closes the channel; it is idempotent. Events
// are shared between subscribers and must not be mutated.
func (m *Manager) Subscribe(runID string) (<-chan Event, func(), error) {
	ar := m.lookup(runID)
	if ar == nil {
		return nil, nil, ErrNotRunning
	}
	return ar.subscribe(m.subBuffer)
}

// Active lists the IDs of runs in progress, sorted.
func (m *Manager) Active() []string {
	m.mu.Lock()
	ars := make([]*activeRun, 0, len(m.runs))
	for _, ar := range m.runs {
		ars = append(ars, ar)
	}
	m.mu.Unlock()

	ids := make([]string, 0, len(ars))
	for _, ar := range ars {
		ar.mu.Lock()
		if !ar.finished {
			ids = append(ids, ar.run.ID)
		}
		ar.mu.Unlock()
	}
	sort.Strings(ids)
	return ids
}

// Workers reports the single in-process pseudo-worker. It is busy while
// any run is active; RunID is the most recently started active run.
func (m *Manager) Workers() []model.Worker {
	w := model.Worker{
		ID:       "local",
		Addr:     "in-process",
		State:    "idle",
		CPU:      math.Float64frombits(m.cpuBits.Load()),
		LastSeen: m.now(),
		Version:  m.version,
	}
	m.mu.Lock()
	var latest time.Time
	for id, ar := range m.runs {
		ar.mu.Lock()
		if !ar.finished && !ar.remote {
			w.State = "busy"
			if w.RunID == "" || ar.run.StartedAt.After(latest) {
				w.RunID, latest = id, ar.run.StartedAt
			}
		}
		ar.mu.Unlock()
	}
	m.mu.Unlock()
	out := []model.Worker{w}
	if m.dispatcher != nil {
		out = append(out, m.dispatcher.Workers()...)
	}
	return out
}

func baseName(p string) string { return filepath.Base(p) }

// targetURLs lists every URL a scenario sends load to: the base URL plus
// steps whose path is an absolute URL. (A path that is entirely a
// template, e.g. "{{url}}", is resolved per iteration and cannot be
// vetted here; see docs/security.md.)
func targetURLs(sc *scenario.Scenario) []string {
	urls := []string{sc.Target.BaseURL}
	for _, st := range sc.Steps {
		p := strings.ToLower(st.Path)
		for _, scheme := range []string{"http://", "https://", "ws://", "wss://"} {
			if strings.HasPrefix(p, scheme) {
				urls = append(urls, st.Path)
				break
			}
		}
	}
	return urls
}

// Shutdown rejects new runs, stops every active run gracefully (they end
// with status stopped and are persisted) and waits for them to finish or
// for ctx to be done, whichever comes first.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	ars := make([]*activeRun, 0, len(m.runs))
	for _, ar := range m.runs {
		ars = append(ars, ar)
	}
	m.mu.Unlock()

	for _, ar := range ars {
		ar.requestStop()
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *Manager) lookup(runID string) *activeRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[runID]
}

func (m *Manager) remove(runID string) {
	m.mu.Lock()
	delete(m.runs, runID)
	m.mu.Unlock()
}

func (m *Manager) setCPU(v float64) { m.cpuBits.Store(math.Float64bits(v)) }
