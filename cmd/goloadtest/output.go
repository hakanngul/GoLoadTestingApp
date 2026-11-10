package main

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func printProgressHeader(w io.Writer, model scenario.LoadModel) {
	target := "target_rps"
	if model == scenario.ModelClosed {
		target = "target_vus"
	}
	fmt.Fprintf(w, "%8s %10s %7s %8s %9s %9s %7s %8s %5s\n",
		"elapsed", target, "active", "rps", "p50", "p95", "err%", "dropped", "cpu%")
}

func printProgress(w io.Writer, elapsed time.Duration, target float64, active int64, s metrics.Summary, cpu float64) {
	fmt.Fprintf(w, "%8s %10.1f %7d %8.1f %9s %9s %7.2f %8d %5.0f\n",
		elapsed.Truncate(time.Second), target, active, s.RPS,
		fmtDur(s.P50), fmtDur(s.P95), s.ErrorRate*100, s.Dropped, cpu*100)
}

func printSummary(w io.Writer, s metrics.Summary, rs []metrics.ThresholdResult) {
	fmt.Fprintln(w, "\n== summary ==")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "step\trequests\terrors\terr%\trps\tmean\tp50\tp90\tp95\tp99\tmax\t")
	names := make([]string, 0, len(s.ByStep))
	for n := range s.ByStep {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		row(tw, n, s.ByStep[n])
	}
	row(tw, "TOTAL", s)
	tw.Flush()
	if s.Dropped > 0 {
		fmt.Fprintf(w, "dropped iterations (max_vus reached): %d\n", s.Dropped)
	}

	if len(rs) == 0 {
		return
	}
	fmt.Fprintln(w, "\n== thresholds ==")
	for _, r := range rs {
		mark := "PASS"
		if !r.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "  %s  %s %s %g  (actual %.3f)\n", mark, r.Threshold.Metric, r.Threshold.Op, r.Threshold.Value, r.Actual)
	}
}

func row(w io.Writer, name string, s metrics.Summary) {
	fmt.Fprintf(w, "%s\t%d\t%d\t%.2f\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s\t\n",
		name, s.Requests, s.Errors, s.ErrorRate*100, s.RPS,
		fmtDur(s.Mean), fmtDur(s.P50), fmtDur(s.P90), fmtDur(s.P95), fmtDur(s.P99), fmtDur(s.Max))
}

func fmtDur(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}
