// Package cluster distributes runs across workers (Phase 4).
//
// Topology: every worker dials the master's gRPC Coordinator service and
// keeps one bidirectional stream (proto/cluster.proto). The master splits
// the load, tells each worker its index/count and a synchronized start
// time, then merges the per-second HDR snapshots exactly (never averaging
// percentiles).
//
// CONTRACT FILE (public API used by internal/runner and cmd/goloadtest).
package cluster

import (
	"context"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// RunSpec is what the master sends to workers for one run.
type RunSpec struct {
	RunID           string
	YAML            string            // full scenario; each worker scales its share
	Files           map[string][]byte // data files by name
	MaxErrorSamples int               // per worker
	StartAt         time.Time         // synchronized start (now + ~2s)
}

// Second is the merged result of one second across all workers of a run.
type Second struct {
	Index    int64             // 1-based second since StartAt
	Snapshot *metrics.Snapshot // merged interval snapshot
	Target   float64           // sum of worker targets
	Active   int64             // sum of in-flight iterations
	CPU      float64           // max worker CPU (0..1)
	Workers  int               // workers that reported this second
}

// Result is the final outcome of a distributed run.
type Result struct {
	Err           error    // first worker error (nil if all completed / stopped)
	DataExhausted bool     // any worker ran out of unique data
	LostWorkers   []string // workers that disconnected mid-run
}

// RemoteRun is an in-progress distributed run.
type RemoteRun interface {
	// Seconds delivers merged seconds in order. A second is emitted when
	// every live worker has reported it, or after a 2s grace for stragglers
	// (late reports for an emitted second are dropped and counted as a
	// warning via Result.LostWorkers if the worker is gone). Closed after
	// the last second, before Done fires.
	Seconds() <-chan Second
	// Errors delivers batches of error samples from workers. Closed with Seconds.
	Errors() <-chan []model.ErrorSample
	// Done is closed when all workers finished (or were lost); then Result is valid.
	Done() <-chan struct{}
	Result() Result
	// Stop asks all workers to stop gracefully. Idempotent.
	Stop()
}

// Dispatcher is the master side, implemented by *Master.
type Dispatcher interface {
	// Workers lists connected (and recently lost) workers.
	Workers() []model.Worker
	// Idle returns the number of connected workers with no active run.
	Idle() int
	// Start assigns n idle workers to the run (ErrNotEnoughWorkers if fewer
	// are idle) and returns once all of them acknowledged by heartbeat or
	// the context ends.
	Start(ctx context.Context, spec RunSpec, n int) (RemoteRun, error)
}
