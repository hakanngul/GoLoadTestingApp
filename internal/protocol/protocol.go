// Package protocol defines how one iteration of a scenario is executed
// against a target. HTTP lives in protocol/httpx; WebSocket comes in Phase 5.
//
// CONTRACT FILE.
package protocol

import (
	"context"
	"time"
)

// Result is the outcome of a single step (request).
type Result struct {
	Step    string        // Step.Name (or "METHOD path" if unnamed)
	Status  int           // 0 on transport error
	Latency time.Duration // see Executor.Iterate for the start point
	Bytes   int64         // response body bytes read
	Err     error         // transport, extract or assertion error; nil = success
}

// OK reports whether the step succeeded.
func (r Result) OK() bool { return r.Err == nil }

// Executor runs scenario iterations. Implementations must be safe for
// concurrent use by many goroutines.
type Executor interface {
	// Iterate runs every step of the scenario once, in order, for one
	// virtual user. vars holds data-row values; the executor may add
	// extracted variables to it (callers pass a fresh map per iteration).
	//
	// Coordinated omission: the FIRST step's latency is measured from
	// `intended` (the scheduled start), not from when the request was
	// actually sent. Later steps measure from their own start.
	//
	// sink is called once per executed step. If a step fails, the
	// iteration stops after reporting it. Iterate returns when done or
	// when ctx is cancelled.
	Iterate(ctx context.Context, vars map[string]string, intended time.Time, sink func(Result))
}
