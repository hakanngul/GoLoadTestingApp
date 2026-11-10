// Package trial runs ONE iteration of a scenario ("test request") and
// reports every step in detail, with hints that explain common problems
// (redirects, WAF blocks, undefined {{vars}}, TLS/DNS errors, ...).
//
// The iteration runs through the same executors as load runs
// (protocol/executor, httpx, wsx), so requests are built, sent, checked
// and extracted exactly as in a run; tracing only records what happened.
// Differences from one iteration of a run, all chosen to keep a trial
// short: think times and ws hold/ping are skipped, at most MaxSteps steps
// run, the whole iteration is bounded by Timeout, and the login cache is
// fresh (auth type login always logs in).
package trial

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/executor"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/wsx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// Defaults for Options.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultMaxSteps  = 20
	DefaultBodyLimit = 8 << 10
)

// Options configures Run.
type Options struct {
	// Vars is the data row used for {{vars}} (nil = none).
	Vars map[string]string
	// ColumnsKnown reports that Vars holds the CSV columns (or that the
	// scenario has no data file); it enables the static undefined-variable
	// check for CSV-sourced names.
	ColumnsKnown bool
	// DataNote explains why the scenario's data file was not used.
	DataNote string
	// Allow vets every target URL (the server allowlist). nil allows all.
	Allow func(rawURL string) error
	// Timeout bounds the whole iteration. 0 = DefaultTimeout.
	Timeout time.Duration
	// MaxSteps caps the steps tried. 0 = DefaultMaxSteps.
	MaxSteps int
	// BodyLimit caps captured body text. 0 = DefaultBodyLimit.
	BodyLimit int
}

// entry tracks one reported step while the iteration runs.
type entry struct {
	model.TryStep
	reported bool
	err      error
	trace    *httpx.Trace
}

// Run executes one iteration of sc (a parsed, valid scenario) and returns
// the per-step report and hints. It never sends a request to a target the
// allowlist rejects.
func Run(ctx context.Context, sc *scenario.Scenario, opts Options) model.TryResult {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = DefaultMaxSteps
	}
	if opts.BodyLimit <= 0 {
		opts.BodyLimit = DefaultBodyLimit
	}
	started := time.Now()

	tsc := trialScenario(sc, opts)
	entries := newEntries(tsc)
	res := model.TryResult{Data: maskVars(opts.Vars), DataNote: opts.DataNote}
	finish := func(hs []model.TryHint) model.TryResult {
		res.DurationMs = ms(time.Since(started))
		res.OK = true
		for _, e := range entries {
			res.Steps = append(res.Steps, e.TryStep)
			res.OK = res.OK && e.State == model.TryPassed
		}
		res.Hints = hs
		if res.Hints == nil {
			res.Hints = []model.TryHint{}
		}
		return res
	}

	var pre []model.TryHint
	if opts.DataNote != "" {
		pre = append(pre, model.TryHint{Level: "info", Message: "No CSV data used: " + opts.DataNote})
	}
	if n := len(sc.Steps); n > opts.MaxSteps {
		pre = append(pre, model.TryHint{Level: "info",
			Message: fmt.Sprintf("Only the first %d of %d steps were tried.", opts.MaxSteps, n)})
	}

	// The allowlist, exactly as for runs: every statically known target.
	if opts.Allow != nil {
		for _, u := range sc.TargetURLs() {
			if err := opts.Allow(u); err != nil {
				return finish(append(pre, notAllowedHint("", u)))
			}
		}
	}

	var (
		mu      sync.Mutex
		pending []*httpx.Trace
		wsSeen  = map[string]*httpx.Trace{}
	)
	xopts := executor.Options{
		HTTP: httpx.Options{
			MaxConnsPerHost: 4,
			CheckURL:        opts.Allow,
			Trace: func(t *httpx.Trace) {
				mu.Lock()
				pending = append(pending, t)
				mu.Unlock()
			},
		},
		WS: wsx.Options{
			CheckURL: opts.Allow,
			Trace: func(step, rawURL string, h http.Header) {
				mu.Lock()
				wsSeen[step] = &httpx.Trace{Step: step, URL: rawURL, ReqHeader: h}
				mu.Unlock()
			},
		},
	}
	ex, closeEx, err := executor.New(tsc, xopts)
	if err != nil {
		return finish(append(pre, model.TryHint{Level: "error", Message: "The scenario cannot be executed: " + err.Error()}))
	}
	defer closeEx()

	byName := make(map[string]*entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	sink := func(r protocol.Result) {
		mu.Lock()
		defer mu.Unlock()
		if e := byName[r.Step]; e != nil && !e.reported {
			e.reported = true
			e.Status = r.Status
			e.DurationMs = ms(r.Latency)
			e.Bytes = r.Bytes
			e.err = r.Err
			e.State = model.TryPassed
			if r.Err != nil {
				e.State = model.TryFailed
				e.Error = r.Err.Error()
			}
			if e.Kind == "ws" {
				e.trace = wsSeen[e.Name]
			} else {
				for i := len(pending) - 1; i >= 0; i-- {
					if pending[i].Step == r.Step {
						e.trace = pending[i]
						pending = slices.Delete(pending, i, i+1)
						break
					}
				}
			}
			return
		}
		// A ws message / hold result: "<step>:<n>" or "<step>:hold".
		for _, e := range entries {
			if e.Kind == "ws" && strings.HasPrefix(r.Step, e.Name+":") {
				m := model.TryMessage{Name: r.Step, DurationMs: ms(r.Latency)}
				if r.Err != nil {
					m.Error = r.Err.Error()
					e.State = model.TryFailed
					e.Error = r.Step + ": " + r.Err.Error()
					e.err = r.Err
				}
				e.Messages = append(e.Messages, m)
				return
			}
		}
	}

	ictx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	vars := maps.Clone(opts.Vars)
	if vars == nil {
		vars = map[string]string{}
	}
	ex.Iterate(ictx, vars, time.Now(), sink)

	var timedOut bool
	if errors.Is(ictx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		mu.Lock()
		for _, e := range entries {
			if !e.reported {
				e.reported, timedOut = true, true
				e.State = model.TryFailed
				e.Error = fmt.Sprintf("did not complete within the %s trial time limit", opts.Timeout)
				break
			}
		}
		mu.Unlock()
	}

	mu.Lock()
	defer mu.Unlock()
	for _, e := range entries {
		if e.trace != nil {
			fillDetails(e, opts.BodyLimit)
		}
	}
	hs := append(pre, hints(sc, entries, opts)...)
	if timedOut {
		hs = append(hs, model.TryHint{Level: "error",
			Message: fmt.Sprintf("The trial was stopped after %s; the target is very slow or not answering.", opts.Timeout)})
	}
	return finish(hs)
}

// trialScenario returns a copy of sc shortened for a trial: at most
// MaxSteps steps, no think times, no ws hold/ping, and a target timeout of
// at most the trial timeout.
func trialScenario(sc *scenario.Scenario, opts Options) *scenario.Scenario {
	t := *sc
	n := min(len(sc.Steps), opts.MaxSteps)
	t.Steps = make([]scenario.Step, n)
	for i := 0; i < n; i++ {
		st := sc.Steps[i]
		st.ThinkTime = scenario.Duration{}
		if st.WS != nil {
			ws := *st.WS
			ws.Hold, ws.PingEvery, ws.PingMessage = scenario.Duration{}, scenario.Duration{}, ""
			st.WS = &ws
		}
		t.Steps[i] = st
	}
	if t.Target.Timeout.Duration <= 0 || t.Target.Timeout.Duration > opts.Timeout {
		t.Target.Timeout.Duration = opts.Timeout
	}
	return &t
}

// newEntries lists the expected results: auth:login (type login) and then
// every step, all not_run.
func newEntries(sc *scenario.Scenario) []*entry {
	var out []*entry
	if a := sc.Auth; a != nil && a.Type == scenario.AuthLogin && a.Login != nil {
		m := a.Login.Method
		if m == "" {
			m = scenario.DefaultLoginMethod
		}
		out = append(out, &entry{TryStep: model.TryStep{
			Name: scenario.AuthLoginStep, Kind: "login", Index: -1, Method: m,
			URL: strings.TrimRight(sc.Target.BaseURL, "/") + a.Login.Path,
		}})
	}
	base := strings.TrimRight(sc.Target.BaseURL, "/")
	for i, st := range sc.Steps {
		e := &entry{TryStep: model.TryStep{Name: st.Name, Kind: "http", Index: i, Method: st.Method, URL: st.Path}}
		if !isAbs(st.Path) {
			e.URL = base + st.Path // as written; replaced by the rendered URL once sent
		}
		if st.IsWS() {
			e.Kind, e.Method = "ws", ""
		}
		out = append(out, e)
	}
	for _, e := range out {
		e.URL = redactURL(e.URL)
		e.State = model.TryNotRun
		e.RequestHeaders = []model.TryHeader{}
		e.ResponseHeaders = []model.TryHeader{}
		e.Extracted = []model.TryExtract{}
		e.Assertions = []model.TryAssertion{}
	}
	return out
}

// fillDetails copies the (redacted, capped) trace into the report.
func fillDetails(e *entry, bodyLimit int) {
	t := e.trace
	e.URL = redactURL(t.URL)
	e.RequestHeaders = requestHeaders(t.ReqHeader, t.Host)
	if t.ReqBody != "" {
		b, trunc := capText(t.ReqBody, bodyLimit)
		e.RequestBody = redactBody(b)
		if trunc {
			e.RequestBody += "…"
		}
	}
	if !t.Sent {
		return
	}
	e.Location = t.Location
	e.ResponseHeaders = responseHeaders(t.RespHeader)
	if len(t.Body) > 0 {
		if isBinary(t.RespHeader.Get("Content-Type"), t.Body) {
			e.BodyBinary = true
		} else {
			b, trunc := capText(string(t.Body), bodyLimit)
			e.Body = redactBody(b)
			e.BodyTruncated = trunc || t.BodyTruncated
		}
	}
	hasStatus := false
	for _, c := range t.Checks {
		hasStatus = hasStatus || strings.HasPrefix(c.Desc, "status ")
	}
	if !hasStatus {
		// The executor's default rule when no status assertion exists.
		e.Assertions = append(e.Assertions, model.TryAssertion{
			Assertion: "status lt 400 (default without a status assertion)",
			Passed:    t.Status < 400,
			Message:   failMsg(t.Status >= 400, fmt.Sprintf("status %d", t.Status)),
		})
	}
	for _, c := range t.Checks {
		e.Assertions = append(e.Assertions, model.TryAssertion{Assertion: c.Desc, Passed: c.Pass, Message: c.Msg})
	}
	for _, x := range t.Extracts {
		e.Extracted = append(e.Extracted, model.TryExtract{
			Var: x.Var, Source: x.Source, Expr: x.Expr, OK: x.OK, Error: x.Msg,
			Value: maskVar(x.Var, x.Value),
		})
	}
}

func failMsg(failed bool, msg string) string {
	if failed {
		return msg
	}
	return ""
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// hostOf returns the host name of a raw URL (or the URL itself).
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return raw
}
