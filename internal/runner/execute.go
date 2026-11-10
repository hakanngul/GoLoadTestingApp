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
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
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
func (m *Manager) execute(ctx context.Context, ar *activeRun, sc *scenario.Scenario, exec *httpx.Executor, feeder data.Feeder) {
	defer m.wg.Done()
	defer exec.Close()
	defer ar.cancel()

	runID := ar.run.ID
	start := time.Now()
	col := metrics.NewCollector(start)
	samples := &errorSampler{max: int64(m.maxSamples), now: m.now}
	eng := engine.New(sc.Load, engine.Options{
		Executor: exec,
		Feeder:   feeder,
		Sink: func(r protocol.Result) {
			col.Record(r)
			if r.Err != nil {
				samples.offer(r)
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
			m.emitPoint(ar, col.Flush(now), now.Sub(start), eng, sampleCPU())
			m.flushSamples(runID, samples)
		case runErr = <-done:
			break loop
		}
	}

	// The tail since the last tick becomes a final (shorter) point if it
	// holds any traffic.
	now := time.Now()
	if tail := col.Flush(now); tail.Total.Requests > 0 || tail.Dropped > 0 {
		m.emitPoint(ar, tail, now.Sub(start), eng, sampleCPU())
	}
	m.flushSamples(runID, samples)

	final := m.finalRun(ar, sc, metrics.Summarize(col.Cumulative()), runErr, peakCPU)
	m.updateRun(final)
	ar.finish(final)
	m.remove(runID)
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
func (m *Manager) emitPoint(ar *activeRun, snap *metrics.Snapshot, elapsed time.Duration, eng *engine.Engine, cpu float64) {
	target, active := eng.Current()
	m.emit(ar, snap, elapsed, target, active, cpu, 1)
}

// emit stores and publishes one per-second point (local or merged remote).
func (m *Manager) emit(ar *activeRun, snap *metrics.Snapshot, elapsed time.Duration, target float64, active int64, cpu float64, workers int) {
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
