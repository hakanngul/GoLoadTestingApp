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
// `status lt 600` to accept any status.
//
// # Redirects
//
// Like Locust, JMeter and browsers, a step follows redirects (301, 302,
// 303, 307, 308) by default and reports the FINAL response: status,
// body, headers for assertions and extracts; latency and the target
// timeout cover the whole chain. 303, and 301/302 after anything but
// GET/HEAD, continue as GET without the body; 307/308 repeat method and
// body. At most target.max_redirects hops (default 10) are followed; a
// chain that repeats a request it already made is reported as a redirect
// loop right away. follow_redirects: false (step or target) reports the
// 3xx itself, with its target appended to error messages.
//
// Every hop that leaves the host of the request being redirected is
// vetted, see Options.CheckRedirect. Authorization, Proxy-Authorization,
// explicit Cookie headers, Options.SensitiveHeaders and a Host override
// are only ever sent to the host of the step's first request.
//
// # Cookies
//
// Unless target.cookies is false, every request carries the cookies of
// the virtual user's protocol.Session (attached to ctx by the engine, or
// created per iteration), and Set-Cookie of every response, including
// each redirect hop, is stored there. The jar is deliberately NOT on the
// shared http.Client: that would share cookies between all virtual
// users. An explicit Cookie header is merged with the jar; the explicit
// cookie wins on a name clash.
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
	"slices"
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

	// CheckURL, if set, vets every rendered request URL before it is sent;
	// an error fails the step without sending. Trial runs use it to apply
	// the target allowlist to templated URLs.
	CheckURL func(rawURL string) error
	// CheckRedirect, if set, vets every redirect hop that leaves the host
	// of the request being redirected (the server's target allowlist); an
	// error fails the step with "redirect to <url> not allowed". Hops that
	// stay on the same host (any scheme or port, as the allowlist ignores
	// both) are always followed.
	CheckRedirect func(rawURL string) error
	// RestrictRedirects applies when CheckRedirect is nil: redirects to
	// another host are then followed only if that host is one of the
	// scenario's own targets (base_url or an absolute step URL). Cluster
	// workers, which have no allowlist of their own, use it so that a
	// redirect cannot take them to a host the master did not vet. With
	// neither option set every redirect is followed (CLI runs).
	RestrictRedirects bool
	// SensitiveHeaders lists extra request headers (besides Authorization,
	// Proxy-Authorization and Cookie) that must not follow a redirect to
	// another host, e.g. a custom auth header.
	SensitiveHeaders []string
	// Trace, if set, is called synchronously with a detailed record of
	// every executed step, just before its Result is reported. Meant for
	// single trial iterations (it buffers every body); leave nil in load
	// runs.
	Trace func(*Trace)
}

// Executor runs scenario iterations over HTTP. It is safe for concurrent
// use by many goroutines.
type Executor struct {
	client  *http.Client
	base    string // base URL without trailing slash
	timeout time.Duration
	steps   []step

	checkURL func(string) error
	trace    func(*Trace)

	cookies       bool // keep cookies in the VU session
	maxRedirects  int  // hops followed per step
	checkRedirect func(string) error
	restrict      bool            // no checkRedirect: own hosts only
	hosts         map[string]bool // the scenario's own target hosts
	sensitive     map[string]bool // canonical names, never sent cross-host
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
	follow    bool // follow redirects
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

		checkURL: opts.CheckURL,
		trace:    opts.Trace,

		cookies:       sc.Target.CookiesEnabled(),
		maxRedirects:  sc.Target.MaxRedirects,
		checkRedirect: opts.CheckRedirect,
		restrict:      opts.RestrictRedirects,
		hosts:         map[string]bool{},
		sensitive: map[string]bool{
			"Authorization": true, "Proxy-Authorization": true, "Cookie": true,
		},
	}
	if e.timeout <= 0 {
		e.timeout = DefaultTimeout
	}
	if e.maxRedirects <= 0 {
		e.maxRedirects = scenario.DefaultMaxRedirects
	}
	for _, h := range opts.SensitiveHeaders {
		e.sensitive[http.CanonicalHeaderKey(strings.TrimSpace(h))] = true
	}
	for _, raw := range sc.TargetURLs() {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			e.hosts[hostKey(u)] = true
		}
	}
	for i, s := range sc.Steps {
		st, err := compileStep(s, sc.Target.Headers, base)
		if err != nil {
			return nil, fmt.Errorf("httpx: step %d (%s): %w", i+1, st.name, err)
		}
		st.follow = sc.Target.FollowsRedirects()
		if s.FollowRedirects != nil {
			st.follow = *s.FollowRedirects
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
		// Redirects are followed by do itself (per-VU cookies, allowlist
		// and tracing per hop), never by the client.
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
	if _, ok := merged["User-Agent"]; !ok {
		merged["User-Agent"] = protocol.UserAgent()
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
	if e.cookies {
		// No session attached (open model): this iteration is a new
		// visitor with an empty jar.
		ctx, _ = protocol.EnsureSession(ctx)
	}
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

// NumSteps reports how many steps the executor was built with.
func (e *Executor) NumSteps() int { return len(e.steps) }

// Step executes only step i (0-based, in the order of the scenario passed
// to New) once and returns its result. It exists for composite executors
// (see package protocol/executor) that interleave HTTP steps with other
// protocols; most callers want Iterate.
//
// Cookies come from and go to the protocol.Session attached to ctx; the
// caller attaches one per iteration or virtual user (without one, cookies
// do not outlive the step).
//
// Latency is measured from start, so the caller applies the coordinated
// omission rule. Step neither sleeps the think time nor modifies vars: on
// success, extracted holds alternating variable name / value pairs for the
// caller to apply. If ctx is cancelled the result should be discarded.
func (e *Executor) Step(ctx context.Context, i int, vars map[string]string, start time.Time) (res protocol.Result, extracted []string) {
	return e.do(ctx, &e.steps[i], vars, start)
}

// do executes one step, following redirects if the step says so (see the
// package documentation). extracted holds alternating var name / value
// pairs, applied by the caller only if the step succeeded.
//
// A failure of a 3xx response that was not followed mentions where it
// redirects to, e.g. "assert status: got 301, want eq 200 (redirects to
// https://host/)".
func (e *Executor) do(ctx context.Context, st *step, vars map[string]string, start time.Time) (res protocol.Result, extracted []string) {
	res.Step = st.name

	var tr *Trace
	if e.trace != nil {
		tr = &Trace{Step: st.name, Method: st.method}
		defer func() {
			tr.Latency, tr.Err = res.Latency, res.Err
			e.trace(tr)
		}()
	}

	var sess *protocol.Session
	if e.cookies {
		if sess = protocol.SessionFrom(ctx); sess == nil {
			sess = protocol.NewSession() // lives for this step's redirects only
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	req, err := e.newRequest(reqCtx, st, vars)
	if err != nil {
		res.Err = fmt.Errorf("build request: %w", err)
		res.Latency = time.Since(start)
		return res, nil
	}
	explicitCookie := req.Header.Get("Cookie")
	if e.cookies {
		setCookieHeader(req, sess, explicitCookie)
	}
	if tr != nil {
		tr.URL, tr.Host, tr.ReqHeader = req.URL.String(), req.Host, req.Header.Clone()
		if st.body != "" {
			tr.ReqBody = scenario.Render(st.body, vars)
		}
	}
	if e.checkURL != nil {
		if err := e.checkURL(req.URL.String()); err != nil {
			res.Err = fmt.Errorf("build request: %w", err)
			res.Latency = time.Since(start)
			return res, nil
		}
	}

	// Send, following redirects. resp is the final response: the first
	// non-redirect, or the redirect that could not be followed (redirErr).
	var (
		resp     *http.Response
		redirErr error
		chain    redirChain
		first    = req
	)
	for {
		resp, err = e.client.Do(req)
		if err != nil {
			// (The error names the URL, also that of a redirect hop.)
			res.Latency = time.Since(start)
			res.Err = e.transportErr("send", reqCtx, err)
			if tr != nil && req != first {
				tr.FinalURL = req.URL.String()
			}
			return res, nil
		}
		sess.StoreResponse(req.URL, resp.Header)
		if !st.follow || !isRedirect(resp.StatusCode) {
			break
		}
		next, err := e.nextHop(reqCtx, st, vars, first, req, resp, sess, explicitCookie, &chain)
		if err != nil {
			redirErr = err
			break
		}
		if next == nil {
			break // a 3xx without Location is a final response
		}
		if tr != nil {
			tr.Hops = append(tr.Hops, HopTrace{
				Method:      req.Method,
				URL:         req.URL.String(),
				Status:      resp.StatusCode,
				Location:    next.URL.String(),
				CookiesSent: cookieHeaderNames(req.Header.Get("Cookie")),
				CookiesSet:  setCookieNames(resp),
			})
		}
		// Drain a small body so the connection can be reused, as
		// net/http does; a large one costs less to close.
		_, _ = io.CopyN(io.Discard, resp.Body, 2<<10)
		resp.Body.Close()
		req = next
	}
	res.Status = resp.StatusCode
	if tr != nil {
		tr.Sent, tr.Status, tr.RespHeader = true, resp.StatusCode, resp.Header.Clone()
		tr.CookiesSent = cookieHeaderNames(req.Header.Get("Cookie"))
		tr.CookiesSet = setCookieNames(resp)
		if req != first {
			tr.FinalURL = req.URL.String()
		}
	}

	// Read the body completely: needed for time-to-last-byte latency and
	// for connection reuse.
	var (
		buf   *bytes.Buffer
		n     int64
		trunc bool
	)
	if st.needBody || tr != nil {
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
	if tr != nil {
		st.traceResponse(tr, &r, n)
		tr.Location = redirectTarget(resp)
	}
	if redirErr != nil {
		res.Err = redirErr
		return res, nil
	}

	if !st.hasStatus && resp.StatusCode >= 400 {
		res.Err = fmt.Errorf("status %d", resp.StatusCode)
		return res, nil
	}
	for i := range st.checks {
		if err := st.checks[i].run(&r); err != nil {
			res.Err = withRedirect(err, resp)
			return res, nil
		}
	}
	if len(st.extracts) > 0 {
		extracted = make([]string, 0, 2*len(st.extracts))
		for i := range st.extracts {
			v, err := st.extracts[i].run(&r)
			if err != nil {
				res.Err = withRedirect(err, resp)
				return res, nil
			}
			extracted = append(extracted, st.extracts[i].varName, v)
		}
	}
	return res, extracted
}

// withRedirect appends the redirect target of a 3xx response to err.
func withRedirect(err error, resp *http.Response) error {
	if loc := redirectTarget(resp); loc != "" {
		return fmt.Errorf("%w (redirects to %s)", err, loc)
	}
	return err
}

// isRedirect reports whether status is a redirect that is followed.
func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// RedirectError is the error of a step whose redirect was not followed
// because the hop is not allowed. Err is the cause (e.g. the allowlist's
// error), if any.
type RedirectError struct {
	URL    string // the refused hop
	Reason string
	Err    error
}

func (e *RedirectError) Error() string {
	return "redirect to " + e.URL + " not allowed (" + e.Reason + ")"
}

func (e *RedirectError) Unwrap() error { return e.Err }

// hostKey is a URL's host name as compared by the redirect rules:
// lower-case, without port or trailing dot (like package allowlist).
func hostKey(u *url.URL) string {
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

// redirectAllowed decides whether a hop from `from` to `to` may be sent:
//
//  1. only http(s) URLs;
//  2. the same host as the request being redirected: always (it is not a
//     new target; scheme and port may change, e.g. http -> https);
//  3. another host: Options.CheckRedirect (the allowlist) decides if set;
//     otherwise, with Options.RestrictRedirects, only the scenario's own
//     target hosts; otherwise any host.
func (e *Executor) redirectAllowed(from, to *url.URL) error {
	if (to.Scheme != "http" && to.Scheme != "https") || to.Hostname() == "" {
		return &RedirectError{URL: capURL(to.String()), Reason: "not an http(s) URL"}
	}
	host := hostKey(to)
	if host == hostKey(from) {
		return nil
	}
	switch {
	case e.checkRedirect != nil:
		if err := e.checkRedirect(to.String()); err != nil {
			return &RedirectError{URL: capURL(to.String()), Reason: "host not in allowlist", Err: err}
		}
	case e.restrict && !e.hosts[host]:
		return &RedirectError{URL: capURL(to.String()),
			Reason: "host is not a target of this scenario; workers only follow redirects to the scenario's own hosts"}
	}
	return nil
}

// redirChain is the state of one step's redirect chain.
type redirChain struct {
	hops   int
	urls   []*url.URL // every request URL so far, for messages
	seen   []string   // request keys already sent (loop detection)
	noBody bool       // a hop turned the request into a body-less GET
}

// describe renders the chain for error messages: paths for the first
// request's origin, full URLs elsewhere, long chains shortened.
func (c *redirChain) describe() string {
	origin := c.urls[0]
	parts := make([]string, 0, len(c.urls))
	for _, u := range c.urls {
		if u.Scheme == origin.Scheme && u.Host == origin.Host {
			parts = append(parts, capURL(u.RequestURI()))
		} else {
			parts = append(parts, capURL(u.String()))
		}
	}
	if len(parts) > 6 {
		parts = append(append(parts[:3:3], "…"), parts[len(parts)-2:]...)
	}
	return strings.Join(parts, " → ")
}

// requestKey identifies a request for loop detection: a server asked the
// same thing with the same cookies answers the same.
func requestKey(r *http.Request) string {
	return r.Method + " " + r.URL.String() + "\x00" + r.Header.Get("Cookie")
}

// nextHop builds the request that follows redirect response resp of req.
// It returns (nil, nil) if resp has no Location (resp is then the final
// response) and an error if the redirect must not or cannot be followed.
func (e *Executor) nextHop(ctx context.Context, st *step, vars map[string]string, first, req *http.Request, resp *http.Response, sess *protocol.Session, explicitCookie string, c *redirChain) (*http.Request, error) {
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil, nil
	}
	u, err := req.URL.Parse(loc)
	if err != nil {
		return nil, fmt.Errorf("redirect: invalid Location %q", capURL(loc))
	}
	u.Fragment = ""
	if len(c.urls) == 0 {
		c.urls = append(c.urls, first.URL)
		c.seen = append(c.seen, requestKey(first))
	}
	if err := e.redirectAllowed(req.URL, u); err != nil {
		return nil, err
	}
	c.urls = append(c.urls, u)

	// Method and body, as browsers and net/http do.
	method := req.Method
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther:
		if method != http.MethodGet && method != http.MethodHead {
			method = http.MethodGet
		}
		c.noBody = true
	}
	var body io.Reader
	if st.body != "" && !c.noBody {
		b := st.body
		if st.bodyTmpl {
			b = scenario.Render(b, vars)
		}
		body = strings.NewReader(b)
	}
	next, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("redirect: %w", err)
	}

	// Headers: those of the first request. Credentials stay with the host
	// they were written for (and are not downgraded to plain http).
	h := first.Header.Clone()
	delete(h, "Cookie") // holds the jar's cookies for the first URL
	if explicitCookie != "" {
		h["Cookie"] = []string{explicitCookie}
	}
	if hostKey(u) != hostKey(first.URL) || (first.URL.Scheme == "https" && u.Scheme != "https") {
		for k := range e.sensitive {
			delete(h, k)
		}
		explicitCookie = ""
	} else if st.host != "" {
		next.Host = first.Host
	}
	if c.noBody {
		for _, k := range [...]string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Language", "Content-Location"} {
			delete(h, k)
		}
	}
	next.Header = h
	if e.cookies {
		setCookieHeader(next, sess, explicitCookie)
	}

	if key := requestKey(next); slices.Contains(c.seen, key) {
		return nil, errors.New("redirect loop: " + c.describe() + e.cookieHint())
	} else if c.hops >= e.maxRedirects {
		return nil, fmt.Errorf("too many redirects (stopped after %d, target.max_redirects): %s%s", c.hops, c.describe(), e.cookieHint())
	} else {
		c.seen = append(c.seen, key)
	}
	c.hops++
	return next, nil
}

// cookieHint is appended to redirect loop errors when cookies are off:
// sites that redirect until a cookie comes back are the usual cause.
func (e *Executor) cookieHint() string {
	if e.cookies {
		return ""
	}
	return " (the server may require cookies, which target.cookies: false disables)"
}

// setCookieHeader sets req's Cookie header to the session's cookies for
// its URL merged with explicit (the scenario's own Cookie header value).
func setCookieHeader(req *http.Request, sess *protocol.Session, explicit string) {
	if v := sess.CookieHeader(req.URL, explicit); v != "" {
		req.Header["Cookie"] = []string{v}
	}
}

// capURL shortens a URL for error messages.
func capURL(s string) string {
	if len(s) > maxLocationLen {
		s = s[:maxLocationLen] + "..."
	}
	return s
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
	h := make(http.Header, len(st.headers)+1) // +1: Cookie
	for _, hd := range st.headers {
		v := hd.value
		if hd.tmpl {
			v = scenario.Render(v, vars)
		}
		h[hd.key] = []string{v}
	}
	req.Header = h
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
