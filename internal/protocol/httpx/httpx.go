// Package httpx implements protocol.Executor over HTTP/1.1 and HTTP/2
// using net/http.
//
// Everything that does not depend on per-iteration variables (URL parsing,
// header merging, regex compilation, assertion values) is done once in
// New, so Iterate only renders templates, sends, reads and checks.
//
// # Measurement
//
// A step's latency runs until the response body has been read completely
// (time-to-last-byte), not just until headers arrive. The first step of an
// iteration is measured from the scheduled `intended` time (coordinated
// omission correction, see protocol.Executor); later steps are measured
// from just before their own request is sent. The duration assertion
// compares against this same reported latency.
//
// # Success
//
// A step fails (Result.Err != nil) on a transport error, a failed
// assertion, or a failed extraction. If a step declares no status
// assertion, a response status >= 400 also fails it; declare e.g.
// `status lt 600` to accept any status. Redirects are never followed: the
// 3xx response is what gets measured and checked.
package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

const (
	// DefaultMaxConnsPerHost is used when Options.MaxConnsPerHost is 0.
	DefaultMaxConnsPerHost = 1000
	// DefaultTimeout applies when the scenario's Target.Timeout is zero.
	DefaultTimeout = 30 * time.Second

	// maxBodyRead caps how much of a body is buffered for extract/assert.
	// Bytes past the cap are still read (and counted) but discarded.
	maxBodyRead = 10 << 20
	// maxPooledBuf keeps oversized buffers from pinning memory in the pool.
	maxPooledBuf = 1 << 20
)

// Options tunes the HTTP transport.
type Options struct {
	// MaxConnsPerHost caps connections (idle + active) per host.
	// 0 means DefaultMaxConnsPerHost; negative means unlimited.
	MaxConnsPerHost int
	// DisableKeepAlives opens a new connection for every request, to
	// measure connection setup cost or defeat sticky load balancing.
	DisableKeepAlives bool
}

// Executor runs scenario iterations over HTTP. It is safe for concurrent
// use by many goroutines.
type Executor struct {
	client  *http.Client
	base    string // base URL without trailing slash
	timeout time.Duration
	steps   []step
}

var _ protocol.Executor = (*Executor)(nil)

type header struct {
	key   string // canonical
	value string
	tmpl  bool // value contains {{
}

// step is the precompiled form of a scenario.Step.
type step struct {
	name      string
	method    string
	path      string
	pathTmpl  bool
	absURL    bool // path is a full http(s) URL; base is not prepended
	headers   []header
	host      string // explicit Host header, applied to req.Host
	hostTmpl  bool
	body      string
	bodyTmpl  bool
	think     time.Duration
	checks    []check
	extracts  []extractor
	hasStatus bool // an explicit status assertion exists
	needBody  bool // body must be buffered
}

// New validates sc and builds an Executor with one shared, load-test-tuned
// http.Client. Errors describe the offending step.
func New(sc *scenario.Scenario, opts Options) (*Executor, error) {
	if sc == nil {
		return nil, errors.New("httpx: nil scenario")
	}
	base := strings.TrimSpace(sc.Target.BaseURL)
	if base != "" {
		u, err := url.Parse(base)
		if err != nil {
			return nil, fmt.Errorf("httpx: target base_url: %w", err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("httpx: target base_url %q: want http(s)://host", base)
		}
		base = strings.TrimRight(base, "/")
	}
	if len(sc.Steps) == 0 {
		return nil, errors.New("httpx: scenario has no steps")
	}

	e := &Executor{
		base:    base,
		timeout: sc.Target.Timeout.Duration,
		steps:   make([]step, len(sc.Steps)),
	}
	if e.timeout <= 0 {
		e.timeout = DefaultTimeout
	}
	for i, s := range sc.Steps {
		st, err := compileStep(s, sc.Target.Headers, base)
		if err != nil {
			return nil, fmt.Errorf("httpx: step %d (%s): %w", i+1, st.name, err)
		}
		e.steps[i] = st
	}

	maxConns := opts.MaxConnsPerHost
	switch {
	case maxConns == 0:
		maxConns = DefaultMaxConnsPerHost
	case maxConns < 0:
		maxConns = 0 // unlimited for http.Transport
	}
	idle := maxConns
	if idle == 0 {
		idle = DefaultMaxConnsPerHost
	}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   e.timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       maxConns,
		MaxIdleConns:          idle,
		MaxIdleConnsPerHost:   idle,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     opts.DisableKeepAlives,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: sc.Target.InsecureSkipVerify, //nolint:gosec // opt-in per scenario
		},
	}
	e.client = &http.Client{
		Transport: tr,
		// A load tester reports what the server returned; never follow.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		// No Client.Timeout: the per-request context deadline covers the
		// whole exchange including the body read.
	}
	return e, nil
}

func compileStep(s scenario.Step, targetHeaders map[string]string, base string) (step, error) {
	st := step{
		method: strings.ToUpper(strings.TrimSpace(s.Method)),
		path:   s.Path,
		body:   s.Body,
		think:  s.ThinkTime.Duration,
	}
	if st.method == "" {
		st.method = http.MethodGet
	}
	st.name = s.Name
	if st.name == "" {
		st.name = st.method + " " + s.Path
	}
	st.pathTmpl = strings.Contains(st.path, "{{")
	st.bodyTmpl = strings.Contains(st.body, "{{")
	lp := strings.ToLower(st.path)
	st.absURL = strings.HasPrefix(lp, "http://") || strings.HasPrefix(lp, "https://")
	if !st.absURL && base == "" {
		return st, errors.New("relative path needs target base_url")
	}
	if !st.absURL && st.path != "" && !strings.HasPrefix(st.path, "/") && !strings.HasPrefix(st.path, "?") {
		st.path = "/" + st.path
	}

	// Merge headers: step overrides target, case-insensitively.
	merged := make(map[string]string, len(targetHeaders)+len(s.Headers))
	for k, v := range targetHeaders {
		merged[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range s.Headers {
		merged[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range merged {
		if k == "Host" {
			st.host, st.hostTmpl = v, strings.Contains(v, "{{")
			continue
		}
		st.headers = append(st.headers, header{key: k, value: v, tmpl: strings.Contains(v, "{{")})
	}

	for _, a := range s.Assert {
		c, err := compileCheck(a)
		if err != nil {
			return st, fmt.Errorf("assert %s: %w", a.Type, err)
		}
		st.hasStatus = st.hasStatus || c.typ == scenario.AssertStatus
		st.needBody = st.needBody || c.needsBody()
		st.checks = append(st.checks, c)
	}
	for _, x := range s.Extract {
		ex, err := compileExtract(x)
		if err != nil {
			return st, err
		}
		st.needBody = st.needBody || ex.needsBody()
		st.extracts = append(st.extracts, ex)
	}
	return st, nil
}

// Close releases idle connections held by the shared transport.
func (e *Executor) Close() { e.client.CloseIdleConnections() }

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// Iterate implements protocol.Executor. See the package documentation for
// how latency is measured. Results are not reported for work interrupted
// by cancellation of ctx.
func (e *Executor) Iterate(ctx context.Context, vars map[string]string, intended time.Time, sink func(protocol.Result)) {
	for i := range e.steps {
		if ctx.Err() != nil {
			return
		}
		st := &e.steps[i]
		start := time.Now()
		if i == 0 && !intended.IsZero() && intended.Before(start) {
			start = intended
		}
		res, extracted := e.do(ctx, st, vars, start)
		if ctx.Err() != nil {
			return // run cancelled: not a failure of the target
		}
		sink(res)
		if res.Err != nil {
			return
		}
		if len(extracted) > 0 {
			if vars == nil {
				vars = make(map[string]string, len(extracted)/2)
			}
			for j := 0; j < len(extracted); j += 2 {
				vars[extracted[j]] = extracted[j+1]
			}
		}
		if st.think > 0 && !sleep(ctx, st.think) {
			return
		}
	}
}

// do executes one step. extracted holds alternating var name / value
// pairs, applied by the caller only if the step succeeded.
func (e *Executor) do(ctx context.Context, st *step, vars map[string]string, start time.Time) (protocol.Result, []string) {
	res := protocol.Result{Step: st.name}

	reqCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	req, err := e.newRequest(reqCtx, st, vars)
	if err != nil {
		res.Err = fmt.Errorf("build request: %w", err)
		res.Latency = time.Since(start)
		return res, nil
	}

	resp, err := e.client.Do(req)
	if err != nil {
		res.Latency = time.Since(start)
		res.Err = e.transportErr("send", reqCtx, err)
		return res, nil
	}
	res.Status = resp.StatusCode

	// Read the body completely: needed for time-to-last-byte latency and
	// for connection reuse.
	var (
		buf   *bytes.Buffer
		n     int64
		trunc bool
	)
	if st.needBody {
		buf = bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer func() {
			if buf.Cap() <= maxPooledBuf {
				bufPool.Put(buf)
			}
		}()
		n, err = io.Copy(buf, io.LimitReader(resp.Body, maxBodyRead))
		if err == nil && n == maxBodyRead {
			var rest int64
			rest, err = io.Copy(io.Discard, resp.Body)
			n += rest
			trunc = rest > 0
		}
	} else {
		n, err = io.Copy(io.Discard, resp.Body)
	}
	resp.Body.Close()
	res.Latency = time.Since(start)
	res.Bytes = n
	if err != nil {
		res.Err = e.transportErr("read body", reqCtx, err)
		return res, nil
	}

	r := response{status: resp.StatusCode, header: resp.Header, trunc: trunc, latency: res.Latency}
	if buf != nil {
		r.body = buf.Bytes()
	}

	if !st.hasStatus && resp.StatusCode >= 400 {
		res.Err = fmt.Errorf("status %d", resp.StatusCode)
		return res, nil
	}
	for i := range st.checks {
		if err := st.checks[i].run(&r); err != nil {
			res.Err = err
			return res, nil
		}
	}
	var extracted []string
	if len(st.extracts) > 0 {
		extracted = make([]string, 0, 2*len(st.extracts))
		for i := range st.extracts {
			v, err := st.extracts[i].run(&r)
			if err != nil {
				res.Err = err
				return res, nil
			}
			extracted = append(extracted, st.extracts[i].varName, v)
		}
	}
	return res, extracted
}

func (e *Executor) newRequest(ctx context.Context, st *step, vars map[string]string) (*http.Request, error) {
	path := st.path
	if st.pathTmpl {
		path = scenario.Render(path, vars)
	}
	target := path
	if !st.absURL {
		target = e.base + path
	}
	var body io.Reader
	if st.body != "" {
		b := st.body
		if st.bodyTmpl {
			b = scenario.Render(b, vars)
		}
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, st.method, target, body)
	if err != nil {
		return nil, err
	}
	if len(st.headers) > 0 {
		h := make(http.Header, len(st.headers))
		for _, hd := range st.headers {
			v := hd.value
			if hd.tmpl {
				v = scenario.Render(v, vars)
			}
			h[hd.key] = []string{v}
		}
		req.Header = h
	}
	if st.host != "" {
		req.Host = st.host
		if st.hostTmpl {
			req.Host = scenario.Render(st.host, vars)
		}
	}
	return req, nil
}

// transportErr labels request-timeout errors clearly. Cancellation of the
// run context is filtered out by Iterate before results are reported.
func (e *Executor) transportErr(phase string, reqCtx context.Context, err error) error {
	if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s: timeout after %s: %w", phase, e.timeout, err)
	}
	return fmt.Errorf("%s: %w", phase, err)
}

// sleep waits for d or until ctx is done; it reports whether d elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
