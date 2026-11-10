package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// defaultMaxVUs is used when Load.MaxVUs is not set (see scenario.Load).
const defaultMaxVUs = 1000

// Run executes the whole load profile and blocks until it is finished and
// every in-flight iteration has returned.
//
// It returns:
//   - nil when the profile completes;
//   - ctx.Err() when ctx is cancelled first;
//   - an error wrapping the feeder's error (errors.Is(err, data.ErrExhausted)
//     in unique mode) when the feeder fails. Scheduling stops immediately and
//     in-flight iterations are allowed to finish.
//
// In-flight iterations run with a context derived from ctx. Once the
// profile ends (or the feeder fails) they get a grace period (30s by
// default) to finish, after which their context is cancelled. Run must be
// called at most once per Engine.
func (e *Engine) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	segs := e.segs

	r := &runState{e: e}
	// iterCtx is what executors see: it follows ctx, and is additionally
	// cancelled when the grace period after the profile expires.
	iterCtx, cancelIter := context.WithCancel(ctx)
	defer cancelIter()
	// schedCtx stops generating new work (arrivals or VU iterations).
	schedCtx, stopSched := context.WithCancel(ctx)
	defer stopSched()
	r.iterCtx, r.stop = iterCtx, stopSched

	e.mu.Lock()
	e.started = time.Now()
	start := e.started
	e.mu.Unlock()

	switch e.load.Model {
	case scenario.ModelOpen, "":
		e.runOpen(schedCtx, r, segs, start)
	case scenario.ModelClosed:
		e.runClosed(schedCtx, r, segs, start)
	default:
		return fmt.Errorf("engine: unknown load model %q", e.load.Model)
	}

	// Scheduling is over: bound the wait for in-flight iterations.
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	grace := time.NewTimer(e.gracePeriod)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		cancelIter()
		<-done
	}

	if err := r.err(); err != nil {
		return err
	}
	return ctx.Err()
}

// Current reports live values for display: the profile's current target
// (arrival rate in iterations/s for the open model, VU count for the closed
// model; 0 before Run starts and after the profile ends) and the number of
// iterations in flight right now. Safe for concurrent use.
func (e *Engine) Current() (target float64, active int64) {
	e.mu.Lock()
	start := e.started
	e.mu.Unlock()
	if !start.IsZero() {
		target = targetAt(e.segs, time.Since(start))
	}
	return target, e.active.Load()
}

// runState is the per-Run shared state used by the open and closed models.
type runState struct {
	e       *Engine
	iterCtx context.Context
	stop    context.CancelFunc // stops scheduling
	wg      sync.WaitGroup     // in-flight iterations / VU goroutines

	mu      sync.Mutex
	feedErr error
}

// err returns the first feeder error, if any.
func (r *runState) err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.feedErr
}

// iterate runs a single iteration: fetch a fresh row, then call the
// executor. It reports false if the feeder failed, in which case the error
// is recorded and scheduling is stopped.
//
// ctx is r.iterCtx, or a context derived from it that carries the virtual
// user's protocol.Session (closed model).
func (r *runState) iterate(ctx context.Context, intended time.Time) bool {
	row, err := r.e.opts.Feeder.Next()
	if err != nil {
		r.mu.Lock()
		if r.feedErr == nil {
			r.feedErr = fmt.Errorf("engine: data feeder: %w", err)
		}
		r.mu.Unlock()
		r.stop()
		return false
	}
	// The executor may add extracted variables to the map, so always hand
	// it a private copy in case the feeder reuses its row maps.
	vars := make(map[string]string, len(row)+4)
	for k, v := range row {
		vars[k] = v
	}
	r.e.opts.Executor.Iterate(ctx, vars, intended, r.sink)
	return true
}

// sink forwards results to Options.Sink, tolerating a nil Sink.
func (r *runState) sink(res protocol.Result) {
	if s := r.e.opts.Sink; s != nil {
		s(res)
	}
}
