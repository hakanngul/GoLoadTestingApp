// Package engine generates load according to a scenario's load profile.
//
// CONTRACT FILE (public API). Implementation lives in open.go / closed.go.
package engine

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// Options wires the engine to its collaborators.
type Options struct {
	Executor protocol.Executor
	Feeder   data.Feeder // nil => data.None{}
	// Sink receives every step result. Must be safe for concurrent use.
	Sink func(protocol.Result)
	// OnDropped is called when an open-model arrival is skipped because
	// MaxVUs in-flight iterations are already running. May be nil.
	OnDropped func()
}

// Engine runs one scenario's load profile on this node.
type Engine struct {
	load scenario.Load
	opts Options

	// gracePeriod bounds how long Run waits for in-flight iterations after
	// the profile ends before cancelling their context. Unexported so tests
	// can shorten it without widening the public Options.
	gracePeriod time.Duration

	segs []segment // resolved load.Stages

	active atomic.Int64 // in-flight iterations
	vus    atomic.Int64 // running closed-model VU goroutines

	mu      sync.Mutex
	started time.Time // zero until Run begins scheduling
}

// defaultGracePeriod is the default Engine.gracePeriod.
const defaultGracePeriod = 30 * time.Second

// New creates an Engine. It does not start any goroutines.
func New(load scenario.Load, opts Options) *Engine {
	if opts.Feeder == nil {
		opts.Feeder = data.None{}
	}
	return &Engine{load: load, opts: opts, gracePeriod: defaultGracePeriod, segs: segments(load.Stages)}
}
