package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/engine"
	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/executor"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// cpuWarnThreshold: above this load-generator CPU usage the run is flagged,
// because latency may then come from the worker, not the target.
const cpuWarnThreshold = 0.80

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("quiet", false, "do not print per-second progress")
	out := fs.String("out", "", "write the final summary as JSON to this file")
	maxConns := fs.Int("max-conns", 0, "max connections per host (0 = executor default)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "run: expected exactly one scenario file")
		return exitUsage
	}

	sc, err := scenario.LoadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "invalid scenario:\n%v\n", err)
		return exitUsage
	}

	var feeder data.Feeder = data.None{}
	if sc.Data != nil {
		if feeder, err = data.NewCSV(sc.Data.File, sc.Data.Mode); err != nil {
			fmt.Fprintf(stderr, "data: %v\n", err)
			return exitUsage
		}
	}

	exec, closeExec, err := executor.New(sc, executor.Options{HTTP: httpx.Options{MaxConnsPerHost: *maxConns}})
	if err != nil {
		fmt.Fprintf(stderr, "executor: %v\n", err)
		return exitUsage
	}
	defer closeExec()

	start := time.Now()
	col := metrics.NewCollector(start)
	groups := metrics.NewErrorGrouper(0, nil)
	eng := engine.New(sc.Load, engine.Options{
		Executor: exec,
		Feeder:   feeder,
		Sink: func(r protocol.Result) {
			col.Record(r)
			if r.Err != nil {
				groups.Record(r.Step, r.Status, r.Err)
			}
		},
		OnDropped: col.RecordDropped,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "running %q: %s model, %s, target %s\n",
		sc.Name, sc.Load.Model, sc.Load.TotalDuration(), sc.Target.BaseURL)

	cpu := newCPUMeter()
	var peakCPU float64
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	if !*quiet {
		printProgressHeader(stdout, sc.Load.Model)
	}

	var runErr error
loop:
	for {
		select {
		case now := <-ticker.C:
			snap := col.Flush(now)
			usage := cpu.sample()
			if usage > peakCPU {
				peakCPU = usage
			}
			if !*quiet {
				target, active := eng.Current()
				printProgress(stdout, now.Sub(start), target, active, metrics.Summarize(snap), usage)
			}
		case runErr = <-done:
			break loop
		}
	}
	col.Flush(time.Now())
	sum := metrics.Summarize(col.Cumulative())

	interrupted := errors.Is(runErr, context.Canceled)
	exhausted := errors.Is(runErr, data.ErrExhausted)
	if runErr != nil && !interrupted && !exhausted {
		fmt.Fprintf(stderr, "run error: %v\n", runErr)
	}

	results, terr := metrics.EvaluateThresholds(sum, sc.Thresholds)
	if terr != nil {
		fmt.Fprintf(stderr, "thresholds: %v\n", terr)
	}

	printSummary(stdout, sum, results)
	if len(sc.Regression) > 0 {
		fmt.Fprintln(stdout, "\nnote: regression rules are skipped (they compare with a test's baseline run; use the server)")
	}
	printErrorGroups(stdout, groups.Snapshot(), 10)
	switch {
	case interrupted:
		fmt.Fprintln(stdout, "\nrun interrupted; results are partial")
	case exhausted:
		fmt.Fprintln(stdout, "\nrun stopped early: test data exhausted")
	}
	if peakCPU >= cpuWarnThreshold {
		fmt.Fprintf(stdout, "\nWARNING: load generator CPU peaked at %.0f%%; latency may be inflated by this machine, not the target\n", peakCPU*100)
	}

	if *out != "" {
		if err := writeJSON(*out, sc, sum, results, peakCPU, runErr); err != nil {
			fmt.Fprintf(stderr, "write %s: %v\n", *out, err)
		}
	}

	switch {
	case runErr != nil && !interrupted && !exhausted:
		return exitRun
	case terr != nil:
		return exitUsage
	case !allPassed(results):
		return exitThreshold
	}
	return exitOK
}

func allPassed(rs []metrics.ThresholdResult) bool {
	for _, r := range rs {
		if !r.Passed {
			return false
		}
	}
	return true
}

type jsonReport struct {
	Scenario   string                    `json:"scenario"`
	Summary    metrics.Summary           `json:"summary"`
	Thresholds []metrics.ThresholdResult `json:"thresholds"`
	Passed     bool                      `json:"passed"`
	PeakCPU    float64                   `json:"peak_cpu"`
	Error      string                    `json:"error,omitempty"`
}

func writeJSON(path string, sc *scenario.Scenario, sum metrics.Summary, rs []metrics.ThresholdResult, cpu float64, runErr error) error {
	rep := jsonReport{Scenario: sc.Name, Summary: sum, Thresholds: rs, Passed: allPassed(rs), PeakCPU: cpu}
	if runErr != nil {
		rep.Error = runErr.Error()
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
