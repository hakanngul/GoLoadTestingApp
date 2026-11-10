package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/provision"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// DefaultProvisionTimeout bounds how long a run waits for freshly launched
// workers to connect.
const DefaultProvisionTimeout = 5 * time.Minute

// remotePlan is everything executeRemote needs, prepared by Start.
type remotePlan struct {
	n         int               // workers to use
	provision int               // workers to launch first (0 = use connected ones)
	files     map[string][]byte // data files for workers
}

// connectedWorkers counts non-lost remote workers.
func (m *Manager) connectedWorkers() int {
	if m.dispatcher == nil {
		return 0
	}
	n := 0
	for _, w := range m.dispatcher.Workers() {
		if w.State != cluster.StateLost {
			n++
		}
	}
	return n
}

// planRemote decides whether a run executes on remote workers.
//
// Rules: with no remote workers connected and workers <= 1 the run is
// local (the master runs an in-process worker, plan "tek node" mode).
// Otherwise it needs n = max(workers, 1) idle remote workers; missing ones
// are launched through the Provisioner when one is configured.
func (m *Manager) planRemote(workers int) (*remotePlan, error) {
	connected := m.connectedWorkers()
	if workers <= 1 && connected == 0 {
		return nil, nil
	}
	if m.dispatcher == nil {
		return nil, ErrDistributedUnavailable
	}
	n := max(workers, 1)
	idle := m.dispatcher.Idle()
	if idle >= n {
		return &remotePlan{n: n}, nil
	}
	if m.provisioner == nil {
		return nil, fmt.Errorf("%w: %d requested, %d idle", cluster.ErrNotEnoughWorkers, n, idle)
	}
	return &remotePlan{n: n, provision: n - idle}, nil
}

// dataFiles returns the scenario's data file contents keyed by name.
func (m *Manager) dataFiles(ctx context.Context, test model.Test, sc *scenario.Scenario) (map[string][]byte, string, error) {
	if sc.Data == nil {
		return nil, "", nil
	}
	name := baseName(sc.Data.File)
	b, err := m.store.GetFile(ctx, test.ID, name)
	if err != nil {
		return nil, "", fmt.Errorf("runner: load data file %q: %w", name, err)
	}
	return map[string][]byte{name: b}, name, nil
}

// executeRemote drives a distributed run: optional provisioning, dispatch,
// then merging per-second results until all workers finish.
func (m *Manager) executeRemote(ctx context.Context, ar *activeRun, sc *scenario.Scenario, yaml string, plan *remotePlan) {
	defer m.wg.Done()
	defer ar.cancel()
	runID := ar.snapshot().ID

	var launched []string
	defer func() {
		if len(launched) == 0 {
			return
		}
		tctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.provisioner.Terminate(tctx, launched); err != nil {
			m.log.Error("runner: terminate workers", "run", runID, "count", len(launched), "err", err)
		}
	}()

	fail := func(err error) {
		run := ar.snapshot()
		run.Status = model.RunError
		if errors.Is(err, context.Canceled) {
			run.Status = model.RunStopped
		} else {
			run.Error = err.Error()
		}
		fin := m.now()
		run.FinishedAt = &fin
		m.updateRun(run)
		ar.finish(run)
		m.remove(runID)
	}

	if plan.provision > 0 {
		ids, err := m.launchWorkers(ctx, runID, plan)
		launched = ids
		if err != nil {
			fail(err)
			return
		}
		run := ar.snapshot()
		run.Status = model.RunRunning
		run.StartedAt = m.now()
		m.updateRun(run)
		ar.setRun(run)
	}

	spec := cluster.RunSpec{
		RunID:           runID,
		YAML:            yaml,
		Files:           plan.files,
		MaxErrorSamples: m.maxSamples,
		StartAt:         time.Now().Add(cluster.DefaultStartDelay),
	}
	rr, err := m.dispatcher.Start(ctx, spec, plan.n)
	if err != nil {
		fail(fmt.Errorf("dispatch to workers: %w", err))
		return
	}

	cum := &metrics.Snapshot{}
	var peakCPU float64
	samplesLeft := m.maxSamples
	seconds, errs := rr.Seconds(), rr.Errors()
	stopSent := false
	for seconds != nil || errs != nil {
		select {
		case sec, ok := <-seconds:
			if !ok {
				seconds = nil
				continue
			}
			ar.groups.Merge(sec.Errors)
			elapsed := time.Duration(sec.Index) * time.Second
			var cur metrics.Summary
			if sec.Snapshot != nil {
				cum.Merge(sec.Snapshot)
				peakCPU = max(peakCPU, sec.CPU)
				m.setCPU(sec.CPU)
				cur = m.emit(ar, sec.Snapshot, elapsed, sec.Target, sec.Active, sec.CPU, sec.Workers)
			}
			// cum is the running merged snapshot: summarizing it costs
			// about as much as summarizing the interval.
			m.publishStats(ar, elapsed, metrics.Summarize(cum), cur)
		case batch, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if samplesLeft <= 0 {
				continue
			}
			if len(batch) > samplesLeft {
				batch = batch[:samplesLeft]
			}
			samplesLeft -= len(batch)
			sctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
			if err := m.store.AppendErrorSamples(sctx, runID, batch); err != nil {
				m.log.Error("runner: store error samples", "run", runID, "err", err)
			}
			cancel()
		case <-ctx.Done():
			if !stopSent {
				rr.Stop()
				stopSent = true
			}
			ctx = context.Background() // keep draining; Done is now never ready
		}
	}
	<-rr.Done()
	res := rr.Result()

	var runErr error
	switch {
	case stopSent:
		runErr = context.Canceled
	case res.Err != nil:
		runErr = res.Err
	case res.DataExhausted:
		runErr = fmt.Errorf("worker data: %w", data.ErrExhausted)
	}
	if cum.Total == nil {
		cum.Total = metrics.NewStats()
	}
	final := m.finalRun(ar, sc, metrics.Summarize(cum), runErr, peakCPU)
	m.storeErrorGroups(runID, ar.groups)
	for _, id := range res.LostWorkers {
		final.Warnings = append(final.Warnings, fmt.Sprintf(
			"worker %s was lost mid-run; generated load was lower than the target from that point", id))
	}
	if wr, ok := rr.(cluster.WarningReporter); ok {
		final.Warnings = append(final.Warnings, wr.Warnings()...)
	}
	m.updateRun(final)
	ar.finish(final)
	m.remove(runID)
}

// launchWorkers starts plan.provision instances and waits until enough
// idle workers are connected. It returns the launched instance IDs (also
// on error, so the caller terminates them).
func (m *Manager) launchWorkers(ctx context.Context, runID string, plan *remotePlan) ([]string, error) {
	insts, err := m.provisioner.Launch(ctx, provision.LaunchRequest{
		RunID:       runID,
		Count:       plan.provision,
		MasterAddr:  m.masterAddr,
		WorkerToken: m.workerToken,
	})
	ids := make([]string, 0, len(insts))
	for _, in := range insts {
		ids = append(ids, in.ID)
	}
	if err != nil {
		var short *provision.ShortfallError
		if !errors.As(err, &short) || len(insts) == 0 {
			return ids, fmt.Errorf("launch workers: %w", err)
		}
		// Partial capacity: run with what we have.
		plan.n -= plan.provision - len(insts)
		m.log.Warn("runner: launched fewer workers than requested", "run", runID, "requested", plan.provision, "launched", len(insts))
	}

	wait, cancel := context.WithTimeout(ctx, m.provisionTimeout)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for m.dispatcher.Idle() < plan.n {
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return ids, ctx.Err()
			}
			return ids, fmt.Errorf("launched workers did not connect within %s (%d of %d idle)",
				m.provisionTimeout, m.dispatcher.Idle(), plan.n)
		case <-tick.C:
		}
	}
	return ids, nil
}
