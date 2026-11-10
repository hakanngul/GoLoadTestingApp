package engine

import (
	"math"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func stage(d time.Duration, target int) scenario.Stage {
	return scenario.Stage{Duration: scenario.Duration{Duration: d}, Target: target}
}

// integral is a reference implementation of N(t) = ∫₀ᵗ r(u) du, written
// independently of schedule (trapezoid over each linear piece).
func integral(segs []segment, t time.Duration) float64 {
	sum := 0.0
	for _, s := range segs {
		if t <= s.start {
			break
		}
		end := min(t, s.start+s.dur)
		span := (end - s.start).Seconds()
		rEnd := s.from + (s.to-s.from)*span/s.dur.Seconds()
		sum += (s.from + rEnd) / 2 * span
	}
	return sum
}

// allArrivals drains a schedule; it fails the test if offsets go backwards.
func allArrivals(t *testing.T, stages []scenario.Stage) ([]time.Duration, []segment) {
	t.Helper()
	segs := segments(stages)
	s := newSchedule(segs)
	var out []time.Duration
	for {
		off, ok := s.next()
		if !ok {
			break
		}
		if n := len(out); n > 0 && off < out[n-1] {
			t.Fatalf("arrival %d at %v before previous %v", n, off, out[n-1])
		}
		if off < 0 || off > profileLength(segs) {
			t.Fatalf("arrival %d at %v outside profile [0, %v]", len(out), off, profileLength(segs))
		}
		out = append(out, off)
		if len(out) > 1e7 {
			t.Fatal("runaway schedule")
		}
	}
	return out, segs
}

// checkExact asserts arrival k lies where the integral reaches k+½ and the
// total count equals round(∫ rate).
func checkExact(t *testing.T, offs []time.Duration, segs []segment) {
	t.Helper()
	total := integral(segs, profileLength(segs))
	if want := int(math.Floor(total + 0.5)); len(offs) != want {
		t.Fatalf("got %d arrivals, want %d (integral %.4f)", len(offs), want, total)
	}
	for k, off := range offs {
		if got := integral(segs, off); math.Abs(got-(float64(k)+0.5)) > 1e-5 { // 1ns rounding at <=1000/s
			t.Fatalf("arrival %d at %v: integral %.9f, want %.1f", k, off, got, float64(k)+0.5)
		}
	}
}

func TestScheduleConstantRate(t *testing.T) {
	// A 0s stage steps the rate straight to 10/s.
	offs, segs := allArrivals(t, []scenario.Stage{stage(0, 10), stage(time.Second, 10)})
	if len(offs) != 10 {
		t.Fatalf("got %d arrivals, want 10", len(offs))
	}
	for k, off := range offs {
		want := time.Duration((float64(k) + 0.5) * float64(100*time.Millisecond))
		if d := off - want; d < -time.Microsecond || d > time.Microsecond {
			t.Errorf("arrival %d at %v, want %v", k, off, want)
		}
	}
	checkExact(t, offs, segs)
}

func TestScheduleRampUp(t *testing.T) {
	// 0 -> 100/s over 2s: N(t) = 25t², 100 arrivals at sqrt((k+½)/25).
	offs, segs := allArrivals(t, []scenario.Stage{stage(2*time.Second, 100)})
	if len(offs) != 100 {
		t.Fatalf("got %d arrivals, want 100", len(offs))
	}
	for k, off := range offs {
		want := math.Sqrt((float64(k) + 0.5) / 25)
		if math.Abs(off.Seconds()-want) > 1e-6 {
			t.Errorf("arrival %d at %.6fs, want %.6fs", k, off.Seconds(), want)
		}
	}
	checkExact(t, offs, segs)
}

func TestScheduleRampDown(t *testing.T) {
	// 0 -> 100 over 1s (50 arrivals) then 100 -> 0 over 2s (100 arrivals).
	offs, segs := allArrivals(t, []scenario.Stage{stage(time.Second, 100), stage(2*time.Second, 0)})
	checkExact(t, offs, segs)
	if len(offs) != 150 {
		t.Fatalf("got %d arrivals, want 150", len(offs))
	}
	// Gaps grow while ramping down.
	n := len(offs)
	if g1, g2 := offs[60]-offs[59], offs[n-1]-offs[n-2]; g2 <= g1 {
		t.Errorf("gaps should widen during ramp down: early %v, last %v", g1, g2)
	}
}

func TestScheduleZeroStages(t *testing.T) {
	stages := []scenario.Stage{
		stage(time.Second, 10),         // 0->10   area 5
		stage(time.Second, 10),         // hold    area 10
		stage(500*time.Millisecond, 0), // 10->0   area 2.5
		stage(time.Second, 0),          // pause   area 0  [2.5s, 3.5s)
		stage(time.Second, 20),         // 0->20   area 10
	}
	offs, segs := allArrivals(t, stages)
	checkExact(t, offs, segs) // 27.5 -> 28 arrivals
	for _, off := range offs {
		if off > 2500*time.Millisecond && off < 3500*time.Millisecond {
			t.Errorf("arrival at %v during the pause", off)
		}
	}

	// Only zero stages: nothing to schedule.
	if offs, _ := allArrivals(t, []scenario.Stage{stage(time.Second, 0)}); len(offs) != 0 {
		t.Errorf("0->0 profile produced %d arrivals", len(offs))
	}
}

func TestScheduleMultiStageCumulative(t *testing.T) {
	stages := []scenario.Stage{
		stage(3*time.Second, 1000),
		stage(7*time.Second, 1000),
		stage(1500*time.Millisecond, 37),
		stage(2*time.Second, 400),
		stage(time.Second, 0),
	}
	offs, segs := allArrivals(t, stages)
	checkExact(t, offs, segs)
}

func TestTargetAt(t *testing.T) {
	segs := segments([]scenario.Stage{stage(2*time.Second, 100), stage(time.Second, 100), stage(time.Second, 0)})
	cases := []struct {
		at   time.Duration
		want float64
	}{
		{0, 0}, {time.Second, 50}, {2500 * time.Millisecond, 100},
		{3500 * time.Millisecond, 50}, {4 * time.Second, 0}, {time.Hour, 0},
	}
	for _, c := range cases {
		if got := targetAt(segs, c.at); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("targetAt(%v) = %v, want %v", c.at, got, c.want)
		}
	}
	if got := profileLength(segs); got != 4*time.Second {
		t.Errorf("profileLength = %v, want 4s", got)
	}
}
