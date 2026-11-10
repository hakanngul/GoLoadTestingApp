package engine

import (
	"context"
	"errors"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// fakeExec is a protocol.Executor that sleeps for a fixed time (or until
// ctx is done) and records what it was called with.
type fakeExec struct {
	sleep time.Duration

	mu       sync.Mutex
	intended []time.Time
	started  []time.Time

	inflight, maxInflight atomic.Int64
}

func (f *fakeExec) Iterate(ctx context.Context, vars map[string]string, intended time.Time, sink func(protocol.Result)) {
	now := time.Now()
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.intended = append(f.intended, intended)
	f.started = append(f.started, now)
	f.mu.Unlock()

	vars["extracted"] = "x" // executors may write to vars
	if f.sleep > 0 {
		t := time.NewTimer(f.sleep)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		}
	}
	sink(protocol.Result{Step: "fake", Status: 200, Latency: time.Since(intended)})
}

func (f *fakeExec) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.intended)
}

// blockExec blocks every iteration until its context is cancelled.
type blockExec struct{ calls atomic.Int64 }

func (b *blockExec) Iterate(ctx context.Context, _ map[string]string, _ time.Time, _ func(protocol.Result)) {
	b.calls.Add(1)
	<-ctx.Done()
}

// limitFeeder hands out n rows, then data.ErrExhausted.
type limitFeeder struct{ left atomic.Int64 }

func (l *limitFeeder) Next() (map[string]string, error) {
	if l.left.Add(-1) < 0 {
		return nil, data.ErrExhausted
	}
	return map[string]string{"id": "1"}, nil
}

func openLoad(maxVUs int, stages ...scenario.Stage) scenario.Load {
	return scenario.Load{Model: scenario.ModelOpen, MaxVUs: maxVUs, Stages: stages}
}

func TestOpenConstantRateAccuracy(t *testing.T) {
	const rate, dur = 500, 2 * time.Second
	exec := &fakeExec{sleep: time.Millisecond}
	var results atomic.Int64
	e := New(openLoad(0, stage(0, rate), stage(dur, rate)), Options{
		Executor: exec,
		Sink:     func(protocol.Result) { results.Add(1) },
	})

	begin := time.Now()
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(begin)

	expected := float64(rate) * dur.Seconds()
	got := exec.calls()
	if int64(got) != results.Load() {
		t.Errorf("sink saw %d results for %d iterations", results.Load(), got)
	}
	// Count iterations that actually started inside the profile window
	// (plus a little slack for the last one), i.e. the achieved rate.
	exec.mu.Lock()
	inWindow := 0
	for _, s := range exec.started {
		if s.Sub(begin) <= dur+5*time.Millisecond {
			inWindow++
		}
	}
	exec.mu.Unlock()
	acc := float64(inWindow)/expected - 1
	t.Logf("iterations=%d inWindow=%d expected=%.0f accuracy=%+.3f%% run=%v", got, inWindow, expected, acc*100, elapsed)
	if acc < -0.02 || acc > 0.02 {
		t.Errorf("achieved %d iterations in window, want %.0f ±2%%", inWindow, expected)
	}
	if float64(got) != expected {
		t.Errorf("total iterations %d, want exactly %.0f", got, expected)
	}
	if elapsed < dur || elapsed > dur+300*time.Millisecond {
		t.Errorf("Run took %v, want ≈%v", elapsed, dur)
	}
}

func TestOpenRamp(t *testing.T) {
	exec := &fakeExec{sleep: time.Millisecond}
	e := New(openLoad(0, stage(2*time.Second, 100)), Options{Executor: exec})
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := exec.calls(); got < 98 || got > 102 {
		t.Errorf("ramp 0->100 over 2s: %d iterations, want ≈100", got)
	}
	// Rate ramps up: the second half holds 3/4 of the arrivals.
	exec.mu.Lock()
	defer exec.mu.Unlock()
	// The first arrival is at sqrt(0.5/25)s; recover the profile start.
	start := slices.MinFunc(exec.intended, time.Time.Compare).Add(-time.Duration(math.Sqrt(0.02) * float64(time.Second)))
	late := 0
	for _, it := range exec.intended {
		if it.Sub(start) >= time.Second {
			late++
		}
	}
	if late < 70 || late > 80 {
		t.Errorf("%d arrivals in the second half, want ≈75", late)
	}
}

func TestOpenDropsAtMaxVUs(t *testing.T) {
	exec := &fakeExec{sleep: 100 * time.Millisecond}
	var dropped atomic.Int64
	e := New(openLoad(2, stage(0, 50), stage(time.Second, 50)), Options{
		Executor:  exec,
		OnDropped: func() { dropped.Add(1) },
	})
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ran, drop := int64(exec.calls()), dropped.Load()
	t.Logf("ran=%d dropped=%d max in-flight=%d", ran, drop, exec.maxInflight.Load())
	if ran+drop != 50 {
		t.Errorf("ran %d + dropped %d != 50 arrivals", ran, drop)
	}
	if m := exec.maxInflight.Load(); m > 2 {
		t.Errorf("max in-flight %d exceeds MaxVUs 2", m)
	}
	// 2 slots × 100ms ⇒ ≈20 admitted per second, ≈30 dropped.
	if drop < 20 || ran < 15 {
		t.Errorf("ran=%d dropped=%d, want ≈20/≈30", ran, drop)
	}
}

func TestOpenIntendedTimes(t *testing.T) {
	stages := []scenario.Stage{stage(500*time.Millisecond, 200), stage(500*time.Millisecond, 50)}
	exec := &fakeExec{}
	e := New(openLoad(0, stages...), Options{Executor: exec})
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sched := newSchedule(segments(stages))
	var want []time.Duration
	for off, ok := sched.next(); ok; off, ok = sched.next() {
		want = append(want, off)
	}

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.intended) != len(want) {
		t.Fatalf("got %d iterations, want %d", len(exec.intended), len(want))
	}
	// Iterations start concurrently, so sort before comparing.
	got := slices.Clone(exec.intended)
	slices.SortFunc(got, func(a, b time.Time) int { return a.Compare(b) })
	start := got[0].Add(-want[0])
	for i, it := range got {
		if d := it.Sub(start) - want[i]; d != 0 {
			t.Fatalf("arrival %d: intended offset %v, want %v", i, it.Sub(start), want[i])
		}
	}
	// Dispatch lag (actual start - intended) must stay small and positive.
	var lags []time.Duration
	for i := range exec.intended {
		lag := exec.started[i].Sub(exec.intended[i])
		if lag < 0 {
			t.Fatalf("iteration started %v before its intended time", -lag)
		}
		lags = append(lags, lag)
	}
	slices.Sort(lags)
	t.Logf("dispatch lag p50=%v p99=%v max=%v", lags[len(lags)/2], lags[len(lags)*99/100], lags[len(lags)-1])
	if p50 := lags[len(lags)/2]; p50 > 5*time.Millisecond {
		t.Errorf("median dispatch lag %v too large", p50)
	}
}

func TestOpenPreservesIntendedWhenBehind(t *testing.T) {
	// 20k/s on a single scheduler means it is often behind; every intended
	// time must still come from the schedule (no drift, no re-stamping).
	exec := &fakeExec{}
	stages := []scenario.Stage{stage(0, 20000), stage(200*time.Millisecond, 20000)}
	e := New(openLoad(0, stages...), Options{Executor: exec})
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if n := len(exec.intended); n != 4000 {
		t.Fatalf("got %d iterations, want 4000", n)
	}
	lo, hi := slices.MinFunc(exec.intended, time.Time.Compare), slices.MaxFunc(exec.intended, time.Time.Compare)
	if span, want := hi.Sub(lo), 199950*time.Microsecond; span != want {
		t.Errorf("intended span %v, want %v", span, want)
	}
}

func TestOpenCancel(t *testing.T) {
	exec := &blockExec{}
	e := New(openLoad(0, stage(0, 100), stage(time.Minute, 100)), Options{Executor: exec})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err := e.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want deadline exceeded", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("Run took %v after cancel", d)
	}
	if _, active := e.Current(); active != 0 {
		t.Errorf("%d iterations still active after Run", active)
	}
}

func TestOpenGracePeriod(t *testing.T) {
	exec := &blockExec{}
	e := New(openLoad(0, stage(0, 20), stage(200*time.Millisecond, 20)), Options{Executor: exec})
	e.gracePeriod = 100 * time.Millisecond
	begin := time.Now()
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d := time.Since(begin); d < 300*time.Millisecond || d > time.Second {
		t.Errorf("Run took %v, want ≈ profile (200ms) + grace (100ms)", d)
	}
	if n := exec.calls.Load(); n != 4 {
		t.Errorf("%d iterations, want 4", n)
	}
}

func TestOpenFeederExhausted(t *testing.T) {
	for _, model := range []scenario.LoadModel{scenario.ModelOpen, scenario.ModelClosed} {
		t.Run(string(model), func(t *testing.T) {
			feeder := &limitFeeder{}
			feeder.left.Store(10)
			exec := &fakeExec{sleep: 5 * time.Millisecond}
			load := scenario.Load{Model: model, Stages: []scenario.Stage{stage(0, 100), stage(time.Minute, 100)}}
			if model == scenario.ModelClosed {
				load.Stages = []scenario.Stage{stage(0, 3), stage(time.Minute, 3)}
			}
			e := New(load, Options{Executor: exec, Feeder: feeder})
			begin := time.Now()
			err := e.Run(context.Background())
			if !errors.Is(err, data.ErrExhausted) {
				t.Fatalf("Run = %v, want ErrExhausted", err)
			}
			if d := time.Since(begin); d > 2*time.Second {
				t.Errorf("Run took %v after exhaustion", d)
			}
			if got := exec.calls(); got != 10 {
				t.Errorf("%d iterations, want 10", got)
			}
		})
	}
}

func TestClosedFollowsProfile(t *testing.T) {
	stages := []scenario.Stage{
		stage(800*time.Millisecond, 4), // 0 -> 4
		stage(600*time.Millisecond, 4), // hold
		stage(400*time.Millisecond, 0), // 4 -> 0
	}
	exec := &fakeExec{sleep: 10 * time.Millisecond}
	e := New(scenario.Load{Model: scenario.ModelClosed, Stages: stages}, Options{Executor: exec})

	type sample struct {
		at     time.Duration
		target float64
		vus    int64
	}
	var samples []sample
	done := make(chan error, 1)
	begin := time.Now()
	go func() { done <- e.Run(context.Background()) }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			break loop
		case <-tick.C:
			target, _ := e.Current()
			samples = append(samples, sample{time.Since(begin), target, e.vus.Load()})
		}
	}

	// profile(t) is the VU target at offset t (piecewise linear).
	profile := func(t time.Duration) float64 {
		ms := float64(t) / float64(time.Millisecond)
		switch {
		case ms <= 0:
			return 0
		case ms < 800:
			return 4 * ms / 800
		case ms < 1400:
			return 4
		case ms < 1800:
			return 4 * (1 - (ms-1400)/400)
		default:
			return 0
		}
	}
	// The controller re-evaluates every 50ms and a VU told to stop first
	// finishes its current iteration, so the running count may trail the
	// profile. Accept any count the profile prescribed within the last lag;
	// on a loaded machine (e.g. go test ./... in parallel) scheduling adds
	// more, so the lag allowance is generous while still catching a
	// controller that does not follow the profile at all.
	const lag = 150 * time.Millisecond
	check := func(from, to time.Duration) {
		t.Helper()
		seen := 0
		for _, s := range samples {
			if s.at < from || s.at > to {
				continue
			}
			seen++
			lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
			for d := time.Duration(0); d <= lag; d += 5 * time.Millisecond {
				v := int64(math.Round(profile(s.at - d)))
				lo, hi = min(lo, v), max(hi, v)
			}
			if s.vus < lo || s.vus > hi {
				t.Errorf("at %v: %d VUs, want %d..%d (profile %.2f)", s.at, s.vus, lo, hi, profile(s.at))
			}
		}
		if seen == 0 {
			t.Errorf("no samples in [%v, %v]", from, to)
		}
	}
	check(100*time.Millisecond, 1750*time.Millisecond)
	// During the hold the count must be exact.
	for _, s := range samples {
		if s.at >= 1000*time.Millisecond && s.at <= 1350*time.Millisecond && s.vus != 4 {
			t.Errorf("hold at %v: %d VUs, want 4", s.at, s.vus)
		}
	}

	if v := e.vus.Load(); v != 0 {
		t.Errorf("%d VUs still running after Run", v)
	}
	if target, active := e.Current(); target != 0 || active != 0 {
		t.Errorf("Current after Run = (%v, %d), want (0, 0)", target, active)
	}
	if exec.maxInflight.Load() > 4 {
		t.Errorf("max in-flight %d > 4 VUs", exec.maxInflight.Load())
	}
}

func TestClosedCancel(t *testing.T) {
	exec := &fakeExec{sleep: 20 * time.Millisecond}
	e := New(scenario.Load{Model: scenario.ModelClosed, Stages: []scenario.Stage{stage(0, 50), stage(time.Minute, 50)}},
		Options{Executor: exec})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	begin := time.Now()
	if err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if d := time.Since(begin); d > 500*time.Millisecond {
		t.Errorf("Run took %v, want prompt stop after cancel", d)
	}
	if v := e.vus.Load(); v != 0 {
		t.Errorf("%d VUs still running", v)
	}
	if exec.calls() < 50 {
		t.Errorf("only %d iterations; expected all 50 VUs to run", exec.calls())
	}
}

func TestUnknownModel(t *testing.T) {
	e := New(scenario.Load{Model: "bogus", Stages: []scenario.Stage{stage(time.Second, 1)}}, Options{Executor: &fakeExec{}})
	if err := e.Run(context.Background()); err == nil {
		t.Fatal("Run with unknown model succeeded")
	}
}
