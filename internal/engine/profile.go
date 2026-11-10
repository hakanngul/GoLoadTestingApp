package engine

import (
	"math"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// segment is one stage of a load profile in resolved form: the target
// ramps linearly from `from` to `to` over [start, start+dur).
type segment struct {
	start time.Duration // offset of the segment from the profile start
	dur   time.Duration
	from  float64 // target at the segment start
	to    float64 // target at the segment end
}

// segments resolves stages into absolute segments. The first stage ramps
// from 0; every later stage ramps from the previous stage's target.
// Negative targets are clamped to 0. Zero-duration stages contribute no
// segment but still change the "previous target" (a step change).
func segments(stages []scenario.Stage) []segment {
	segs := make([]segment, 0, len(stages))
	var at time.Duration
	prev := 0.0
	for _, st := range stages {
		to := math.Max(0, float64(st.Target))
		if d := st.Duration.Duration; d > 0 {
			segs = append(segs, segment{start: at, dur: d, from: prev, to: to})
			at += d
		}
		prev = to
	}
	return segs
}

// profileLength returns the total duration of the resolved profile.
func profileLength(segs []segment) time.Duration {
	if len(segs) == 0 {
		return 0
	}
	last := segs[len(segs)-1]
	return last.start + last.dur
}

// targetAt returns the profile's target (rate or VU count) at offset t.
// Outside the profile it returns 0.
func targetAt(segs []segment, t time.Duration) float64 {
	for _, s := range segs {
		if t >= s.start && t < s.start+s.dur {
			frac := float64(t-s.start) / float64(s.dur)
			return s.from + (s.to-s.from)*frac
		}
	}
	return 0
}
