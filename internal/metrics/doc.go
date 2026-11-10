// Package metrics aggregates step results into HDR histograms.
//
// Design rules from the plan:
//   - Workers send histograms; the master MERGES them. Never average percentiles.
//   - Snapshots are per-interval (1s) and mergeable (for distributed runs).
//   - Latency is stored in microseconds, range 1us..60s, 3 significant digits.
//
// Public API (implemented in this package):
//
//	type Stats struct { Requests, Errors, Bytes int64; Hist *hdrhistogram.Histogram }
//	func NewStats() *Stats
//	func (s *Stats) Merge(o *Stats)
//
//	type Snapshot struct { Start, End time.Time; Total *Stats; ByStep map[string]*Stats; Dropped int64 }
//	func (s *Snapshot) Merge(o *Snapshot)
//
//	type Collector struct{ ... }              // safe for concurrent use
//	func NewCollector(start time.Time) *Collector
//	func (c *Collector) Record(r protocol.Result)
//	func (c *Collector) RecordDropped()
//	func (c *Collector) Flush(now time.Time) *Snapshot   // interval since last Flush; also folded into cumulative
//	func (c *Collector) Cumulative() *Snapshot           // copy of everything flushed so far + pending
//
//	type Summary struct {
//	    Requests, Errors, Dropped int64; ErrorRate, RPS float64
//	    Mean, P50, P90, P95, P99, Max time.Duration
//	    ByStep map[string]Summary // nil inside nested summaries
//	}
//	func Summarize(s *Snapshot) Summary
//
//	type ThresholdResult struct { Threshold scenario.Threshold; Actual float64; Passed bool }
//	func EvaluateThresholds(sum Summary, th []scenario.Threshold) ([]ThresholdResult, error)
//
// Additions beyond the planned API:
//
//	const LowestLatencyMicros, HighestLatencyMicros, SignificantFigures  // histogram geometry
//	func (s *Stats) Clone() *Stats
//	func (s *Snapshot) Clone() *Snapshot
//	func (s *Snapshot) MarshalBinary() ([]byte, error)   // gob + HdrHistogram V2 compressed
//	func (s *Snapshot) UnmarshalBinary(data []byte) error
//
// Latencies below 1us (including 0) are recorded as 1us; latencies above
// 60s are clamped to 60s and still counted.
package metrics
