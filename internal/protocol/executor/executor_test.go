package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/wsx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// server: POST /login returns a token; GET /ws accepts only that token
// and answers "join" with "joined"; GET /after echoes the Authorization
// header.
func server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"s3cret"}`))
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "s3cret" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, msg, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if strings.Contains(string(msg), "join") {
				c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"joined","seq":7}`))
			}
		}
	})
	mux.HandleFunc("GET /after", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.URL.RawQuery))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func parse(t *testing.T, y string) *scenario.Scenario {
	t.Helper()
	sc, err := scenario.Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return sc
}

type collector struct {
	mu  sync.Mutex
	res []protocol.Result
}

func (c *collector) sink(r protocol.Result) {
	c.mu.Lock()
	c.res = append(c.res, r)
	c.mu.Unlock()
}

func (c *collector) steps() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.res {
		s := r.Step
		if r.Err != nil {
			s += "!" + r.Err.Error()
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

const mixedYAML = `
name: mixed
target:
  base_url: %BASE%
  timeout: 5s
load:
  model: closed
  stages: [{duration: 1s, target: 1}]
steps:
  - name: login
    method: POST
    path: /login
    extract: [{var: token, from: json, expr: token}]
  - name: chat
    type: ws
    path: /ws?token={{token}}
    ws:
      messages:
        - send: '{"type":"join"}'
          expect: joined
      hold: 100ms
    extract: [{var: seq, from: json, expr: seq}]
    think_time: 10ms
  - name: after
    path: /after?seq={{seq}}
    assert: [{type: body_contains, value: "seq=7"}]
`

func TestMixedHTTPLoginThenWS(t *testing.T) {
	srv := server(t)
	sc := parse(t, strings.ReplaceAll(mixedYAML, "%BASE%", srv.URL))
	exec, closeFn, err := New(sc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	var c collector
	vars := map[string]string{}
	exec.Iterate(context.Background(), vars, time.Now().Add(-time.Second), c.sink)
	if got := c.steps(); got != "login,chat,chat:1,after" {
		t.Fatalf("results = %s", got)
	}
	if vars["token"] != "s3cret" || vars["seq"] != "7" {
		t.Errorf("vars = %v", vars)
	}
	if c.res[0].Latency < time.Second {
		t.Errorf("first step latency %v not measured from intended", c.res[0].Latency)
	}
	if c.res[1].Latency >= time.Second {
		t.Errorf("ws connect latency %v measured from intended", c.res[1].Latency)
	}
}

func TestMixedStopsOnFailure(t *testing.T) {
	srv := server(t)
	// A wrong token fails the ws handshake; the later http step must not run.
	y := strings.ReplaceAll(mixedYAML, "%BASE%", srv.URL)
	y = strings.Replace(y, "path: /ws?token={{token}}", "path: /ws?token=wrong", 1)
	exec, closeFn, err := New(parse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	var c collector
	exec.Iterate(context.Background(), nil, time.Now(), c.sink)
	if got := c.steps(); !strings.HasPrefix(got, "login,chat!connect:") || strings.Contains(got, "after") {
		t.Fatalf("results = %s", got)
	}
	if c.res[1].Status != http.StatusUnauthorized {
		t.Errorf("status = %d", c.res[1].Status)
	}
}

func TestMixedCancelDuringHold(t *testing.T) {
	srv := server(t)
	y := strings.Replace(strings.ReplaceAll(mixedYAML, "%BASE%", srv.URL), "hold: 100ms", "hold: 1m", 1)
	exec, closeFn, err := New(parse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var c collector
	begin := time.Now()
	exec.Iterate(ctx, nil, time.Now(), c.sink)
	if el := time.Since(begin); el > 3*time.Second {
		t.Errorf("Iterate took %v after cancel", el)
	}
	if got := c.steps(); got != "login,chat,chat:1" {
		t.Fatalf("results = %s", got)
	}
}

func TestPureProtocolsUseTheirExecutors(t *testing.T) {
	httpOnly := parse(t, `
name: h
target: {base_url: "http://example.test"}
load: {stages: [{duration: 1s, target: 1}]}
steps: [{path: /}]
`)
	e, closeFn, err := New(httpOnly, Options{HTTP: httpx.Options{MaxConnsPerHost: 5}})
	if err != nil {
		t.Fatal(err)
	}
	closeFn()
	if _, ok := e.(*httpx.Executor); !ok {
		t.Errorf("http-only scenario got %T, want *httpx.Executor", e)
	}

	wsOnly := parse(t, `
name: w
target: {base_url: "http://example.test"}
load: {stages: [{duration: 1s, target: 1}]}
steps: [{type: ws, path: /ws, ws: {hold: 1s}}]
`)
	e, closeFn, err = New(wsOnly, Options{})
	if err != nil {
		t.Fatal(err)
	}
	closeFn()
	if _, ok := e.(*wsx.Executor); !ok {
		t.Errorf("ws-only scenario got %T, want *wsx.Executor", e)
	}

	if _, _, err := New(nil, Options{}); err == nil {
		t.Error("nil scenario accepted")
	}
}

func TestExampleBuilds(t *testing.T) {
	sc, err := scenario.LoadFile("../../../examples/websocket_chat.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e, closeFn, err := New(sc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, ok := e.(*mixed); !ok {
		t.Errorf("got %T, want the mixed executor", e)
	}
}
