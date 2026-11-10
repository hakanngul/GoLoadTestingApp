package engine

import (
	"context"
	"time"
)

// runOpen drives the open (arrival-rate) model.
//
// A single scheduler goroutine (the caller) walks the analytic arrival
// schedule (see schedule). For each arrival it computes the absolute
// intended start time, sleeps on one reusable timer until then, and
// dispatches. If the intended time has already passed (high rates, GC
// pauses, a slow machine) it dispatches immediately but keeps the original
// intended timestamp, so executors measure latency from the scheduled start
// (coordinated-omission correction) and the long-run rate stays exact.
//
// Concurrency cap: Load.MaxVUs is enforced with an atomic in-flight counter
// used as a non-blocking semaphore, plus one goroutine per admitted arrival.
// Spawning a goroutine costs on the order of a microsecond, keeps the
// scheduler's per-arrival work tiny, and makes the cap exact (unlike a
// worker pool fed by an unbuffered channel, where a worker that has just
// finished but not yet looped back to receive would cause a spurious drop),
// with no idle goroutines for large MaxVUs. If MaxVUs iterations are already
// in flight at an arrival's dispatch time the arrival is dropped:
// Options.OnDropped is called and the scheduler moves on without blocking.
//
// Sessions: every arrival is a new visitor, so no protocol.Session is
// attached; the executor then starts each iteration with an empty cookie
// jar (see protocol.Session).
//
// runOpen returns when the schedule is exhausted or ctx is done; it does not
// wait for in-flight iterations (Run does).
func (e *Engine) runOpen(ctx context.Context, r *runState, segs []segment, start time.Time) {
	maxVUs := int64(e.load.MaxVUs)
	if maxVUs <= 0 {
		maxVUs = defaultMaxVUs
	}
	sched := newSchedule(segs)

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		off, ok := sched.next()
		if !ok {
			// Hold until the profile's end so a trailing low-rate or pause
			// stage still lasts its full configured duration.
			off = profileLength(segs)
		}
		intended := start.Add(off)
		if d := time.Until(intended); d > 0 {
			timer.Reset(d)
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			}
		} else if ctx.Err() != nil {
			return
		}
		if !ok {
			return
		}

		if e.active.Add(1) > maxVUs {
			e.active.Add(-1)
			if f := e.opts.OnDropped; f != nil {
				f()
			}
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer e.active.Add(-1)
			r.iterate(r.iterCtx, intended)
		}()
	}
}
