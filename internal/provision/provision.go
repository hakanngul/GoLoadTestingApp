// Package provision starts and stops the short-lived worker machines that
// generate load for a distributed run.
//
// The master calls Launch when a run that needs remote workers starts,
// waits for the workers to dial back over gRPC, and calls Terminate when the
// run finishes (or fails). List and the AWS-specific TerminateStale act as a
// safety net so that a crashed master never leaves paid instances running.
//
// Two implementations exist:
//
//   - Noop: no cloud provider configured; workers are started by hand.
//   - AWS:  EC2 spot (optionally on-demand) instances from a launch template.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Provisioner manages the lifecycle of worker machines for a run.
//
// Implementations must be safe for concurrent use.
type Provisioner interface {
	// Launch starts up to req.Count workers for req.RunID. It may succeed
	// partially: the returned slice always lists every instance that was
	// actually started (so the caller can terminate them), and the error,
	// if any, describes the shortfall (see *ShortfallError).
	Launch(ctx context.Context, req LaunchRequest) ([]Instance, error)

	// Terminate stops the given instances. Unknown or already terminated
	// IDs are not an error. An empty slice is a no-op.
	Terminate(ctx context.Context, ids []string) error

	// List returns the live (not yet terminated) workers of a run. An
	// empty runID lists every worker managed by this provisioner.
	List(ctx context.Context, runID string) ([]Instance, error)
}

// LaunchRequest describes the workers needed for one run.
type LaunchRequest struct {
	// RunID identifies the run; instances are tagged with it.
	RunID string
	// Count is the number of workers to start.
	Count int
	// MasterAddr is the host:port of the master's gRPC endpoint that the
	// workers dial (e.g. "10.0.1.15:9090").
	MasterAddr string
	// WorkerToken authenticates workers to the master. It is a secret:
	// implementations must never log it. LaunchRequest redacts it in
	// String, GoString and slog output.
	WorkerToken string
}

// String implements fmt.Stringer without exposing the token.
func (r LaunchRequest) String() string {
	return fmt.Sprintf("LaunchRequest{RunID:%q Count:%d MasterAddr:%q WorkerToken:%s}",
		r.RunID, r.Count, r.MasterAddr, redact(r.WorkerToken))
}

// GoString implements fmt.GoStringer (%#v) without exposing the token.
func (r LaunchRequest) GoString() string { return r.String() }

// LogValue implements slog.LogValuer without exposing the token.
func (r LaunchRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("run_id", r.RunID),
		slog.Int("count", r.Count),
		slog.String("master_addr", r.MasterAddr),
		slog.String("worker_token", redact(r.WorkerToken)),
	)
}

func redact(s string) string {
	if s == "" {
		return `""`
	}
	return "[REDACTED]"
}

// Validate reports whether the request is usable.
func (r LaunchRequest) Validate() error {
	switch {
	case r.RunID == "":
		return errors.New("provision: RunID is required")
	case r.Count < 1:
		return fmt.Errorf("provision: Count must be >= 1, got %d", r.Count)
	case r.MasterAddr == "":
		return errors.New("provision: MasterAddr is required")
	case r.WorkerToken == "":
		return errors.New("provision: WorkerToken is required")
	}
	return nil
}

// Instance is a worker machine as seen by the provisioner.
type Instance struct {
	ID        string
	PrivateIP string
	// State is the provider's lifecycle state, e.g. "pending", "running".
	State string
	// Spot is true for spot (interruptible) capacity.
	Spot bool
	// InstanceType is the machine type actually launched, e.g. "c7i.large".
	InstanceType string
	// LaunchedAt is when the provider started the instance (zero if unknown).
	LaunchedAt time.Time
}

// ErrNotConfigured is returned by Noop.Launch: no cloud provider is set up,
// so workers must be started manually with `goloadtest worker`.
var ErrNotConfigured = errors.New("provision: no cloud provisioner configured; start workers manually")

// ShortfallError reports a partially (or entirely) failed Launch.
type ShortfallError struct {
	Requested int
	Launched  int
	// Attempts holds the error of every failed launch attempt, in order.
	Attempts []error
}

func (e *ShortfallError) Error() string {
	msg := fmt.Sprintf("provision: launched %d of %d workers", e.Launched, e.Requested)
	if n := len(e.Attempts); n > 0 {
		msg += fmt.Sprintf(" (%d failed attempts, last: %v)", n, e.Attempts[n-1])
	}
	return msg
}

// Unwrap exposes the individual attempt errors to errors.Is / errors.As.
func (e *ShortfallError) Unwrap() []error { return e.Attempts }

// Noop is a Provisioner for setups without cloud integration. Listing and
// terminating are no-ops; launching any worker fails with ErrNotConfigured.
type Noop struct{}

var _ Provisioner = Noop{}

// Launch always returns ErrNotConfigured.
func (Noop) Launch(context.Context, LaunchRequest) ([]Instance, error) {
	return nil, ErrNotConfigured
}

// Terminate does nothing.
func (Noop) Terminate(context.Context, []string) error { return nil }

// List returns no instances.
func (Noop) List(context.Context, string) ([]Instance, error) { return nil, nil }
