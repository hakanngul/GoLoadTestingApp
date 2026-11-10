// Package runner executes tests on behalf of the API: it freezes the
// scenario, drives the engine (locally now, on workers from Phase 4),
// persists per-second metrics and fans live events out to subscribers.
//
// CONTRACT FILE (public API).
package runner

import (
	"context"
	"errors"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// ErrNotRunning is returned by Stop for runs that are not active.
var ErrNotRunning = errors.New("runner: run is not active")

// ErrTargetNotAllowed is returned when the scenario target is not on the
// allowlist (Phase 6).
var ErrTargetNotAllowed = errors.New("runner: target not in allowlist")

// EventType distinguishes live events.
type EventType string

const (
	EventMetric EventType = "metric" // Metric set
	EventStatus EventType = "status" // Run set (status/summary changed)
	EventDone   EventType = "done"   // Run set; channel closes after this
)

// Event is one live update for a run (sent as SSE `event: <Type>`,
// `data: <json of Metric or Run>`).
type Event struct {
	Type   EventType
	Metric *model.MetricPoint
	Run    *model.Run
}

// Runner is implemented by *Manager (manager.go).
type Runner interface {
	// Start validates the test's scenario, creates the run record and
	// starts it asynchronously. Returns the run in status running.
	Start(ctx context.Context, test model.Test, req model.StartRunRequest) (model.Run, error)
	// Stop requests a graceful stop; the run ends with status stopped.
	Stop(runID string) error
	// Subscribe returns live events for an active run. The channel is
	// closed after EventDone or when cancel is called. For runs that are
	// not active it returns ErrNotRunning (clients then read the store).
	Subscribe(runID string) (events <-chan Event, cancel func(), err error)
	// Active lists IDs of runs in progress.
	Active() []string
	// Workers lists known workers (local pseudo-worker until Phase 4).
	Workers() []model.Worker
}
