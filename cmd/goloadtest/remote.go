package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/apiclient"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// Tunables, variables so tests can shorten them.
var (
	// remoteRequestTimeout bounds every single HTTP request.
	remoteRequestTimeout = 30 * time.Second
	// remoteRetries is how often a GET is retried on a transient error.
	remoteRetries = 3
	// remoteRetryBackoff is the base pause between GET retries (grows linearly).
	remoteRetryBackoff = 500 * time.Millisecond
	// remotePlainEvery is the progress line interval when stdout is not a TTY.
	remotePlainEvery = 10 * time.Second
	// remoteMaxPollFailures is how many polls in a row may fail (each after
	// its own retries) before the run is given up.
	remoteMaxPollFailures = 5
)

// cmdRemote dispatches `goloadtest remote <subcommand>`.
func cmdRemote(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprintln(stderr, "usage: goloadtest remote run --server URL (--test NAME|ID | --suite NAME|ID) [flags]")
		return exitUsage
	}
	if args[0] != "run" {
		fmt.Fprintf(stderr, "remote: unknown subcommand %q (want: run)\n", args[0])
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return remoteRun(ctx, args[1:], stdout, stderr)
}

// remoteOpts are the parsed flags of `remote run`.
type remoteOpts struct {
	server       string // normalized base URL, no trailing slash, no credentials
	token        string
	test, suite  string
	label        string
	workers      int
	junit, json  string
	timeout      time.Duration
	poll         time.Duration
	stopOnCancel bool
}

func parseRemoteOpts(args []string, stderr io.Writer) (*remoteOpts, int) {
	fs := flag.NewFlagSet("remote run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", os.Getenv("GOLOAD_SERVER"), "GoLoad server URL, e.g. https://goload.example.com (env GOLOAD_SERVER)")
	token := fs.String("token", "", "API bearer token (prefer env GOLOAD_TOKEN; flags show up in ps)")
	test := fs.String("test", "", "run this saved test (name or id)")
	suite := fs.String("suite", "", "run this suite (name or id)")
	label := fs.String("label", "", "label for the run, e.g. a git SHA or version")
	workers := fs.Int("workers", 0, "worker count (test only; 0 = the server default)")
	junit := fs.String("junit", "", "write a JUnit XML report to this file")
	jsonOut := fs.String("json", "", "write the final run / suite run as JSON to this file")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up (and stop the remote run) after this long")
	poll := fs.Duration("poll", 2*time.Second, "poll interval")
	noStop := fs.Bool("no-stop-on-cancel", false, "leave the remote run going on Ctrl+C, SIGTERM or --timeout")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: goloadtest remote run --server URL [--token T] (--test NAME|ID | --suite NAME|ID) [flags]")
		fmt.Fprintln(stderr, "Runs a saved test or suite on a GoLoad server and waits for the result.")
		fmt.Fprintln(stderr, "Exit codes: 0 passed, 1 failed, 2 usage, 3 error/stopped/timeout/unreachable.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, exitUsage
	}
	bad := func(format string, a ...any) (*remoteOpts, int) {
		fmt.Fprintf(stderr, "remote run: "+format+"\n", a...)
		return nil, exitUsage
	}
	if fs.NArg() != 0 {
		return bad("unexpected argument %q", fs.Arg(0))
	}
	if (*test == "") == (*suite == "") {
		return bad("exactly one of --test or --suite is required")
	}
	if *workers < 0 {
		return bad("--workers must be >= 0")
	}
	if *workers != 0 && *suite != "" {
		return bad("--workers applies to --test only (set workers per item in the suite)")
	}
	if *poll <= 0 || *timeout <= 0 {
		return bad("--poll and --timeout must be positive")
	}
	if *server == "" {
		return bad("--server is required")
	}
	u, err := url.Parse(*server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return bad("--server must be an http:// or https:// URL")
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	tok := *token
	if tok == "" {
		tok = os.Getenv("GOLOAD_TOKEN")
	}
	return &remoteOpts{
		server: strings.TrimRight(u.String(), "/"), token: tok,
		test: *test, suite: *suite, label: *label, workers: *workers,
		junit: *junit, json: *jsonOut, timeout: *timeout, poll: *poll,
		stopOnCancel: !*noStop,
	}, exitOK
}

// printAPIError prints an error with its details and hints.
func printAPIError(w io.Writer, prefix string, err error) {
	fmt.Fprintf(w, "%s: %v\n", prefix, err)
	var ae *apiclient.APIError
	if errors.As(err, &ae) {
		for _, d := range ae.Details {
			fmt.Fprintf(w, "  - %s\n", d)
		}
		if ae.Status == http.StatusUnauthorized {
			fmt.Fprintln(w, "  hint: set GOLOAD_TOKEN (or --token) to the server's API token")
		}
	}
}

// remoteResult is the final state in one shape for suites and single runs.
type remoteResult struct {
	Kind     string // "test" or "suite"
	Name     string
	ID       string // run or suite run id
	URL      string
	Status   model.RunStatus
	Error    string
	Started  time.Time
	Finished *time.Time
	Items    []remoteItem
	Raw      any // model.Run or model.SuiteRun
}

type remoteItem struct {
	Name       string
	Status     string
	Error      string
	RunID      string
	Started    *time.Time
	Finished   *time.Time
	Summary    *model.Summary
	Thresholds []model.ThresholdResult
}

func (it remoteItem) duration() time.Duration {
	if it.Started == nil || it.Finished == nil {
		return 0
	}
	return it.Finished.Sub(*it.Started)
}

func (it remoteItem) failedThresholds() []string {
	var out []string
	for _, t := range it.Thresholds {
		if !t.Passed {
			out = append(out, fmt.Sprintf("%s %s %g (actual %.3g)", t.Metric, t.Op, t.Value, t.Actual))
		}
	}
	return out
}

func resultFromRun(base string, r model.Run) remoteResult {
	return remoteResult{
		Kind: "test", Name: r.TestName, ID: r.ID, URL: base + "/runs/" + r.ID,
		Status: r.Status, Error: r.Error, Started: r.StartedAt, Finished: r.FinishedAt, Raw: r,
		Items: []remoteItem{{
			Name: r.TestName, Status: string(r.Status), Error: r.Error, RunID: r.ID,
			Started: &r.StartedAt, Finished: r.FinishedAt, Summary: r.Summary, Thresholds: r.Thresholds,
		}},
	}
}

func resultFromSuiteRun(base string, s model.SuiteRun) remoteResult {
	res := remoteResult{
		Kind: "suite", Name: s.SuiteName, ID: s.ID, URL: base + "/suite-runs/" + s.ID,
		Status: s.Status, Error: s.Error, Started: s.StartedAt, Finished: s.FinishedAt, Raw: s,
	}
	for _, it := range s.Items {
		res.Items = append(res.Items, remoteItem{
			Name: it.TestName, Status: it.Status, Error: it.Error, RunID: it.RunID,
			Started: it.StartedAt, Finished: it.FinishedAt, Summary: it.Summary,
		})
	}
	return res
}

// exitFor maps a finished result to an exit code.
func (r remoteResult) exitFor() int {
	switch r.Status {
	case model.RunPassed:
		for _, it := range r.Items {
			if it.Status != string(model.RunPassed) {
				return exitThreshold
			}
		}
		return exitOK
	case model.RunFailed:
		return exitThreshold
	default: // stopped, error, still active
		return exitRun
	}
}

// remoteRun is the testable body of `remote run`. ctx is cancelled by
// Ctrl+C / SIGTERM.
func remoteRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code := parseRemoteOpts(args, stderr)
	if o == nil {
		return code
	}
	c := &apiclient.Client{BaseURL: o.server, Token: o.token, HTTP: &http.Client{Timeout: remoteRequestTimeout},
		Retries: remoteRetries, Backoff: remoteRetryBackoff}

	// --- resolve ---
	kind, listPath := "test", "/api/tests"
	ref := o.test
	if o.suite != "" {
		kind, listPath, ref = "suite", "/api/suites", o.suite
	}
	var items []apiclient.Ref
	if kind == "test" {
		var ts []model.Test
		if err := c.Do(ctx, http.MethodGet, listPath, nil, &ts); err != nil {
			printAPIError(stderr, "remote run: listing tests", err)
			return exitRun
		}
		for _, t := range ts {
			items = append(items, apiclient.Ref{ID: t.ID, Name: t.Name})
		}
	} else {
		var ss []model.Suite
		if err := c.Do(ctx, http.MethodGet, listPath, nil, &ss); err != nil {
			printAPIError(stderr, "remote run: listing suites", err)
			return exitRun
		}
		for _, s := range ss {
			items = append(items, apiclient.Ref{ID: s.ID, Name: s.Name})
		}
	}
	target, err := apiclient.ResolveRef(kind, ref, items)
	if err != nil {
		fmt.Fprintf(stderr, "remote run: %v\n", err)
		return exitUsage
	}

	// --- start ---
	var (
		id       string
		statPath string // GET path prefix
		stopPath string
		res      remoteResult
	)
	if kind == "test" {
		var run model.Run
		req := model.StartRunRequest{Workers: o.workers, Label: o.label}
		if err := c.Do(ctx, http.MethodPost, "/api/tests/"+url.PathEscape(target.ID)+"/runs", req, &run); err != nil {
			printAPIError(stderr, fmt.Sprintf("remote run: server rejected starting test %q", target.Name), err)
			return exitRun
		}
		id, statPath = run.ID, "/api/runs/"
		stopPath = statPath + url.PathEscape(id) + "/stop"
		res = resultFromRun(o.server, run)
	} else {
		var sr model.SuiteRun
		req := model.StartSuiteRequest{Label: o.label}
		if err := c.Do(ctx, http.MethodPost, "/api/suites/"+url.PathEscape(target.ID)+"/runs", req, &sr); err != nil {
			printAPIError(stderr, fmt.Sprintf("remote run: server rejected starting suite %q", target.Name), err)
			return exitRun
		}
		id, statPath = sr.ID, "/api/suite-runs/"
		stopPath = statPath + url.PathEscape(id) + "/stop"
		res = resultFromSuiteRun(o.server, sr)
	}
	fmt.Fprintf(stdout, "started %s %q: %s\n", kind, target.Name, res.URL)

	fetch := func(ctx context.Context) (remoteResult, error) {
		if kind == "test" {
			var r model.Run
			if err := c.Do(ctx, http.MethodGet, statPath+url.PathEscape(id), nil, &r); err != nil {
				return remoteResult{}, err
			}
			return resultFromRun(o.server, r), nil
		}
		var s model.SuiteRun
		if err := c.Do(ctx, http.MethodGet, statPath+url.PathEscape(id), nil, &s); err != nil {
			return remoteResult{}, err
		}
		return resultFromSuiteRun(o.server, s), nil
	}

	// --- poll ---
	tctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	tty := isTTY(stdout)
	prog := &remoteProgress{w: stdout, tty: tty}
	failures := 0
	var interrupted string // why we stopped waiting, "" = finished normally
	var pollErr error
	ticker := time.NewTicker(o.poll)
	defer ticker.Stop()
poll:
	for {
		r, err := fetch(tctx)
		switch {
		case err == nil:
			failures = 0
			res = r
			if r.Status.Finished() {
				break poll
			}
			prog.update(r)
		case tctx.Err() != nil:
			// handled below
		default:
			var ae *apiclient.APIError
			if errors.As(err, &ae) && !ae.Transient() {
				pollErr = err
				break poll
			}
			if failures++; failures >= remoteMaxPollFailures {
				pollErr = err
				break poll
			}
			prog.note(fmt.Sprintf("poll failed (%v), retrying", err))
		}
		select {
		case <-tctx.Done():
			if ctx.Err() != nil {
				interrupted = "interrupted"
			} else {
				interrupted = fmt.Sprintf("timed out after %s", o.timeout)
			}
			break poll
		case <-ticker.C:
		}
	}
	prog.done()

	exit := exitOK
	switch {
	case interrupted != "" || pollErr != nil:
		reason := interrupted
		if pollErr != nil {
			printAPIError(stderr, "remote run: lost contact with the server", pollErr)
			reason = "server unreachable"
		} else {
			fmt.Fprintf(stderr, "remote run: %s\n", reason)
		}
		if o.stopOnCancel {
			sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := c.Do(sctx, http.MethodPost, stopPath, nil, nil); err != nil {
				printAPIError(stderr, "remote run: could not stop the remote run", err)
			} else {
				fmt.Fprintf(stderr, "remote run: stop requested for %s\n", res.URL)
				// Pick up the stopped state for the reports (best effort).
				time.Sleep(min(o.poll, time.Second))
				if r, err := fetch(sctx); err == nil {
					res = r
				}
			}
			scancel()
		} else {
			fmt.Fprintf(stderr, "remote run: leaving the remote run active: %s\n", res.URL)
		}
		exit = exitRun
	default:
		exit = res.exitFor()
	}

	// Per-item thresholds for suites (best effort).
	if kind == "suite" {
		fctx, fcancel := context.WithTimeout(context.Background(), 20*time.Second)
		for i, it := range res.Items {
			if it.RunID == "" || !model.RunStatus(it.Status).Finished() {
				continue
			}
			var r model.Run
			if err := c.Do(fctx, http.MethodGet, "/api/runs/"+url.PathEscape(it.RunID), nil, &r); err == nil {
				res.Items[i].Thresholds = r.Thresholds
			}
		}
		fcancel()
	}

	printRemoteSummary(stdout, res)
	if interrupted == "" && pollErr == nil && exit == exitRun {
		fmt.Fprintf(stderr, "remote run: %s ended with status %s\n", kind, res.Status)
	}

	if o.junit != "" {
		if err := writeJUnit(o.junit, res); err != nil {
			fmt.Fprintf(stderr, "remote run: writing JUnit report: %v\n", err)
			if exit == exitOK {
				exit = exitRun
			}
		}
	}
	if o.json != "" {
		if err := writeJSONFile(o.json, res.Raw); err != nil {
			fmt.Fprintf(stderr, "remote run: writing JSON file: %v\n", err)
			if exit == exitOK {
				exit = exitRun
			}
		}
	}
	return exit
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// isTTY reports whether w is an interactive terminal.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// remoteProgress prints a rewriting line on a TTY and a plain line every
// remotePlainEvery (and on every status change) otherwise.
type remoteProgress struct {
	w      io.Writer
	tty    bool
	last   time.Time
	lastSt string
	dirty  bool
}

func (p *remoteProgress) update(r remoteResult) {
	line := progressLine(r)
	if p.tty {
		fmt.Fprintf(p.w, "\r\x1b[K%s", line)
		p.dirty = true
		return
	}
	st := string(r.Status)
	if p.last.IsZero() || st != p.lastSt || time.Since(p.last) >= remotePlainEvery {
		fmt.Fprintln(p.w, line)
		p.last, p.lastSt = time.Now(), st
	}
}

func (p *remoteProgress) note(s string) {
	if p.tty && p.dirty {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
	fmt.Fprintln(p.w, s)
}

func (p *remoteProgress) done() {
	if p.tty && p.dirty {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
}

func progressLine(r remoteResult) string {
	elapsed := time.Since(r.Started).Truncate(time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	if r.Kind == "suite" {
		fin, cur := 0, ""
		for _, it := range r.Items {
			if model.RunStatus(it.Status).Finished() || it.Status == model.SuiteItemSkipped {
				fin++
			} else if it.Status == string(model.RunRunning) && cur == "" {
				cur = it.Name
			}
		}
		s := fmt.Sprintf("%s %8s  %d/%d tests done", r.Status, elapsed, fin, len(r.Items))
		if cur != "" {
			s += "  running: " + cur
		}
		return s
	}
	s := fmt.Sprintf("%s %8s", r.Status, elapsed)
	if sm := r.Items[0].Summary; sm != nil {
		s += fmt.Sprintf("  req %d  rps %.1f  p95 %s  err %.2f%%", sm.Requests, sm.RPS, fmtMs2(sm.P95Ms), sm.ErrorRate*100)
	}
	return s
}

// fmtMs2 formats a millisecond value like fmtDur does for durations.
func fmtMs2(ms float64) string { return fmtDur(time.Duration(ms * float64(time.Millisecond))) }

func printRemoteSummary(w io.Writer, r remoteResult) {
	fmt.Fprintf(w, "\n== %s %q: %s ==\n", r.Kind, r.Name, strings.ToUpper(string(r.Status)))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "test\tstatus\trequests\trps\tp95\terr%\tfailed thresholds")
	for _, it := range r.Items {
		reqs, rps, p95, er := "-", "-", "-", "-"
		if s := it.Summary; s != nil {
			reqs = fmt.Sprint(s.Requests)
			rps = fmt.Sprintf("%.1f", s.RPS)
			p95 = fmtMs2(s.P95Ms)
			er = fmt.Sprintf("%.2f", s.ErrorRate*100)
		}
		ft := strings.Join(it.failedThresholds(), "; ")
		if ft == "" {
			ft = "-"
		}
		name := it.Name
		if name == "" {
			name = "(deleted test)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, it.Status, reqs, rps, p95, er, ft)
	}
	tw.Flush()
	for _, it := range r.Items {
		if it.Error != "" {
			fmt.Fprintf(w, "error (%s): %s\n", it.Name, it.Error)
		}
	}
	if r.Error != "" {
		fmt.Fprintf(w, "error: %s\n", r.Error)
	}
	fmt.Fprintf(w, "\n%s\n", r.URL)
}
