package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// minRetention is the shortest accepted -retention / -older-than: a
// typo such as "90m" for "90d" must not wipe the run history.
const minRetention = time.Hour

// retentionInterval is how often serve prunes when -retention is set.
const retentionInterval = time.Hour

// parseRetention parses a retention age: a Go duration ("720h", "90m")
// optionally led by whole days ("90d", "1d12h"). "" and "0" mean 0
// (keep forever). Anything else must be at least minRetention.
func parseRetention(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return 0, nil
	}
	var d time.Duration
	rest := v
	if i := strings.IndexByte(v, 'd'); i >= 0 {
		days, err := strconv.ParseUint(v[:i], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: want e.g. 90d, 36h or 1d12h", v)
		}
		d = time.Duration(days) * 24 * time.Hour
		rest = v[i+1:]
	}
	if rest != "" {
		x, err := time.ParseDuration(rest)
		if err != nil || x < 0 {
			return 0, fmt.Errorf("invalid duration %q: want e.g. 90d, 36h or 1d12h", v)
		}
		d += x
	}
	if d < minRetention {
		return 0, fmt.Errorf("duration %q is below the 1h minimum", v)
	}
	return d, nil
}

// fmtRetention renders d as days when it is a whole number of days.
func fmtRetention(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
}

// startRetention prunes runs older than keep now and then every
// retentionInterval until ctx is done. The returned function waits for
// the background goroutine to exit (cancel ctx first).
func startRetention(ctx context.Context, st store.Store, keep time.Duration, log *slog.Logger) (wait func(), err error) {
	p, ok := st.(store.Pruner)
	if !ok {
		return nil, fmt.Errorf("store %T does not support retention", st)
	}
	log.Info("retention enabled: finished runs are deleted once older than this (pinned runs, baselines and each test's latest run are kept)",
		"retention", fmtRetention(keep), "check_every", retentionInterval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(retentionInterval)
		defer t.Stop()
		for {
			pruneOnce(ctx, p, keep, log)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() { <-done }, nil
}

// pruneOnce runs one retention pass and logs the outcome.
func pruneOnce(ctx context.Context, p store.Pruner, keep time.Duration, log *slog.Logger) {
	cutoff := time.Now().Add(-keep)
	start := time.Now()
	runs, err := p.PruneRuns(ctx, cutoff, false)
	switch {
	case err != nil && ctx.Err() != nil:
		return // shutting down
	case err != nil:
		log.Warn("retention: prune runs", "error", err, "deleted", len(runs))
	case len(runs) > 0:
		log.Info("retention: deleted old runs", "count", len(runs),
			"older_than", cutoff.UTC().Format(time.RFC3339), "took", time.Since(start).Round(time.Millisecond))
	default:
		log.Debug("retention: nothing to delete", "older_than", cutoff.UTC().Format(time.RFC3339))
	}
}

// cmdPrune deletes (or with -dry-run lists) finished runs older than
// -older-than from a store, with the same exemptions as serve -retention.
func cmdPrune(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("prune", flag.ContinueOnError)
	flags.SetOutput(stderr)
	db := flags.String("db", envOr("GOLOAD_DB", defaultDB),
		`store: "sqlite:<path>" (or a path ending in .db) or "postgres://..." DSN`)
	olderThan := flags.String("older-than", "", `delete finished runs older than this, e.g. 30d, 720h or 1d12h (required, min 1h)`)
	dryRun := flags.Bool("dry-run", false, "only list the runs that would be deleted")
	flags.Usage = func() {
		fmt.Fprint(stderr, `Usage: goloadtest prune -older-than 30d [-db sqlite:./goload.db] [-dry-run]

Deletes finished runs (with their metrics, error samples and error groups)
whose finish time is older than -older-than. Pinned runs, a test's baseline
run and each test's latest finished run are always kept; queued and running runs
are never touched. SQLite files are compacted afterwards.

`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "prune: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *olderThan == "" {
		fmt.Fprintln(stderr, "prune: -older-than is required (e.g. -older-than 30d)")
		return exitUsage
	}
	keep, err := parseRetention(*olderThan)
	if err == nil && keep == 0 {
		err = errors.New("must be greater than 0")
	}
	if err != nil {
		fmt.Fprintf(stderr, "prune: -older-than: %v\n", err)
		return exitUsage
	}
	spec, err := parseStoreSpec(*db)
	if err != nil {
		fmt.Fprintf(stderr, "prune: -db: %v\n", err)
		return exitUsage
	}
	switch spec.kind {
	case "memory":
		fmt.Fprintln(stderr, "prune: -db memory holds no data to prune")
		return exitUsage
	case "sqlite":
		// Do not create an empty database by mistake (wrong path).
		if _, err := os.Stat(spec.loc); err != nil {
			fmt.Fprintf(stderr, "prune: %v\n", err)
			return exitUsage
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := openStore(ctx, spec)
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return exitRun
	}
	defer st.Close()
	p, ok := st.(store.Pruner)
	if !ok {
		fmt.Fprintf(stderr, "prune: store %s does not support pruning\n", spec.kind)
		return exitRun
	}

	cutoff := time.Now().Add(-keep)
	runs, err := p.PruneRuns(ctx, cutoff, *dryRun)
	if len(runs) > 0 {
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RUN\tTEST\tSTATUS\tSTARTED\tFINISHED")
		for _, r := range runs {
			fin := "-"
			if !r.FinishedAt.IsZero() {
				fin = r.FinishedAt.Local().Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.TestName, r.Status, r.StartedAt.Local().Format(time.DateTime), fin)
		}
		tw.Flush()
	}
	verb := "deleted"
	if *dryRun {
		verb = "would delete"
	}
	fmt.Fprintf(stdout, "%s %d run(s) older than %s (before %s)\n", verb, len(runs), fmtRetention(keep),
		cutoff.Local().Format(time.DateTime))
	if err != nil {
		fmt.Fprintf(stderr, "prune: %v\n", err)
		return exitRun
	}
	return exitOK
}
