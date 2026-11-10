package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/engine"
	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// cpuWarnThreshold: above this load-generator CPU usage the run is flagged,
// because latency may then come from the worker, not the target.
const cpuWarnThreshold = 0.80

// storeTimeout bounds every store write made by a run goroutine.
const storeTimeout = 10 * time.Second

// maxSampleMessage caps the length of a stored error sample message.
const maxSampleMessage = 1024

// execute drives one run to completion and persists its outcome.
func (m *Manager) execute(ctx context.Context, ar *activeRun, sc *scenario.Scenario, exec protocol.Executor, closeExec func(), feeder data.Feeder) {
	defer m.wg.Done()
	defer closeExec()
	defer ar.cancel()

	runID := ar.run.ID
	start := time.Now()
	col := metrics.NewCollector(start)
	samples := &errorSampler{max: int64(m.maxSamples), now: m.now}
	groups := ar.groups
	eng := engine.New(sc.Load, engine.Options{
		Executor: exec,
		Feeder:   feeder,
		Sink: func(r protocol.Result) {
			col.Record(r)
			if r.Err != nil {
				samples.offer(r)
				groups.Record(r.Step, r.Status, r.Err)
			}
		},
		OnDropped: col.RecordDropped,
	})

	cpu := newCPUMeter()
	var peakCPU float64
	sampleCPU := func() float64 {
		u := cpu.sample()
		if u > peakCPU {
			peakCPU = u
		}
		m.setCPU(u)
		return u
	}

	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var runErr error
loop:
	for {
		select {
		case now := <-ticker.C:
			cur := m.emitPoint(ar, col.Flush(now), now.Sub(start), eng, sampleCPU())
			// CumulativeSummary reuses the collector's running cumulative
			// snapshot (no histogram copies): ~2.5ms per tick for 10 steps,
			// about the cost of summarizing the interval itself.
			m.publishStats(ar, now.Sub(start), col.CumulativeSummary(), cur)
			m.flushSamples(runID, samples)
		case runErr = <-done:
			break loop
		}
	}

	// The tail since the last tick becomes a final (shorter) point if it
	// holds any traffic.
	now := time.Now()
	var cur metrics.Summary
	if tail := col.Flush(now); tail.Total.Requests > 0 || tail.Dropped > 0 {
		cur = m.emitPoint(ar, tail, now.Sub(start), eng, sampleCPU())
	}
	m.flushSamples(runID, samples)
	// Final stats: Total now equals the final summary.
	m.publishStats(ar, now.Sub(start), col.CumulativeSummary(), cur)

	final := m.finalRun(ar, sc, metrics.Summarize(col.Cumulative()), runErr, peakCPU)
	m.storeErrorGroups(runID, ar.groups)
	m.updateRun(final)
	ar.finish(final)
	m.remove(runID)
}

// publishStats publishes one model.LiveStats: total is cumulative since
// the start, current the last interval.
func (m *Manager) publishStats(ar *activeRun, elapsed time.Duration, total, current metrics.Summary) {
	ar.publishStats(&model.LiveStats{
		Elapsed: elapsed.Seconds(),
		Total:   liveSummary(total),
		Current: liveSummary(current),
		Errors:  ar.groups.Snapshot(),
	})
}

// liveSummary is toModelSummary with ByStep always present.
func liveSummary(s metrics.Summary) model.Summary {
	out := toModelSummary(s)
	if out.ByStep == nil {
		out.ByStep = map[string]model.Summary{}
	}
	return out
}

// storeErrorGroups persists the run's final error groups (if any).
func (m *Manager) storeErrorGroups(runID string, g *metrics.ErrorGrouper) {
	groups := g.Snapshot()
	if len(groups) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := m.store.SetErrorGroups(ctx, runID, groups); err != nil {
		m.log.Error("runner: store error groups", "run", runID, "count", len(groups), "err", err)
	}
}

// finalRun builds the terminal run record.
//
// Status: stopped when the engine was cancelled (Stop or Shutdown; a Stop
// that races with natural completion keeps the natural outcome); error
// when the engine failed unexpectedly or thresholds could not be
// evaluated; otherwise failed if any threshold failed, else passed. Data
// exhaustion (unique mode) ends the run early but is not an error: it adds
// a warning and thresholds decide.
func (m *Manager) finalRun(ar *activeRun, sc *scenario.Scenario, sum metrics.Summary, runErr error, peakCPU float64) model.Run {
	ar.mu.Lock()
	run := ar.run
	ar.mu.Unlock()

	s := toModelSummary(sum)
	run.Summary = &s
	run.PeakCPU = peakCPU
	run.Warnings = append([]string{}, run.Warnings...)
	run.Thresholds = []model.ThresholdResult{}

	results, terr := metrics.EvaluateThresholds(sum, sc.Thresholds)
	allPassed := true
	for _, r := range results {
		run.Thresholds = append(run.Thresholds, model.ThresholdResult{
			Metric: r.Threshold.Metric,
			Op:     r.Threshold.Op,
			Value:  r.Threshold.Value,
			Actual: r.Actual,
			Passed: r.Passed,
		})
		allPassed = allPassed && r.Passed
	}

	cancelled := errors.Is(runErr, context.Canceled)
	exhausted := errors.Is(runErr, data.ErrExhausted)
	if exhausted {
		run.Warnings = append(run.Warnings, "test data exhausted (unique mode): the run stopped early when every row had been used")
	}
	if peakCPU >= cpuWarnThreshold {
		run.Warnings = append(run.Warnings, fmt.Sprintf(
			"load generator CPU peaked at %.0f%%; latency may be inflated by the load generator, not the target", peakCPU*100))
	}

	switch {
	case cancelled:
		run.Status = model.RunStopped
	case runErr != nil && !exhausted:
		run.Status = model.RunError
		run.Error = runErr.Error()
	case terr != nil:
		run.Status = model.RunError
		run.Error = "thresholds: " + terr.Error()
	case !allPassed:
		run.Status = model.RunFailed
	default:
		run.Status = model.RunPassed
	}
	fin := m.now()
	run.FinishedAt = &fin
	return run
}

// emitPoint converts one interval snapshot into a MetricPoint, stores it
// with its encoded histograms and publishes it to subscribers.
func (m *Manager) emitPoint(ar *activeRun, snap *metrics.Snapshot, elapsed time.Duration, eng *engine.Engine, cpu float64) metrics.Summary {
	target, active := eng.Current()
	return m.emit(ar, snap, elapsed, target, active, cpu, 1)
}

// emit stores and publishes one per-second point (local or merged remote)
// and returns the interval's summary.
func (m *Manager) emit(ar *activeRun, snap *metrics.Snapshot, elapsed time.Duration, target float64, active int64, cpu float64, workers int) metrics.Summary {
	sum := metrics.Summarize(snap)
	p := model.MetricPoint{
		T:          m.now(),
		Elapsed:    elapsed.Seconds(),
		TargetRate: target,
		Active:     active,
		Requests:   sum.Requests,
		Errors:     sum.Errors,
		Dropped:    sum.Dropped,
		RPS:        sum.RPS,
		ErrorRate:  sum.ErrorRate,
		MeanMs:     ms(sum.Mean),
		P50Ms:      ms(sum.P50),
		P95Ms:      ms(sum.P95),
		P99Ms:      ms(sum.P99),
		MaxMs:      ms(sum.Max),
		CPU:        cpu,
		Workers:    workers,
	}
	runID := ar.run.ID // ID never changes after Start
	blob, err := snap.MarshalBinary()
	if err != nil {
		m.log.Error("runner: encode snapshot", "run", runID, "err", err)
		blob = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	if err := m.store.AppendMetric(ctx, runID, p, blob); err != nil {
		m.log.Error("runner: store metric point", "run", runID, "err", err)
	}
	cancel()
	ar.publish(Event{Type: EventMetric, Metric: &p})
	return sum
}

// flushSamples persists error samples collected since the last call.
func (m *Manager) flushSamples(runID string, s *errorSampler) {
	batch := s.drain()
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := m.store.AppendErrorSamples(ctx, runID, batch); err != nil {
		m.log.Error("runner: store error samples", "run", runID, "count", len(batch), "err", err)
	}
}

// updateRun persists r, logging (not propagating) failures.
func (m *Manager) updateRun(r model.Run) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := m.store.UpdateRun(ctx, r); err != nil {
		m.log.Error("runner: update run", "run", r.ID, "status", r.Status, "err", err)
	}
}

// errorSampler keeps the first max failed results of a run. Once full,
// offer costs a single atomic load.
type errorSampler struct {
	max   int64
	now   func() time.Time
	taken atomic.Int64

	mu      sync.Mutex
	pending []model.ErrorSample
}

func (s *errorSampler) offer(r protocol.Result) {
	if s.taken.Load() >= s.max || s.taken.Add(1) > s.max {
		return
	}
	msg := ""
	if r.Err != nil {
		msg = truncate(r.Err.Error(), maxSampleMessage)
	}
	sample := model.ErrorSample{T: s.now(), Step: r.Step, Status: r.Status, Message: msg}
	s.mu.Lock()
	s.pending = append(s.pending, sample)
	s.mu.Unlock()
}

func (s *errorSampler) drain() []model.ErrorSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

// truncate shortens s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s) + "…"
}

// toModelSummary converts a metrics.Summary to its JSON shape (durations
// in milliseconds), including per-step summaries.
func toModelSummary(s metrics.Summary) model.Summary {
	out := summaryFields(s)
	if len(s.ByStep) > 0 {
		out.ByStep = make(map[string]model.Summary, len(s.ByStep))
		for name, st := range s.ByStep {
			out.ByStep[name] = summaryFields(st)
		}
	}
	return out
}

func summaryFields(s metrics.Summary) model.Summary {
	return model.Summary{
		Requests:  s.Requests,
		Errors:    s.Errors,
		Dropped:   s.Dropped,
		ErrorRate: s.ErrorRate,
		RPS:       s.RPS,
		MeanMs:    ms(s.Mean),
		P50Ms:     ms(s.P50),
		P90Ms:     ms(s.P90),
		P95Ms:     ms(s.P95),
		P99Ms:     ms(s.P99),
		MaxMs:     ms(s.Max),
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
