package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/trial"
)

// cmdTry runs one iteration of a scenario and prints a per-step report
// with hints. Exit 0 when every step passed, 1 otherwise, 2 on usage or
// scenario errors.
func cmdTry(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("try", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("v", false, "also print request/response headers and body snippets")
	asJSON := fs.Bool("json", false, "print the result as JSON (same shape as POST /api/tests/try)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: goloadtest try [-v] [-json] scenario.yaml")
		fmt.Fprintln(stderr, "Runs ONE iteration (auth + each step once) and explains failures.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "try: expected exactly one scenario file")
		return exitUsage
	}
	sc, err := scenario.LoadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "invalid scenario:\n%v\n", err)
		return exitUsage
	}
	opts := trial.Options{ColumnsKnown: true}
	if sc.Data != nil {
		f, err := data.NewCSV(sc.Data.File, scenario.DataSequential)
		if err != nil {
			opts.ColumnsKnown = false
			opts.DataNote = err.Error()
		} else {
			opts.Vars = f.(*data.CSV).First()
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res := trial.Run(ctx, sc, opts)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		printTry(stdout, sc.Name, res, *verbose)
	}
	if !res.OK {
		return exitThreshold
	}
	return exitOK
}

func printTry(w io.Writer, name string, res model.TryResult, verbose bool) {
	fmt.Fprintf(w, "test request %q: %d step(s) in %s\n", name, len(res.Steps), fmtMs(res.DurationMs))
	if len(res.Data) > 0 {
		var kv []string
		for _, k := range slices.Sorted(maps.Keys(res.Data)) {
			kv = append(kv, k+"="+res.Data[k])
		}
		fmt.Fprintf(w, "data row: %s\n", strings.Join(kv, " "))
	}
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tSTEP\tRESULT\tSTATUS\tTIME\tREQUEST")
	for i, s := range res.Steps {
		status, dur := "-", "-"
		if s.State != model.TryNotRun {
			dur = fmtMs(s.DurationMs)
			if s.Status > 0 {
				status = fmt.Sprint(s.Status)
			}
		}
		req := strings.TrimSpace(s.Method + " " + s.URL)
		if s.Kind == "ws" {
			req = "WS " + s.URL
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", i+1, s.Name, stateLabel(s.State), status, dur, req)
	}
	tw.Flush()

	for i, s := range res.Steps {
		if s.State == model.TryNotRun {
			continue
		}
		var lines []string
		if s.Error != "" {
			lines = append(lines, "error: "+s.Error)
		}
		for _, h := range s.Redirects {
			l := fmt.Sprintf("redirect %d %s %s -> %s", h.Status, h.Method, h.URL, h.Location)
			if len(h.CookiesSet) > 0 {
				l += " (sets cookie " + strings.Join(h.CookiesSet, ", ") + ")"
			}
			lines = append(lines, l)
		}
		if s.Location != "" {
			lines = append(lines, "location: "+s.Location)
		}
		if len(s.CookiesSent) > 0 {
			lines = append(lines, "cookies sent: "+strings.Join(s.CookiesSent, ", "))
		}
		if len(s.CookiesSet) > 0 {
			lines = append(lines, "cookies set: "+strings.Join(s.CookiesSet, ", "))
		}
		for _, a := range s.Assertions {
			l := "assert " + mark(a.Passed) + " " + a.Assertion
			if a.Message != "" {
				l += " — " + a.Message
			}
			lines = append(lines, l)
		}
		for _, x := range s.Extracted {
			l := "extract " + mark(x.OK) + " {{" + x.Var + "}}"
			if x.OK {
				l += " = " + x.Value
			} else {
				l += " — " + x.Error
			}
			lines = append(lines, l)
		}
		for _, m := range s.Messages {
			l := "message " + mark(m.Error == "") + " " + m.Name + " " + fmtMs(m.DurationMs)
			if m.Error != "" {
				l += " — " + m.Error
			}
			lines = append(lines, l)
		}
		if verbose {
			for _, h := range s.RequestHeaders {
				lines = append(lines, "> "+h.Name+": "+h.Value)
			}
			if s.RequestBody != "" {
				lines = append(lines, "> body: "+oneLine(s.RequestBody, 300))
			}
			for _, h := range s.ResponseHeaders {
				lines = append(lines, "< "+h.Name+": "+h.Value)
			}
			switch {
			case s.BodyBinary:
				lines = append(lines, fmt.Sprintf("< body: (binary, %d bytes)", s.Bytes))
			case s.Body != "":
				lines = append(lines, "< body: "+oneLine(s.Body, 500))
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%d %s\n", i+1, s.Name)
		for _, l := range lines {
			fmt.Fprintf(w, "    %s\n", l)
		}
	}

	if len(res.Hints) > 0 {
		fmt.Fprintln(w, "\nhints:")
		for _, h := range res.Hints {
			prefix := strings.ToUpper(h.Level)
			if h.Step != "" {
				prefix += " [" + h.Step + "]"
			}
			fmt.Fprintf(w, "  %s %s\n", prefix, h.Message)
			if h.Fix != nil {
				fmt.Fprintf(w, "      fix: %s\n", h.Fix.Label)
			}
		}
	}
	if res.OK {
		fmt.Fprintln(w, "\nall steps passed")
	} else {
		fmt.Fprintln(w, "\nFAILED")
	}
}

func stateLabel(s string) string {
	switch s {
	case model.TryPassed:
		return "ok"
	case model.TryFailed:
		return "FAIL"
	}
	return "not run"
}

func mark(ok bool) string {
	if ok {
		return "ok  "
	}
	return "FAIL"
}

func fmtMs(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.2fs", v/1000)
	}
	return fmt.Sprintf("%.0fms", v)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
