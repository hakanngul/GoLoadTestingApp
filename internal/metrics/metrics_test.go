package metrics

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func res(step string, lat time.Duration, failed bool) protocol.Result {
	r := protocol.Result{Step: step, Status: 200, Latency: lat, Bytes: 100}
	if failed {
		r.Err = errors.New("boom")
		r.Status = 500
	}
	return r
}

func TestRecordAndFlushBasics(t *testing.T) {
	c := NewCollector(t0)
	c.Record(res("a", 10*time.Millisecond, false))
	c.Record(res("a", 20*time.Millisecond, true))
	c.Record(res("b", 30*time.Millisecond, false))
	c.RecordDropped()

	s := c.Flush(t0.Add(time.Second))
	if !s.Start.Equal(t0) || !s.End.Equal(t0.Add(time.Second)) {
		t.Fatalf("window = [%v, %v)", s.Start, s.End)
	}
	if s.Total.Requests != 3 || s.Total.Errors != 1 || s.Total.Bytes != 300 || s.Dropped != 1 {
		t.Fatalf("total = %+v dropped=%d", s.Total, s.Dropped)
	}
	if s.Total.Hist.TotalCount() != 3 {
		t.Fatalf("hist count = %d", s.Total.Hist.TotalCount())
	}
	if a := s.ByStep["a"]; a == nil || a.Requests != 2 || a.Errors != 1 {
		t.Fatalf("step a = %+v", a)
	}
	if b := s.ByStep["b"]; b == nil || b.Requests != 1 {
		t.Fatalf("step b = %+v", b)
	}

	// Second interval is empty and starts where the first ended.
	s2 := c.Flush(t0.Add(2 * time.Second))
	if s2.Total.Requests != 0 || s2.Dropped != 0 || len(s2.ByStep) != 0 {
		t.Fatalf("second interval not empty: %+v", s2)
	}
	if !s2.Start.Equal(t0.Add(time.Second)) {
		t.Fatalf("second start = %v", s2.Start)
	}
}

func TestFlushCumulativeSemantics(t *testing.T) {
	c := NewCollector(t0)
	for i := 0; i < 10; i++ {
		c.Record(res("a", time.Millisecond, false))
	}
	c.Flush(t0.Add(time.Second))
	for i := 0; i < 5; i++ {
		c.Record(res("a", 2*time.Millisecond, i == 0))
	}
	c.RecordDropped()

	// Cumulative includes pending without resetting it.
	cum := c.Cumulative()
	if cum.Total.Requests != 15 || cum.Total.Errors != 1 || cum.Dropped != 1 {
		t.Fatalf("cumulative = %+v dropped=%d", cum.Total, cum.Dropped)
	}
	if !cum.Start.Equal(t0) || cum.End.Before(t0.Add(time.Second)) {
		t.Fatalf("cumulative window = [%v, %v)", cum.Start, cum.End)
	}
	cum2 := c.Cumulative()
	if cum2.Total.Requests != 15 {
		t.Fatalf("Cumulative reset pending: %d", cum2.Total.Requests)
	}

	// Flush still sees the pending data.
	iv := c.Flush(t0.Add(2 * time.Second))
	if iv.Total.Requests != 5 || iv.Dropped != 1 || iv.Total.Errors != 1 {
		t.Fatalf("interval = %+v dropped=%d", iv.Total, iv.Dropped)
	}
	cum3 := c.Cumulative()
	if cum3.Total.Requests != 15 || cum3.Dropped != 1 || cum3.ByStep["a"].Requests != 15 {
		t.Fatalf("cumulative after flush = %+v", cum3.Total)
	}
	// Nothing pending: End is exactly the last flush.
	if !cum3.End.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("cumulative end = %v", cum3.End)
	}

	// Returned snapshots do not alias internal state.
	cum3.Total.Hist.Reset()
	cum3.ByStep["a"].Hist.Reset()
	iv.Total.Hist.Reset()
	if got := c.Cumulative().Total.Hist.TotalCount(); got != 15 {
		t.Fatalf("internal hist aliased, count = %d", got)
	}
	if got := c.Cumulative().ByStep["a"].Hist.TotalCount(); got != 15 {
		t.Fatalf("internal step hist aliased, count = %d", got)
	}
}

func TestClampAndZeroLatency(t *testing.T) {
	c := NewCollector(t0)
	c.Record(res("a", 0, false))
	c.Record(res("a", -time.Second, false))
	c.Record(res("a", 5*time.Minute, false))
	c.Record(res("a", time.Hour, false))
	s := c.Flush(t0.Add(time.Second))
	h := s.Total.Hist
	if h.TotalCount() != 4 || s.Total.Requests != 4 {
		t.Fatalf("clamped values dropped: hist=%d req=%d", h.TotalCount(), s.Total.Requests)
	}
	if h.Min() != 1 {
		t.Fatalf("min = %d, want 1us", h.Min())
	}
	if !h.ValuesAreEquivalent(h.Max(), HighestLatencyMicros) {
		t.Fatalf("max = %d, want ~%d", h.Max(), HighestLatencyMicros)
	}
	sum := Summarize(s)
	if sum.Max < 59*time.Second || sum.Max > 61*time.Second {
		t.Fatalf("summary max = %v", sum.Max)
	}
}

// Two collectors fed disjoint halves, merged, must equal one collector fed
// the union.
func TestMergeEqualsUnion(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	a, b, all := NewCollector(t0), NewCollector(t0.Add(time.Second)), NewCollector(t0)
	for i := 0; i < 20000; i++ {
		// Heavy-tailed latencies so p99 is meaningful.
		lat := time.Duration(rng.ExpFloat64()*float64(5*time.Millisecond)) + time.Millisecond
		step := fmt.Sprintf("s%d", i%3)
		r := res(step, lat, i%17 == 0)
		all.Record(r)
		if i%2 == 0 {
			a.Record(r)
		} else {
			b.Record(r)
		}
	}
	a.RecordDropped()
	b.RecordDropped()
	b.RecordDropped()

	sa := a.Flush(t0.Add(3 * time.Second))
	sb := b.Flush(t0.Add(4 * time.Second))
	su := all.Flush(t0.Add(4 * time.Second))

	merged := sa.Clone()
	merged.Merge(sb)

	if !merged.Start.Equal(t0) || !merged.End.Equal(t0.Add(4*time.Second)) {
		t.Fatalf("merged window = [%v, %v)", merged.Start, merged.End)
	}
	if merged.Dropped != 3 {
		t.Fatalf("dropped = %d", merged.Dropped)
	}
	if !merged.Total.Hist.Equals(su.Total.Hist) {
		t.Fatal("merged total histogram differs from union")
	}
	ms, us := Summarize(merged), Summarize(su)
	if ms.P99 != us.P99 || ms.P50 != us.P50 || ms.Max != us.Max || ms.Requests != us.Requests || ms.Errors != us.Errors {
		t.Fatalf("merged summary %+v != union %+v", ms, us)
	}
	for name, st := range su.ByStep {
		m := merged.ByStep[name]
		if m == nil || m.Requests != st.Requests || !m.Hist.Equals(st.Hist) {
			t.Fatalf("step %s differs", name)
		}
		if ms.ByStep[name].P99 != us.ByStep[name].P99 {
			t.Fatalf("step %s p99 differs", name)
		}
	}

	// Merge must not alias the source.
	sb.Total.Hist.Reset()
	if merged.Total.Hist.TotalCount() != 20000 {
		t.Fatal("merged snapshot aliases source histogram")
	}
}

func TestSnapshotMergeIntoEmpty(t *testing.T) {
	var s Snapshot
	c := NewCollector(t0)
	c.Record(res("x", time.Millisecond, false))
	s.Merge(c.Flush(t0.Add(time.Second)))
	s.Merge(nil)
	if s.Total.Requests != 1 || s.ByStep["x"].Requests != 1 || !s.Start.Equal(t0) {
		t.Fatalf("merge into zero snapshot: %+v", s)
	}
}

func TestConcurrentRecordFlush(t *testing.T) {
	c := NewCollector(t0)
	const workers, per = 16, 5000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				c.Record(res(fmt.Sprintf("s%d", i%4), time.Duration(i%1000)*time.Microsecond, i%10 == 0))
				if i%100 == 0 {
					c.RecordDropped()
				}
			}
		}(w)
	}
	var total, dropped int64
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	tick := t0
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
		}
		tick = tick.Add(time.Second)
		s := c.Flush(tick)
		total += s.Total.Requests
		dropped += s.Dropped
		_ = c.Cumulative()
	}
	s := c.Flush(tick.Add(time.Second))
	total += s.Total.Requests
	dropped += s.Dropped

	if total != workers*per {
		t.Fatalf("interval total = %d, want %d", total, workers*per)
	}
	if dropped != workers*per/100 {
		t.Fatalf("dropped = %d", dropped)
	}
	cum := c.Cumulative()
	if cum.Total.Requests != workers*per || cum.Total.Hist.TotalCount() != workers*per {
		t.Fatalf("cumulative = %d / %d", cum.Total.Requests, cum.Total.Hist.TotalCount())
	}
	if cum.Total.Errors != workers*per/10 {
		t.Fatalf("errors = %d", cum.Total.Errors)
	}
	var steps int64
	for _, st := range cum.ByStep {
		steps += st.Requests
	}
	if steps != cum.Total.Requests {
		t.Fatalf("sum of steps %d != total %d", steps, cum.Total.Requests)
	}
}

func TestSummarize(t *testing.T) {
	c := NewCollector(t0)
	for i := 1; i <= 100; i++ {
		c.Record(res("a", time.Duration(i)*time.Millisecond, i > 95))
	}
	s := c.Flush(t0.Add(10 * time.Second))
	sum := Summarize(s)
	if sum.Requests != 100 || sum.Errors != 5 || sum.ErrorRate != 0.05 {
		t.Fatalf("counts: %+v", sum)
	}
	if sum.RPS != 10 {
		t.Fatalf("rps = %v", sum.RPS)
	}
	near := func(name string, got, want time.Duration) {
		t.Helper()
		if d := got - want; d < -want/100 || d > want/100 {
			t.Errorf("%s = %v, want ~%v", name, got, want)
		}
	}
	near("p50", sum.P50, 50*time.Millisecond)
	near("p90", sum.P90, 90*time.Millisecond)
	near("p95", sum.P95, 95*time.Millisecond)
	near("p99", sum.P99, 99*time.Millisecond)
	near("max", sum.Max, 100*time.Millisecond)
	near("mean", sum.Mean, 50500*time.Microsecond)
	if a, ok := sum.ByStep["a"]; !ok || a.Requests != 100 || a.ByStep != nil || a.RPS != 10 {
		t.Fatalf("by step: %+v", a)
	}

	// Zero duration and empty snapshot guards.
	z := Summarize(&Snapshot{Start: t0, End: t0, Total: NewStats()})
	if z.RPS != 0 || z.ErrorRate != 0 || z.P99 != 0 {
		t.Fatalf("empty summary: %+v", z)
	}
	if got := Summarize(nil); got.Requests != 0 {
		t.Fatalf("nil summary: %+v", got)
	}
}

func TestEvaluateThresholds(t *testing.T) {
	sum := Summary{
		Requests: 1000, Errors: 10, ErrorRate: 0.01, RPS: 250,
		Mean: 40 * time.Millisecond, P50: 30 * time.Millisecond, P90: 80 * time.Millisecond,
		P95: 120 * time.Millisecond, P99: 250500 * time.Microsecond, Max: 2 * time.Second,
	}
	cases := []struct {
		metric, op string
		value      float64
		actual     float64
		pass       bool
	}{
		{"latency.p50", "lt", 50, 30, true},
		{"latency.p90", "lte", 80, 80, true},
		{"latency.p95", "lt", 120, 120, false},
		{"latency.p99", "lt", 250, 250.5, false},
		{"latency.p99", "gt", 250, 250.5, true},
		{"latency.mean", "gte", 40, 40, true},
		{"latency.max", "lt", 1000, 2000, false},
		{"error_rate", "lt", 0.02, 0.01, true},
		{"error_rate", "lte", 0.005, 0.01, false},
		{"rps", "gte", 200, 250, true},
		{"rps", "gt", 250, 250, false},
	}
	for _, tc := range cases {
		t.Run(tc.metric+"_"+tc.op, func(t *testing.T) {
			th := scenario.Threshold{Metric: tc.metric, Op: tc.op, Value: tc.value}
			got, err := EvaluateThresholds(sum, []scenario.Threshold{th})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Actual != tc.actual || got[0].Passed != tc.pass || got[0].Threshold != th {
				t.Fatalf("got %+v, want actual=%v pass=%v", got, tc.actual, tc.pass)
			}
		})
	}

	for _, bad := range []scenario.Threshold{
		{Metric: "latency.p42", Op: "lt", Value: 1},
		{Metric: "rps", Op: "eq", Value: 1},
		{Metric: "", Op: "lt", Value: 1},
	} {
		if _, err := EvaluateThresholds(sum, []scenario.Threshold{bad}); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
	if got, err := EvaluateThresholds(sum, nil); err != nil || len(got) != 0 {
		t.Fatalf("nil thresholds: %v %v", got, err)
	}
}

func TestBinaryRoundTrip(t *testing.T) {
	c := NewCollector(t0)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		c.Record(res(fmt.Sprintf("s%d", i%2), time.Duration(rng.Intn(500000))*time.Microsecond, i%9 == 0))
	}
	c.Record(res("slow", time.Hour, false))
	c.RecordDropped()
	orig := c.Flush(t0.Add(time.Second))

	data, err := orig.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(orig.Start) || !got.End.Equal(orig.End) || got.Dropped != orig.Dropped {
		t.Fatalf("header mismatch: %+v", got)
	}
	eq := func(name string, a, b *Stats) {
		t.Helper()
		if a.Requests != b.Requests || a.Errors != b.Errors || a.Bytes != b.Bytes || !a.Hist.Equals(b.Hist) {
			t.Fatalf("%s mismatch", name)
		}
	}
	eq("total", got.Total, orig.Total)
	if len(got.ByStep) != len(orig.ByStep) {
		t.Fatalf("steps: %d vs %d", len(got.ByStep), len(orig.ByStep))
	}
	for name, st := range orig.ByStep {
		eq(name, got.ByStep[name], st)
	}
	if Summarize(&got).P99 != Summarize(orig).P99 {
		t.Fatal("p99 differs after round trip")
	}

	// Decoded snapshots are mergeable with local ones.
	got.Merge(orig)
	if got.Total.Requests != 2*orig.Total.Requests {
		t.Fatal("merge after decode failed")
	}

	// Empty snapshot round trip and garbage input.
	empty := &Snapshot{}
	if data, err = empty.MarshalBinary(); err != nil {
		t.Fatal(err)
	}
	var e Snapshot
	if err := e.UnmarshalBinary(data); err != nil || e.Total == nil || e.Total.Requests != 0 {
		t.Fatalf("empty round trip: %+v %v", e, err)
	}
	if err := e.UnmarshalBinary([]byte("garbage")); err == nil {
		t.Fatal("expected error on garbage")
	}
}

func TestRecordNoAllocs(t *testing.T) {
	c := NewCollector(t0)
	r := res("a", 3*time.Millisecond, false)
	c.Record(r) // warm the step entry
	allocs := testing.AllocsPerRun(1000, func() { c.Record(r) })
	// Allow a stray allocation from a shard's first sight of the step.
	if allocs > 0.01 {
		t.Fatalf("Record allocates %.2f times per call", allocs)
	}
}

func BenchmarkCollectorRecord(b *testing.B) {
	c := NewCollector(t0)
	r := res("a", 3*time.Millisecond, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Record(r)
	}
}

func BenchmarkCollectorRecordParallel(b *testing.B) {
	c := NewCollector(t0)
	steps := []string{"login", "list", "detail", "checkout"}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Record(protocol.Result{Step: steps[i&3], Latency: time.Duration(i&1023) * time.Millisecond, Bytes: 512})
			i++
		}
	})
}

func BenchmarkFlush(b *testing.B) {
	c := NewCollector(t0)
	steps := []string{"login", "list", "detail", "checkout"}
	now := t0
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 1000; j++ {
			c.Record(protocol.Result{Step: steps[j&3], Latency: time.Duration(j) * time.Millisecond})
		}
		now = now.Add(time.Second)
		c.Flush(now)
	}
}
