package draft

import (
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

type seg struct {
	weight int // relative share of the total duration
	target int
}

// stages builds closed-model stages (target = concurrent users; each stage
// ramps linearly from the previous target) that add up to exactly total.
func stages(goal Goal, users int, total time.Duration) []scenario.Stage {
	var segs []seg
	switch goal {
	case GoalLoad:
		// ramp up 20%, hold 70%, ramp down 10%
		segs = []seg{{20, users}, {70, users}, {10, 0}}
	case GoalStress:
		// n increasing steps (ramp + hold each) using 90%, then ramp down.
		n := 4
		if users < n {
			n = users
		}
		for i := 1; i <= n; i++ {
			t := (users*i + n - 1) / n
			segs = append(segs, seg{3, t}, seg{15, t})
		}
		segs = append(segs, seg{n * 2, 0})
	case GoalSpike:
		base := users / 10
		if base < 1 {
			base = 1
		}
		// settle at baseline, calm period, sudden jump, short hold, drop, recover
		segs = []seg{{5, base}, {30, base}, {2, users}, {20, users}, {2, base}, {41, base}}
	default: // smoke: short ramp, steady
		segs = []seg{{10, users}, {90, users}}
	}

	weights := make([]int, len(segs))
	for i, s := range segs {
		weights[i] = s.weight
	}
	durs := split(int(total/time.Second), weights)
	out := make([]scenario.Stage, len(segs))
	for i, s := range segs {
		out[i] = scenario.Stage{Duration: scenario.Duration{Duration: time.Duration(durs[i]) * time.Second}, Target: s.target}
	}
	return out
}

// split divides total whole seconds over weights, at least 1s each, summing
// to total exactly (total must be >= len(weights); callers guarantee >= 10
// and at most 9 segments for the minimum duration, which is checked below).
func split(total int, weights []int) []int {
	n := len(weights)
	if total < n {
		total = n
	}
	sum := 0
	for _, w := range weights {
		sum += w
	}
	out := make([]int, n)
	used, biggest := 0, 0
	for i, w := range weights {
		out[i] = total * w / sum
		if out[i] < 1 {
			out[i] = 1
		}
		used += out[i]
		if weights[i] > weights[biggest] {
			biggest = i
		}
	}
	// Put the rounding difference into the longest stage.
	out[biggest] += total - used
	if out[biggest] < 1 {
		// Too many minimum-length stages: take the excess from stages > 1s.
		deficit := 1 - out[biggest]
		out[biggest] = 1
		for i := 0; i < n && deficit > 0; i++ {
			for out[i] > 1 && deficit > 0 {
				out[i]--
				deficit--
			}
		}
	}
	return out
}
