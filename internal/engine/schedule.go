package engine

import (
	"math"
	"time"
)

// schedule computes open-model arrival offsets analytically.
//
// The arrival rate r(t) is piecewise linear (see segments). Let
// N(t) = ∫₀ᵗ r(u) du be the expected number of arrivals by time t. The k-th
// arrival (k = 0, 1, 2, ...) is placed at the time t where N(t) = k + ½.
// Centering each arrival in its unit of "area" means a profile whose
// integral is A produces exactly round(A) arrivals and no arrival ever lands
// exactly on a profile boundary (avoiding floating-point off-by-one at the
// end of a run).
//
// Because every offset is derived from the closed-form integral rather than
// by accumulating inter-arrival gaps, there is no drift: arrival 1e6 is as
// accurate as arrival 1.
//
// Within a segment of length D ramping from r0 to r1, with τ the time since
// the segment start:
//
//	N(τ) = r0·τ + a·τ²,  a = (r1 - r0) / (2D)
//
// Solving a·τ² + r0·τ - m = 0 for the remaining area m > 0 gives the
// numerically stable root
//
//	τ = 2m / (r0 + √(r0² + 4am))
//
// which also covers the constant-rate case (a = 0 ⇒ τ = m/r0) and ramps
// down (a < 0). Segments with zero area (a 0→0 pause) yield no arrivals.
//
// A schedule is not safe for concurrent use.
type schedule struct {
	segs []segment
	cum  []float64 // cum[i] = N at segs[i].start
	area []float64 // area[i] = arrivals contributed by segs[i]
	idx  int       // current segment
	k    int64     // index of the next arrival
}

func newSchedule(segs []segment) *schedule {
	s := &schedule{segs: segs, cum: make([]float64, len(segs)), area: make([]float64, len(segs))}
	total := 0.0
	for i, sg := range segs {
		s.cum[i] = total
		s.area[i] = (sg.from + sg.to) / 2 * sg.dur.Seconds()
		total += s.area[i]
	}
	return s
}

// total returns the integral of the rate over the whole profile.
func (s *schedule) total() float64 {
	if len(s.segs) == 0 {
		return 0
	}
	n := len(s.segs) - 1
	return s.cum[n] + s.area[n]
}

// next returns the offset from the profile start of the next arrival, or
// ok=false once the profile is exhausted. Offsets are non-decreasing.
func (s *schedule) next() (offset time.Duration, ok bool) {
	want := float64(s.k) + 0.5
	for s.idx < len(s.segs) && s.cum[s.idx]+s.area[s.idx] < want {
		s.idx++
	}
	if s.idx == len(s.segs) {
		return 0, false
	}
	sg := s.segs[s.idx]
	m := want - s.cum[s.idx] // remaining area inside this segment, > 0
	r0 := sg.from
	a := (sg.to - sg.from) / (2 * sg.dur.Seconds())
	disc := math.Max(0, r0*r0+4*a*m) // clamp rounding noise near r1 == 0
	tau := 2 * m / (r0 + math.Sqrt(disc))
	if tau > sg.dur.Seconds() { // guard against rounding at the segment end
		tau = sg.dur.Seconds()
	}
	s.k++
	return sg.start + time.Duration(math.Round(tau*float64(time.Second))), true
}
