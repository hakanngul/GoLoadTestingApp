package metrics

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
)

// liveStatsCollector returns a collector that has already flushed several
// seconds of traffic over 10 steps with a wide latency spread (so the
// histograms have many distinct buckets filled), as during a long run.
func liveStatsCollector(b *testing.B) (*Collector, func(time.Time) *Snapshot) {
	b.Helper()
	const steps, perTick = 10, 20000
	rng := rand.New(rand.NewSource(1))
	names := make([]string, steps)
	for i := range names {
		names[i] = fmt.Sprintf("step-%02d", i)
	}
	c := NewCollector(t0)
	now := t0
	fill := func() {
		for i := 0; i < perTick; i++ {
			lat := time.Duration(rng.ExpFloat64()*float64(20*time.Millisecond)) + time.Duration(rng.Intn(5000))*time.Microsecond
			c.Record(protocol.Result{Step: names[i%steps], Status: 200, Latency: lat})
		}
	}
	tick := func(at time.Time) *Snapshot { fill(); return c.Flush(at) }
	for i := 0; i < 30; i++ {
		now = now.Add(time.Second)
		tick(now)
	}
	return c, tick
}

// BenchmarkLiveStatsCumulative is the naive per-tick cost: deep copy of the
// cumulative snapshot plus pending data, then Summarize.
func BenchmarkLiveStatsCumulative(b *testing.B) {
	c, tick := liveStatsCollector(b)
	tick(t0.Add(31 * time.Second))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Summarize(c.Cumulative())
	}
}

// BenchmarkLiveStatsCumulativeSummary is what the runner does per tick.
func BenchmarkLiveStatsCumulativeSummary(b *testing.B) {
	c, tick := liveStatsCollector(b)
	tick(t0.Add(31 * time.Second))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CumulativeSummary()
	}
}

// BenchmarkLiveStatsRunningMerge keeps a separate running snapshot and
// merges each interval into it (the remote-run approach).
func BenchmarkLiveStatsRunningMerge(b *testing.B) {
	_, tick := liveStatsCollector(b)
	iv := tick(t0.Add(31 * time.Second))
	cum := iv.Clone()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cum.Merge(iv)
		_ = Summarize(cum)
	}
}

// BenchmarkLiveStatsInterval is Summarize of one interval (Current).
func BenchmarkLiveStatsInterval(b *testing.B) {
	_, tick := liveStatsCollector(b)
	iv := tick(t0.Add(31 * time.Second))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Summarize(iv)
	}
}
