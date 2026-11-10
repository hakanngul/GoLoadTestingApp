package wsx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func dur(d time.Duration) scenario.Duration { return scenario.Duration{Duration: d} }

type collector struct {
	mu  sync.Mutex
	res []protocol.Result
}

func (c *collector) sink(r protocol.Result) {
	c.mu.Lock()
	c.res = append(c.res, r)
	c.mu.Unlock()
}

func (c *collector) all() []protocol.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.Result(nil), c.res...)
}

// chatServer is a scriptable WebSocket test server.
type chatServer struct {
	*httptest.Server

	silent      bool          // never reply
	closeAfter  time.Duration // server-initiated close after this long (0 = never)
	pings       atomic.Int64  // text messages equal to "ping"
	ctrlPings   atomic.Int64  // protocol ping frames received
	normalClose atomic.Int64  // connections the client closed with 1000
	lastQuery   atomic.Value  // string
	lastAuth    atomic.Value  // string
	reject      bool          // answer the handshake with 403
}

func newChatServer(t testing.TB, configure func(*chatServer)) *chatServer {
	t.Helper()
	s := &chatServer{}
	if configure != nil {
		configure(s)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *chatServer) handle(w http.ResponseWriter, r *http.Request) {
	if s.reject {
		http.Error(w, "nope", http.StatusForbidden)
		return
	}
	s.lastQuery.Store(r.URL.RawQuery)
	s.lastAuth.Store(r.Header.Get("Authorization"))
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			s.ctrlPings.Add(1)
			return true
		},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	if s.closeAfter > 0 {
		time.AfterFunc(s.closeAfter, func() { c.Close(websocket.StatusGoingAway, "bye") })
	}
	ctx := context.Background()
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				s.normalClose.Add(1)
			}
			return
		}
		m := string(msg)
		switch {
		case s.silent:
		case m == "ping":
			s.pings.Add(1)
		case strings.Contains(m, `"join"`):
			// Some noise first: expect must skip non-matching messages.
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"welcome"}`))
			room := between(m, `"room":"`, `"`)
			_ = c.Write(ctx, websocket.MessageText,
				[]byte(`{"type":"joined","room":"`+room+`","member":{"id":42}}`))
		default:
			_ = c.Write(ctx, websocket.MessageText, msg) // echo
		}
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return s
}

func wsStep(name, path string, ws scenario.WSStep) scenario.Step {
	return scenario.Step{Name: name, Type: scenario.StepWS, Path: path, WS: &ws}
}

func newExec(t testing.TB, base string, timeout time.Duration, steps ...scenario.Step) *Executor {
	t.Helper()
	sc := &scenario.Scenario{
		Name:   "t",
		Target: scenario.Target{BaseURL: base, Timeout: dur(timeout)},
		Steps:  steps,
	}
	e, err := New(sc, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

func names(rs []protocol.Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Step
		if r.Err != nil {
			out[i] += "!"
		}
	}
	return out
}

func wantSteps(t *testing.T, rs []protocol.Result, want ...string) {
	t.Helper()
	if got := names(rs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("results = %v, want %v (errors: %v)", got, want, errs(rs))
	}
}

func errs(rs []protocol.Result) []error {
	var out []error
	for _, r := range rs {
		if r.Err != nil {
			out = append(out, r.Err)
		}
	}
	return out
}

func TestConnectExpectExtractAndTemplating(t *testing.T) {
	srv := newChatServer(t, nil)
	st := wsStep("chat", "/ws?room={{room}}", scenario.WSStep{
		Messages: []scenario.WSMessage{
			{Send: `{"type":"join","room":"{{room}}"}`, Expect: `"room":"{{room}}"`},
			{Send: "hello {{room}}"}, // send only: no result
			{Send: "echo-{{room}}", Expect: "echo-{{room}}"},
		},
	})
	st.Headers = map[string]string{"Authorization": "Bearer {{token}}"}
	st.Extract = []scenario.Extract{{Var: "echoed", From: scenario.FromRegex, Expr: `echo-(\w+)`}}
	e := newExec(t, srv.URL, 5*time.Second, st)

	var c collector
	extracted, ok := e.Step(context.Background(), 0, map[string]string{"room": "lobby", "token": "t0k"}, time.Now(), c.sink)
	if !ok {
		t.Fatalf("Step failed: %v", errs(c.all()))
	}
	rs := c.all()
	wantSteps(t, rs, "chat", "chat:1", "chat:3")
	for _, r := range rs {
		if r.Status != http.StatusSwitchingProtocols || r.Latency <= 0 {
			t.Errorf("%s: status %d latency %v", r.Step, r.Status, r.Latency)
		}
	}
	if rs[1].Bytes == 0 {
		t.Error("expect result counted no bytes")
	}
	if got := strings.Join(extracted, "="); got != "echoed=lobby" {
		t.Errorf("extracted = %q", got)
	}
	if q := srv.lastQuery.Load(); q != "room=lobby" {
		t.Errorf("query = %v", q)
	}
	if a := srv.lastAuth.Load(); a != "Bearer t0k" {
		t.Errorf("auth = %v", a)
	}
}

func TestJSONExtractAndIterateSharesVars(t *testing.T) {
	srv := newChatServer(t, nil)
	first := wsStep("join", "/ws", scenario.WSStep{
		Messages: []scenario.WSMessage{{Send: `{"type":"join","room":"r1"}`, Expect: `"joined"`}},
	})
	first.Extract = []scenario.Extract{{Var: "member", From: scenario.FromJSON, Expr: "member.id"}}
	second := wsStep("echo", "/ws?m={{member}}", scenario.WSStep{
		Messages: []scenario.WSMessage{{Send: "id={{member}}", Expect: "id=42"}},
	})
	e := newExec(t, srv.URL, 5*time.Second, first, second)
	var c collector
	vars := map[string]string{}
	e.Iterate(context.Background(), vars, time.Now(), c.sink)
	wantSteps(t, c.all(), "join", "join:1", "echo", "echo:1")
	if vars["member"] != "42" || srv.lastQuery.Load() != "m=42" {
		t.Errorf("vars = %v, query = %v", vars, srv.lastQuery.Load())
	}
}

func TestExpectTimeout(t *testing.T) {
	srv := newChatServer(t, func(s *chatServer) { s.silent = true })
	e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{
		Hold:     dur(time.Minute), // never reached
		Messages: []scenario.WSMessage{{Send: "hi", Expect: "never", Timeout: dur(150 * time.Millisecond)}},
	}))
	var c collector
	begin := time.Now()
	e.Iterate(context.Background(), nil, time.Now(), c.sink)
	rs := c.all()
	wantSteps(t, rs, "chat", "chat:1!")
	if !strings.Contains(rs[1].Err.Error(), "timeout after 150ms") {
		t.Errorf("err = %v", rs[1].Err)
	}
	if el := time.Since(begin); el > 2*time.Second {
		t.Errorf("took %v", el)
	}
}

func TestServerClosesDuringHold(t *testing.T) {
	srv := newChatServer(t, func(s *chatServer) { s.closeAfter = 100 * time.Millisecond })
	e := newExec(t, srv.URL, 5*time.Second,
		wsStep("chat", "/ws", scenario.WSStep{Hold: dur(10 * time.Second)}),
		wsStep("never", "/ws", scenario.WSStep{}),
	)
	var c collector
	begin := time.Now()
	e.Iterate(context.Background(), nil, time.Now(), c.sink)
	rs := c.all()
	wantSteps(t, rs, "chat", "chat:hold!")
	if msg := rs[1].Err.Error(); !strings.Contains(msg, "1001") || !strings.Contains(msg, "during hold") {
		t.Errorf("err = %v", msg)
	}
	if el := time.Since(begin); el > 3*time.Second {
		t.Errorf("hold failure took %v to notice", el)
	}
}

func TestHoldCompletesWithNormalClose(t *testing.T) {
	srv := newChatServer(t, nil)
	e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{Hold: dur(200 * time.Millisecond)}))
	var c collector
	begin := time.Now()
	e.Iterate(context.Background(), nil, time.Now(), c.sink)
	el := time.Since(begin)
	wantSteps(t, c.all(), "chat")
	if el < 200*time.Millisecond || el > 2*time.Second {
		t.Errorf("iteration took %v, want ~200ms", el)
	}
	if c.all()[0].Latency > 150*time.Millisecond {
		t.Errorf("holding time leaked into latency: %v", c.all()[0].Latency)
	}
	waitFor(t, func() bool { return srv.normalClose.Load() == 1 })
}

func TestPingEvery(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		srv := newChatServer(t, nil)
		e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{
			Hold: dur(450 * time.Millisecond), PingEvery: dur(100 * time.Millisecond), PingMessage: "{{p}}",
		}))
		var c collector
		e.Iterate(context.Background(), map[string]string{"p": "ping"}, time.Now(), c.sink)
		wantSteps(t, c.all(), "chat")
		waitFor(t, func() bool { return srv.normalClose.Load() == 1 })
		if n := srv.pings.Load(); n < 3 || n > 5 {
			t.Errorf("server saw %d pings, want ~4", n)
		}
	})
	t.Run("control frame", func(t *testing.T) {
		srv := newChatServer(t, nil)
		e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{
			Hold: dur(350 * time.Millisecond), PingEvery: dur(100 * time.Millisecond),
		}))
		var c collector
		e.Iterate(context.Background(), nil, time.Now(), c.sink)
		wantSteps(t, c.all(), "chat")
		if n := srv.ctrlPings.Load(); n < 2 {
			t.Errorf("server saw %d ping frames, want >= 2", n)
		}
	})
}

func TestContextCancelMidHoldReportsNothing(t *testing.T) {
	srv := newChatServer(t, nil)
	e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{
		Hold: dur(time.Minute), PingEvery: dur(50 * time.Millisecond), PingMessage: "ping",
	}))
	ctx, cancel := context.WithCancel(context.Background())
	var c collector
	done := make(chan struct{})
	go func() {
		e.Iterate(ctx, nil, time.Now(), c.sink)
		close(done)
	}()
	waitFor(t, func() bool { return len(c.all()) == 1 })
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Iterate did not return after cancel")
	}
	wantSteps(t, c.all(), "chat") // only the connect result, no failure
	waitFor(t, func() bool { return srv.normalClose.Load() == 1 })
}

func TestContextCancelDuringExpect(t *testing.T) {
	srv := newChatServer(t, func(s *chatServer) { s.silent = true })
	e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{
		Messages: []scenario.WSMessage{{Send: "x", Expect: "never"}},
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var c collector
	e.Iterate(ctx, nil, time.Now(), c.sink)
	wantSteps(t, c.all(), "chat")
}

func TestHandshakeRejected(t *testing.T) {
	srv := newChatServer(t, func(s *chatServer) { s.reject = true })
	e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{}))
	var c collector
	e.Iterate(context.Background(), nil, time.Now(), c.sink)
	rs := c.all()
	wantSteps(t, rs, "chat!")
	if rs[0].Status != http.StatusForbidden || !strings.HasPrefix(rs[0].Err.Error(), "connect:") {
		t.Errorf("status %d err %v", rs[0].Status, rs[0].Err)
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	e := newExec(t, url, time.Second, wsStep("chat", "/ws", scenario.WSStep{}))
	var c collector
	e.Iterate(context.Background(), nil, time.Now(), c.sink)
	rs := c.all()
	wantSteps(t, rs, "chat!")
	if rs[0].Status != 0 {
		t.Errorf("status = %d", rs[0].Status)
	}
}

func TestCoordinatedOmission(t *testing.T) {
	srv := newChatServer(t, nil)
	e := newExec(t, srv.URL, 5*time.Second,
		wsStep("a", "/ws", scenario.WSStep{}), wsStep("b", "/ws", scenario.WSStep{}))
	var c collector
	e.Iterate(context.Background(), nil, time.Now().Add(-time.Second), c.sink)
	rs := c.all()
	wantSteps(t, rs, "a", "b")
	if rs[0].Latency < time.Second {
		t.Errorf("first step latency %v not measured from intended", rs[0].Latency)
	}
	if rs[1].Latency >= time.Second {
		t.Errorf("second step latency %v measured from intended", rs[1].Latency)
	}
}

func TestAbsoluteURLAndSchemeMapping(t *testing.T) {
	srv := newChatServer(t, nil)
	abs := "ws" + strings.TrimPrefix(srv.URL, "http") + "/x?abs=1"
	e := newExec(t, "http://unused.invalid", 5*time.Second, wsStep("chat", abs, scenario.WSStep{}))
	var c collector
	e.Iterate(context.Background(), nil, time.Now(), c.sink)
	wantSteps(t, c.all(), "chat")
	if srv.lastQuery.Load() != "abs=1" {
		t.Errorf("query = %v", srv.lastQuery.Load())
	}

	for in, want := range map[string]string{
		"http://h:1/p/": "ws://h:1/p",
		"HTTPS://h":     "wss://h",
		"wss://h/x":     "wss://h/x",
		"":              "",
	} {
		if got, err := wsBase(in); err != nil || got != want {
			t.Errorf("wsBase(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := wsBase("ftp://h"); err == nil {
		t.Error("wsBase(ftp) accepted")
	}
}

func TestNewValidation(t *testing.T) {
	ok := wsStep("a", "/ws", scenario.WSStep{})
	cases := map[string]*scenario.Scenario{
		"nil steps": {Target: scenario.Target{BaseURL: "http://h"}},
		"http step": {Target: scenario.Target{BaseURL: "http://h"}, Steps: []scenario.Step{{Name: "h", Path: "/"}}},
		"no base":   {Steps: []scenario.Step{ok}},
		"bad base":  {Target: scenario.Target{BaseURL: "ftp://h"}, Steps: []scenario.Step{ok}},
		"extract without expect": {Target: scenario.Target{BaseURL: "http://h"}, Steps: []scenario.Step{func() scenario.Step {
			s := wsStep("a", "/ws", scenario.WSStep{Messages: []scenario.WSMessage{{Send: "x"}}})
			s.Extract = []scenario.Extract{{Var: "v", From: scenario.FromJSON, Expr: "a"}}
			return s
		}()}},
	}
	for name, sc := range cases {
		if _, err := New(sc, Options{}); err == nil {
			t.Errorf("%s: New accepted", name)
		}
	}
	if _, err := New(nil, Options{}); err == nil {
		t.Error("nil scenario accepted")
	}
}

func TestConcurrentIterate(t *testing.T) {
	srv := newChatServer(t, nil)
	e := newExec(t, srv.URL, 5*time.Second, wsStep("chat", "/ws", scenario.WSStep{
		Hold:      dur(100 * time.Millisecond),
		Messages:  []scenario.WSMessage{{Send: `{"type":"join","room":"{{room}}"}`, Expect: `"room":"{{room}}"`}},
		PingEvery: dur(20 * time.Millisecond), PingMessage: "ping",
	}))
	var okN, failN atomic.Int64
	sink := func(r protocol.Result) {
		if r.Err != nil {
			failN.Add(1)
		} else {
			okN.Add(1)
		}
	}
	var wg sync.WaitGroup
	for w := 0; w < 50; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				e.Iterate(context.Background(), map[string]string{"room": strings.Repeat("r", w%5+1)}, time.Now(), sink)
			}
		}(w)
	}
	wg.Wait()
	if failN.Load() != 0 || okN.Load() != 50*3*2 {
		t.Errorf("ok=%d fail=%d", okN.Load(), failN.Load())
	}
}

// waitFor polls cond for up to 3s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 3s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
