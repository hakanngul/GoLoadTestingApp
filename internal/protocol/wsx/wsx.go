// Package wsx implements WebSocket steps (scenario.Step with type ws) on
// top of github.com/coder/websocket.
//
// A ws step runs in three phases on one connection:
//
//  1. Connect: the WebSocket handshake to Step.Path resolved against the
//     target base URL (http->ws, https->wss).
//  2. Exchange: Step.WS.Messages in order. A message may send a text frame,
//     wait for a received message containing Expect, or both.
//  3. Hold: the connection stays open for Step.WS.Hold, optionally sending
//     PingMessage every PingEvery, and is then closed normally.
//
// # Results
//
// One ws step reports several protocol.Results:
//
//   - Step "<name>": the handshake. Latency runs from the step start (from
//     the scheduled `intended` time if this is the first step of the
//     iteration, per the coordinated omission rule) until the upgrade
//     response has been received. Status is 101, or the HTTP status of a
//     rejected handshake (0 on transport errors).
//   - Step "<name>:<n>" for every message n (1-based position in
//     ws.messages) that has an Expect: latency from just before the send
//     (or from the start of the wait for an expect-only message) until a
//     matching message arrived. Bytes counts every message read meanwhile.
//     A send-only message reports a Result only if the send fails.
//   - Step "<name>:hold": reported only on failure, i.e. when the
//     connection ends before Hold elapsed (server close, network error,
//     failed ping). Holding time itself is never reported as latency.
//
// As with HTTP steps, the first failed Result ends the iteration. Work
// interrupted by cancellation of the iteration context (including during
// the hold) is not reported at all.
//
// # Memory
//
// The design target is tens of thousands of concurrently held connections
// per process. Each held connection costs its virtual user's goroutine,
// the library's connection state (two 4 KiB bufio buffers) and nothing
// else: the hold phase reads and discards incoming frames on the VU
// goroutine itself (answering pings and noticing closes), and the hold
// deadline, pings and context cancellation are driven by runtime timers
// whose callbacks run only briefly. Messages are only buffered while an
// Expect is pending, through a shared pool, capped at MaxMessageBytes.
package wsx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/jsonpath"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

const (
	// DefaultTimeout applies when the scenario's Target.Timeout is zero.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxMessageBytes is used when Options.MaxMessageBytes is 0.
	DefaultMaxMessageBytes = 1 << 20

	// StatusSwitchingProtocols is reported for successful ws Results.
	StatusSwitchingProtocols = http.StatusSwitchingProtocols

	// maxPooledBuf keeps oversized buffers from pinning memory in the pool.
	maxPooledBuf = 64 << 10
)

// Options tunes the WebSocket executor.
type Options struct {
	// MaxMessageBytes caps how much of one received message is buffered
	// while matching an Expect (and for extract). Longer messages are read
	// completely and counted, but only the first MaxMessageBytes are
	// searched. 0 means DefaultMaxMessageBytes.
	MaxMessageBytes int

	// CheckURL, if set, vets every rendered connection URL before the
	// handshake; an error fails the step without connecting (trial runs).
	CheckURL func(rawURL string) error
	// Trace, if set, is called with the rendered URL and handshake headers
	// of every ws step just before it connects (trial runs only).
	Trace func(step, rawURL string, header http.Header)
}

// Executor runs ws steps. It is safe for concurrent use by many
// goroutines.
type Executor struct {
	client  *http.Client
	base    string // ws(s)://host[/prefix] without trailing slash; may be ""
	timeout time.Duration
	maxMsg  int
	steps   []step

	checkURL func(string) error
	trace    func(string, string, http.Header)
}

var _ protocol.Executor = (*Executor)(nil)

type header struct {
	key   string // canonical
	value string
	tmpl  bool
}

// step is the precompiled form of a ws scenario.Step.
type step struct {
	name      string
	holdLabel string // name + ":hold"
	path      string
	pathTmpl  bool
	headers   []header
	host      string
	hostTmpl  bool
	think     time.Duration

	msgs       []message
	lastExpect int // index of the last message with an Expect, -1 if none
	extracts   []extractor

	hold      time.Duration
	pingEvery time.Duration
	pingMsg   string // empty: protocol-level ping frame
	pingTmpl  bool
}

type message struct {
	label      string // name + ":" + 1-based index
	send       string
	sendTmpl   bool
	expect     string
	expectTmpl bool
	timeout    time.Duration
}

type extractor struct {
	varName string
	from    scenario.ExtractSource
	expr    string
	path    jsonpath.Path
	re      *regexp.Regexp
}

// New builds an Executor for a scenario whose steps are all ws steps. To
// run scenarios that mix HTTP and WebSocket steps use package
// protocol/executor, which builds one from the ws steps only.
func New(sc *scenario.Scenario, opts Options) (*Executor, error) {
	if sc == nil {
		return nil, errors.New("wsx: nil scenario")
	}
	if len(sc.Steps) == 0 {
		return nil, errors.New("wsx: scenario has no steps")
	}
	base, err := wsBase(sc.Target.BaseURL)
	if err != nil {
		return nil, err
	}
	e := &Executor{
		base:    base,
		timeout: sc.Target.Timeout.Duration,
		maxMsg:  opts.MaxMessageBytes,
		steps:   make([]step, len(sc.Steps)),

		checkURL: opts.CheckURL,
		trace:    opts.Trace,
	}
	if e.timeout <= 0 {
		e.timeout = DefaultTimeout
	}
	if e.maxMsg <= 0 {
		e.maxMsg = DefaultMaxMessageBytes
	}
	for i := range sc.Steps {
		st, err := e.compileStep(&sc.Steps[i], sc.Target.Headers)
		if err != nil {
			return nil, fmt.Errorf("wsx: step %d (%s): %w", i+1, sc.Steps[i].Name, err)
		}
		e.steps[i] = st
	}

	// A dedicated transport: upgraded connections leave the pool, so no
	// per-host cap applies, and WebSocket needs HTTP/1.1.
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   e.timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:   false,
		MaxIdleConnsPerHost: 16, // only rejected handshakes return conns
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: sc.Target.InsecureSkipVerify, //nolint:gosec // opt-in per scenario
		},
	}
	e.client = &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // report, never follow
		},
	}
	return e, nil
}

// schemeSep separates a URL's scheme from the rest.
const schemeSep = "://"

// wsBase maps an http(s) base URL to ws(s). An empty base is allowed; then
// every ws step needs an absolute URL.
func wsBase(base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", nil
	}
	if scheme, rest, ok := strings.Cut(base, schemeSep); ok {
		switch strings.ToLower(scheme) {
		case "http":
			return "ws" + schemeSep + rest, nil
		case "https":
			return "wss" + schemeSep + rest, nil
		case "ws", "wss":
			return base, nil
		}
	}
	return "", fmt.Errorf("wsx: target base_url %q: want http(s):// or ws(s)://", base)
}

func (e *Executor) compileStep(s *scenario.Step, targetHeaders map[string]string) (step, error) {
	st := step{
		name:       s.Name,
		path:       s.Path,
		pathTmpl:   strings.Contains(s.Path, "{{"),
		think:      s.ThinkTime.Duration,
		lastExpect: -1,
	}
	if st.name == "" {
		st.name = "WS " + s.Path
	}
	st.holdLabel = st.name + ":hold"
	if !s.IsWS() {
		return st, fmt.Errorf("type %q is not ws", s.Type)
	}
	if s.WS == nil {
		return st, errors.New("missing ws block")
	}
	if !st.pathTmpl && !isAbsURL(st.path) && e.base == "" {
		return st, errors.New("relative path needs target base_url")
	}

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

	ws := s.WS
	st.hold = ws.Hold.Duration
	st.pingEvery = ws.PingEvery.Duration
	st.pingMsg = ws.PingMessage
	st.pingTmpl = strings.Contains(ws.PingMessage, "{{")
	if st.hold < 0 || st.pingEvery < 0 {
		return st, errors.New("hold and ping_every must not be negative")
	}
	for j, m := range ws.Messages {
		if m.Send == "" && m.Expect == "" {
			return st, fmt.Errorf("message %d: needs send or expect", j+1)
		}
		msg := message{
			label:      fmt.Sprintf("%s:%d", st.name, j+1),
			send:       m.Send,
			sendTmpl:   strings.Contains(m.Send, "{{"),
			expect:     m.Expect,
			expectTmpl: strings.Contains(m.Expect, "{{"),
			timeout:    m.Timeout.Duration,
		}
		if msg.timeout <= 0 {
			msg.timeout = e.timeout
		}
		if m.Expect != "" {
			st.lastExpect = j
		}
		st.msgs = append(st.msgs, msg)
	}
	for _, x := range s.Extract {
		ex := extractor{varName: x.Var, from: x.From, expr: x.Expr}
		switch x.From {
		case scenario.FromJSON:
			p, err := jsonpath.Compile(x.Expr)
			if err != nil {
				return st, fmt.Errorf("extract %s: %w", x.Var, err)
			}
			ex.path = p
		case scenario.FromRegex:
			re, err := regexp.Compile(x.Expr)
			if err != nil {
				return st, fmt.Errorf("extract %s: %w", x.Var, err)
			}
			ex.re = re
		default:
			return st, fmt.Errorf("extract %s: source %q is not supported on ws steps", x.Var, x.From)
		}
		st.extracts = append(st.extracts, ex)
	}
	if len(st.extracts) > 0 && st.lastExpect < 0 {
		return st, errors.New("extract needs a message with expect")
	}
	return st, nil
}

func isAbsURL(p string) bool {
	lp := strings.ToLower(p)
	return strings.HasPrefix(lp, "ws://") || strings.HasPrefix(lp, "wss://") ||
		strings.HasPrefix(lp, "http://") || strings.HasPrefix(lp, "https://")
}

// Close releases idle connections held by the dial transport. Open
// WebSocket connections belong to running iterations and are unaffected.
func (e *Executor) Close() { e.client.CloseIdleConnections() }

// NumSteps reports how many steps the executor was built with.
func (e *Executor) NumSteps() int { return len(e.steps) }

// Iterate implements protocol.Executor for scenarios made only of ws steps.
func (e *Executor) Iterate(ctx context.Context, vars map[string]string, intended time.Time, sink func(protocol.Result)) {
	for i := range e.steps {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		if i == 0 && !intended.IsZero() && intended.Before(start) {
			start = intended
		}
		extracted, ok := e.Step(ctx, i, vars, start, sink)
		if !ok {
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
		if t := e.steps[i].think; t > 0 && !sleep(ctx, t) {
			return
		}
	}
}

// Step runs ws step i (0-based) once: connect, exchange, hold, close. It
// reports its Results to sink (see the package documentation) and returns
// ok=false if the iteration must stop, i.e. a Result failed or ctx was
// cancelled. On success, extracted holds alternating variable name / value
// pairs for the caller to apply. Latency of the connect Result is measured
// from start. Step does not sleep the think time.
func (e *Executor) Step(ctx context.Context, i int, vars map[string]string, start time.Time, sink func(protocol.Result)) (extracted []string, ok bool) {
	st := &e.steps[i]

	// 1. Connect.
	c, res := e.connect(ctx, st, vars, start)
	if ctx.Err() != nil {
		if c != nil {
			c.CloseNow()
		}
		return nil, false
	}
	sink(res)
	if res.Err != nil {
		return nil, false
	}
	defer c.CloseNow() // no-op once closed normally
	c.SetReadLimit(-1) // we cap what we buffer ourselves

	// 2. Exchange.
	for j := range st.msgs {
		m := &st.msgs[j]
		res, ex := e.exchange(ctx, c, st, m, j == st.lastExpect, vars)
		if ctx.Err() != nil {
			return nil, false
		}
		if res.Step != "" {
			sink(res)
		}
		if res.Err != nil {
			return nil, false
		}
		if ex != nil {
			extracted = ex
		}
	}

	// 3. Hold and close.
	if st.hold > 0 {
		err := e.hold(ctx, c, st, vars)
		if ctx.Err() != nil {
			return nil, false
		}
		if err != nil {
			sink(protocol.Result{Step: st.holdLabel, Err: err})
			return nil, false
		}
		return extracted, true
	}
	c.Close(websocket.StatusNormalClosure, "")
	return extracted, ctx.Err() == nil
}

// connect performs the handshake. On failure c is nil and res.Err is set.
func (e *Executor) connect(ctx context.Context, st *step, vars map[string]string, start time.Time) (*websocket.Conn, protocol.Result) {
	res := protocol.Result{Step: st.name}

	path := st.path
	if st.pathTmpl {
		path = scenario.Render(path, vars)
	}
	target := path
	if !isAbsURL(path) {
		if e.base == "" {
			res.Err = fmt.Errorf("connect: relative path %q needs target base_url", path)
			res.Latency = time.Since(start)
			return nil, res
		}
		if path != "" && path[0] != '/' && path[0] != '?' {
			path = "/" + path
		}
		target = e.base + path
	}
	opts := &websocket.DialOptions{HTTPClient: e.client}
	if len(st.headers) > 0 {
		h := make(http.Header, len(st.headers))
		for _, hd := range st.headers {
			v := hd.value
			if hd.tmpl {
				v = scenario.Render(v, vars)
			}
			h[hd.key] = []string{v}
		}
		opts.HTTPHeader = h
	}
	if st.host != "" {
		opts.Host = st.host
		if st.hostTmpl {
			opts.Host = scenario.Render(st.host, vars)
		}
	}

	if e.trace != nil {
		e.trace(st.name, target, opts.HTTPHeader.Clone())
	}
	if e.checkURL != nil {
		if err := e.checkURL(target); err != nil {
			res.Err = fmt.Errorf("connect: %w", err)
			res.Latency = time.Since(start)
			return nil, res
		}
	}

	dctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, target, opts)
	res.Latency = time.Since(start)
	if err != nil {
		if resp != nil {
			res.Status = resp.StatusCode
		}
		if errors.Is(dctx.Err(), context.DeadlineExceeded) {
			res.Err = fmt.Errorf("connect: timeout after %s: %w", e.timeout, err)
		} else {
			res.Err = fmt.Errorf("connect: %w", err)
		}
		return nil, res
	}
	res.Status = StatusSwitchingProtocols
	return c, res
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// exchange sends m.send (if any) and waits for m.expect (if any). The
// returned Result has an empty Step when there is nothing to report (a
// successful send-only message). If last is set and the step has extracts,
// they run on the matching message; extraction errors fail the Result.
func (e *Executor) exchange(ctx context.Context, c *websocket.Conn, st *step, m *message, last bool, vars map[string]string) (protocol.Result, []string) {
	res := protocol.Result{Step: m.label, Status: StatusSwitchingProtocols}
	mctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	start := time.Now()

	if m.send != "" {
		payload := m.send
		if m.sendTmpl {
			payload = scenario.Render(payload, vars)
		}
		if err := c.Write(mctx, websocket.MessageText, []byte(payload)); err != nil {
			res.Latency = time.Since(start)
			res.Status = 0
			res.Err = fmt.Errorf("send: %w", wsErr(mctx, m.timeout, err))
			return res, nil
		}
	}
	if m.expect == "" {
		return protocol.Result{}, nil
	}

	want := m.expect
	if m.expectTmpl {
		want = scenario.Render(want, vars)
	}
	buf := bufPool.Get().(*bytes.Buffer)
	defer func() {
		if buf.Cap() <= maxPooledBuf {
			bufPool.Put(buf)
		}
	}()
	for {
		buf.Reset()
		_, r, err := c.Reader(mctx)
		if err == nil {
			var n, rest int64
			n, err = io.Copy(buf, io.LimitReader(r, int64(e.maxMsg)))
			if err == nil && n == int64(e.maxMsg) {
				rest, err = io.Copy(io.Discard, r)
			}
			res.Bytes += n + rest
		}
		if err != nil {
			res.Latency = time.Since(start)
			res.Status = 0
			res.Err = fmt.Errorf("expect %q: %w", want, wsErr(mctx, m.timeout, err))
			return res, nil
		}
		if bytes.Contains(buf.Bytes(), []byte(want)) {
			break
		}
	}
	res.Latency = time.Since(start)

	if !last || len(st.extracts) == 0 {
		return res, nil
	}
	extracted := make([]string, 0, 2*len(st.extracts))
	var (
		doc    any
		docErr error
		parsed bool
	)
	body := buf.Bytes()
	for i := range st.extracts {
		x := &st.extracts[i]
		var v string
		switch x.from {
		case scenario.FromRegex:
			sm := x.re.FindSubmatch(body)
			if sm == nil {
				res.Err = fmt.Errorf("extract %s: regex %s did not match", x.varName, x.expr)
				return res, nil
			}
			if len(sm) > 1 {
				v = string(sm[1])
			} else {
				v = string(sm[0])
			}
		default: // json
			if !parsed {
				parsed = true
				dec := json.NewDecoder(bytes.NewReader(body))
				dec.UseNumber()
				if err := dec.Decode(&doc); err != nil {
					docErr = fmt.Errorf("invalid JSON message: %w", err)
				}
			}
			if docErr != nil {
				res.Err = fmt.Errorf("extract %s: %w", x.varName, docErr)
				return res, nil
			}
			got, ok := x.path.Get(doc)
			if !ok {
				res.Err = fmt.Errorf("extract %s: json path %s not found", x.varName, x.path)
				return res, nil
			}
			v = jsonpath.String(got)
		}
		extracted = append(extracted, x.varName, v)
	}
	return res, extracted
}

// Connection states during hold.
const (
	holdOpen    int32 = iota
	holdClosing       // we initiated the close (hold elapsed or ctx done)
	holdBroken        // the connection ended on its own
)

// hold keeps c open for st.hold, then closes it normally. It returns nil
// if the hold completed (or ctx was cancelled; the caller checks ctx), or
// an error describing why the connection ended early.
//
// The VU goroutine reads and discards incoming frames, which also answers
// pings and observes the close handshake. The hold deadline, ctx
// cancellation and pings run as timer callbacks so no goroutine is parked
// per connection besides the caller's.
func (e *Executor) hold(ctx context.Context, c *websocket.Conn, st *step, vars map[string]string) error {
	var state atomic.Int32
	closed := make(chan struct{})
	closeConn := func() {
		if state.CompareAndSwap(holdOpen, holdClosing) {
			c.Close(websocket.StatusNormalClosure, "")
			close(closed)
		}
	}
	holdTimer := time.AfterFunc(st.hold, closeConn)
	defer holdTimer.Stop()
	stopCtx := context.AfterFunc(ctx, closeConn)
	defer stopCtx()

	var pingErr atomic.Pointer[error]
	if st.pingEvery > 0 {
		var payload []byte
		if st.pingMsg != "" {
			p := st.pingMsg
			if st.pingTmpl {
				p = scenario.Render(p, vars)
			}
			payload = []byte(p)
		}
		// Create the timer stopped and arm it only once both t and ping are
		// assigned, so the callback never observes them half-initialised.
		var ping func()
		t := time.AfterFunc(time.Hour, func() { ping() })
		t.Stop()
		ping = func() {
			if state.Load() != holdOpen {
				return
			}
			pctx, cancel := context.WithTimeout(context.Background(), e.timeout)
			var err error
			if payload != nil {
				err = c.Write(pctx, websocket.MessageText, payload)
			} else {
				err = c.Ping(pctx)
			}
			cancel()
			if state.Load() != holdOpen {
				return
			}
			if err != nil {
				err = fmt.Errorf("ping: %w", wsErr(pctx, e.timeout, err))
				pingErr.Store(&err)
				c.CloseNow() // ends the read loop below
				return
			}
			t.Reset(st.pingEvery)
		}
		t.Reset(st.pingEvery)
		defer t.Stop()
	}

	var readErr error
	for {
		_, r, err := c.Reader(context.Background())
		if err == nil {
			_, err = io.Copy(io.Discard, r)
		}
		if err != nil {
			readErr = err
			break
		}
	}

	if state.CompareAndSwap(holdOpen, holdBroken) {
		c.CloseNow()
		if p := pingErr.Load(); p != nil {
			return *p
		}
		if cs := websocket.CloseStatus(readErr); cs != -1 {
			return fmt.Errorf("server closed the connection during hold (status %d %s)", int(cs), cs)
		}
		return fmt.Errorf("connection lost during hold: %w", readErr)
	}
	<-closed // our close handshake has finished
	return nil
}

// wsErr labels a context timeout clearly; other errors pass through.
func wsErr(ctx context.Context, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timeout after %s", timeout)
	}
	if cs := websocket.CloseStatus(err); cs != -1 {
		return fmt.Errorf("connection closed by server (status %d %s)", int(cs), cs)
	}
	return err
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
