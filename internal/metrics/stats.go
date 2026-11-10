package metrics

import (
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

// Histogram geometry shared by every histogram in this package. All values
// are latencies in microseconds. Using one geometry everywhere keeps merges
// lossless.
const (
	// LowestLatencyMicros is the smallest recordable latency (1us). Shorter
	// latencies, including 0, are recorded as this value.
	LowestLatencyMicros int64 = 1
	// HighestLatencyMicros is the largest recordable latency (60s). Longer
	// latencies are clamped to this value: they are still counted, never
	// dropped.
	HighestLatencyMicros int64 = 60_000_000
	// SignificantFigures is the histogram precision.
	SignificantFigures = 3
)

// newHist returns an empty histogram with the package geometry.
func newHist() *hdrhistogram.Histogram {
	return hdrhistogram.New(LowestLatencyMicros, HighestLatencyMicros, SignificantFigures)
}

// latencyMicros converts d to microseconds, clamped to
// [LowestLatencyMicros, HighestLatencyMicros].
func latencyMicros(d time.Duration) int64 {
	us := d.Microseconds()
	if us < LowestLatencyMicros {
		return LowestLatencyMicros
	}
	if us > HighestLatencyMicros {
		return HighestLatencyMicros
	}
	return us
}

// Stats aggregates the results of one step (or of all steps).
//
// Requests counts every result, Errors those with a non-nil Err, Bytes sums
// response body bytes and Hist holds latencies in microseconds.
type Stats struct {
	Requests, Errors, Bytes int64
	Hist                    *hdrhistogram.Histogram
}

// NewStats returns empty Stats with an allocated histogram.
func NewStats() *Stats {
	return &Stats{Hist: newHist()}
}

// record adds one observation. us must already be clamped.
func (s *Stats) record(us, bytes int64, failed bool) {
	s.Requests++
	if failed {
		s.Errors++
	}
	s.Bytes += bytes
	// us is clamped to the histogram range, so this cannot fail.
	_ = s.Hist.RecordValue(us)
}

// Merge adds o's counters and histogram into s. A nil o is a no-op.
// Percentiles of the result are computed from the merged histogram, which
// is the only correct way to combine them.
func (s *Stats) Merge(o *Stats) {
	if o == nil {
		return
	}
	s.Requests += o.Requests
	s.Errors += o.Errors
	s.Bytes += o.Bytes
	if o.Hist != nil && o.Hist.TotalCount() > 0 {
		if s.Hist == nil {
			s.Hist = newHist()
		}
		s.Hist.Merge(o.Hist)
	}
}

// Clone returns a deep copy of s that shares no memory with it.
func (s *Stats) Clone() *Stats {
	if s == nil {
		return nil
	}
	c := *s
	if s.Hist != nil {
		c.Hist = s.Hist.Clone()
	} else {
		c.Hist = newHist()
	}
	return &c
}

// reset zeroes s in place, keeping its histogram allocation.
func (s *Stats) reset() {
	s.Requests, s.Errors, s.Bytes = 0, 0, 0
	if s.Hist != nil {
		s.Hist.Reset()
	}
}

// Snapshot is the aggregate of all results in the window [Start, End).
// Total covers every step; ByStep is keyed by protocol.Result.Step.
// Dropped counts open-model arrivals that were not executed.
type Snapshot struct {
	Start, End time.Time
	Total      *Stats
	ByStep     map[string]*Stats
	Dropped    int64
}

// newSnapshot returns an empty snapshot for [start, end).
func newSnapshot(start, end time.Time) *Snapshot {
	return &Snapshot{Start: start, End: end, Total: NewStats(), ByStep: map[string]*Stats{}}
}

// Merge folds o into s: Total, every ByStep entry and Dropped are added and
// the time window is widened to cover both snapshots (zero times are
// ignored). s never aliases o's histograms afterwards. A nil o is a no-op.
func (s *Snapshot) Merge(o *Snapshot) {
	if o == nil {
		return
	}
	if s.Total == nil {
		s.Total = NewStats()
	}
	s.Total.Merge(o.Total)
	for name, st := range o.ByStep {
		if st == nil {
			continue
		}
		if s.ByStep == nil {
			s.ByStep = make(map[string]*Stats, len(o.ByStep))
		}
		dst, ok := s.ByStep[name]
		if !ok {
			dst = NewStats()
			s.ByStep[name] = dst
		}
		dst.Merge(st)
	}
	s.Dropped += o.Dropped
	if !o.Start.IsZero() && (s.Start.IsZero() || o.Start.Before(s.Start)) {
		s.Start = o.Start
	}
	if !o.End.IsZero() && (s.End.IsZero() || o.End.After(s.End)) {
		s.End = o.End
	}
}

// Clone returns a deep copy of s that shares no memory with it.
func (s *Snapshot) Clone() *Snapshot {
	if s == nil {
		return nil
	}
	c := &Snapshot{Start: s.Start, End: s.End, Dropped: s.Dropped, ByStep: make(map[string]*Stats, len(s.ByStep))}
	if s.Total != nil {
		c.Total = s.Total.Clone()
	} else {
		c.Total = NewStats()
	}
	for name, st := range s.ByStep {
		if st != nil {
			c.ByStep[name] = st.Clone()
		}
	}
	return c
}
