// Package report aggregates stored runs for reporting: exact window
// summaries (merged HDR histograms), run-vs-run comparisons, per-test
// trends and the overview dashboard.
//
// Window semantics (ResolveWindow):
//
//   - full: [0, D) where D is the run duration (the Elapsed of the last
//     stored metric point; FinishedAt-StartedAt when there are none).
//   - steady: the scenario stages minus the initial ramp-up and a final
//     ramp-down. Stage 0 always starts from 0, so it is skipped entirely
//     when its target is > 0; the last stage is skipped when its target is
//     lower than the previous stage's (there must be at least two stages).
//     The result is clamped to [0, D] (a stopped run is shorter than its
//     scenario). If nothing is left, the window falls back to full and its
//     Kind says "full".
//   - custom: [from, to) seconds since start, clamped to [0, D]; to must be
//     greater than from. A window entirely past the end is empty.
//
// Point selection: point i covers (start_i, Elapsed_i] with
// start_i = max(Elapsed_{i-1}, Elapsed_i - 1) (so a lost second does not
// stretch the next point) and is included when its midpoint lies in
// [From, To). Rates (RPS) are computed over the window length To-From.
package report

import (
	"errors"
	"fmt"
	"math"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// ErrBadWindow is wrapped by window and tolerance validation errors (the
// API answers 400).
var ErrBadWindow = errors.New("report: invalid window")

// DefaultTolerance is the relative tolerance used by comparisons.
const DefaultTolerance = 0.10

// WindowSpec is a requested (unresolved) window.
type WindowSpec struct {
	Kind     model.WindowKind
	From, To float64 // custom only, seconds since start
}

// Steady is the default window request.
var Steady = WindowSpec{Kind: model.WindowSteady}

// ResolveWindow turns a window request into concrete bounds for a run of
// runDuration seconds whose scenario had stages (see the package comment).
func ResolveWindow(kind model.WindowKind, from, to float64, stages []scenario.Stage, runDuration float64) (model.Window, error) {
	d := runDuration
	if math.IsNaN(d) || d < 0 {
		d = 0
	}
	full := model.Window{Kind: model.WindowFull, From: 0, To: d}
	switch kind {
	case model.WindowFull:
		return full, nil
	case model.WindowSteady:
		if len(stages) == 0 {
			return full, nil
		}
		var total float64
		for _, st := range stages {
			total += st.Duration.Seconds()
		}
		lo, hi := 0.0, total
		if stages[0].Target > 0 {
			lo = stages[0].Duration.Seconds()
		}
		if n := len(stages); n >= 2 && stages[n-1].Target < stages[n-2].Target {
			hi = total - stages[n-1].Duration.Seconds()
		}
		hi = math.Min(hi, d)
		if lo >= hi {
			return full, nil
		}
		return model.Window{Kind: model.WindowSteady, From: lo, To: hi}, nil
	case model.WindowCustom:
		if math.IsNaN(from) || math.IsNaN(to) || math.IsInf(from, 0) || math.IsInf(to, 0) {
			return model.Window{}, fmt.Errorf("%w: from and to must be finite numbers", ErrBadWindow)
		}
		if to <= from {
			return model.Window{}, fmt.Errorf("%w: to must be greater than from", ErrBadWindow)
		}
		lo := math.Min(math.Max(from, 0), d)
		hi := math.Min(math.Max(to, 0), d)
		return model.Window{Kind: model.WindowCustom, From: lo, To: hi}, nil
	}
	return model.Window{}, fmt.Errorf("%w: unknown window %q (want full, steady or custom)", ErrBadWindow, kind)
}

// pointInWindow reports which points fall inside w (see package comment).
func pointsInWindow(pts []model.MetricPoint, w model.Window) []bool {
	in := make([]bool, len(pts))
	prev := 0.0
	for i, p := range pts {
		start := math.Max(prev, p.Elapsed-1)
		mid := (start + p.Elapsed) / 2
		prev = p.Elapsed
		in[i] = mid >= w.From && mid < w.To
	}
	return in
}
