// Package engine generates load according to a scenario's load profile.
//
// CONTRACT FILE (public API). Implementation lives in open.go / closed.go.
package engine

import (
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
}

// New creates an Engine. It does not start any goroutines.
func New(load scenario.Load, opts Options) *Engine {
	if opts.Feeder == nil {
		opts.Feeder = data.None{}
	}
	return &Engine{load: load, opts: opts}
}
