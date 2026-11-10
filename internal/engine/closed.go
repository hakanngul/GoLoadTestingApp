package engine

import (
	"context"
	"math"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
)

// closedTick is how often the closed-model controller re-evaluates the
// desired VU count. Ramps are linear, so 50ms keeps the VU curve within one
// tick of the exact profile without measurable overhead.
const closedTick = 50 * time.Millisecond

// runClosed drives the closed (fixed virtual users) model.
//
// A controller (the caller) samples the profile every closedTick and sets
// the number of running VUs to round(target(t)). Each VU is a goroutine that
// loops: fetch a row, Iterate with intended = time.Now() (a closed model has
// no schedule to fall behind), repeat. Ramping down signals the
// highest-index VUs to stop; a signalled VU finishes its current iteration
// and exits. At the end of the profile, on ctx cancellation or on feeder
// failure, every VU is signalled. runClosed does not wait for VUs to exit
// (Run does, bounded by the grace period).
func (e *Engine) runClosed(ctx context.Context, r *runState, segs []segment, start time.Time) {
	var stops []chan struct{} // stops[i] signals VU i to exit
	setVUs := func(n int) {
		for len(stops) < n {
			stop := make(chan struct{})
			stops = append(stops, stop)
			r.wg.Add(1)
			e.vus.Add(1)
			go e.vu(ctx, r, stop)
		}
		for len(stops) > n {
			last := len(stops) - 1
			close(stops[last])
			stops = stops[:last]
		}
	}
	defer setVUs(0)

	end := start.Add(profileLength(segs))
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		now := time.Now()
		if !now.Before(end) {
			return
		}
		setVUs(int(math.Round(targetAt(segs, now.Sub(start)))))

		timer.Reset(min(closedTick, end.Sub(now)))
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
	}
}

// vu is one closed-model virtual user. It exits after its current iteration
// once stop is closed or ctx (the scheduling context) is done.
//
// The VU owns one protocol.Session (its cookie jar) for its whole life, so
// cookies set in one iteration are sent in the next, like a Locust user or
// a JMeter thread. A VU started later (ramp-up) starts with an empty jar.
func (e *Engine) vu(ctx context.Context, r *runState, stop <-chan struct{}) {
	defer r.wg.Done()
	defer e.vus.Add(-1)
	iterCtx := protocol.WithSession(r.iterCtx, protocol.NewSession())
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		default:
		}
		e.active.Add(1)
		ok := r.iterate(iterCtx, time.Now())
		e.active.Add(-1)
		if !ok {
			return
		}
	}
}
