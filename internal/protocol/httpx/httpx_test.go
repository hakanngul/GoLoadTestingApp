package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func dur(d time.Duration) scenario.Duration { return scenario.Duration{Duration: d} }

// collector is a concurrency-safe sink.
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

func newExec(t testing.TB, base string, steps ...scenario.Step) *Executor {
	t.Helper()
	sc := &scenario.Scenario{Target: scenario.Target{BaseURL: base}, Steps: steps}
	e, err := New(sc, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

func run(e *Executor, vars map[string]string) []protocol.Result {
	var c collector
	e.Iterate(context.Background(), vars, time.Now(), c.sink)
	return c.all()
}

func testServer(t testing.TB) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"user":"alice"`) {
			http.Error(w, "bad body: "+string(b), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Session", "sess-42")
		fmt.Fprint(w, `{"data":{"token":"tok-123","user":{"id":7,"name":"alice"}},"items":[{"id":11},{"id":12}],"ok":true,"score":3.5}`)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" || r.Header.Get("X-Session") != "sess-42" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `<html><span id="csrf">abc-%s</span><p>item=%s</p></html>`, r.Header.Get("X-Env"), r.URL.Query().Get("item"))
	})
	mux.HandleFunc("GET /status/{code}", func(w http.ResponseWriter, r *http.Request) {
		var code int
		fmt.Sscan(r.PathValue("code"), &code)
		w.WriteHeader(code)
		fmt.Fprint(w, `{"ok":false}`)
	})
	mux.HandleFunc("GET /redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/status/200", http.StatusFound)
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "late")
	})
	mux.HandleFunc("GET /json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":9007199254740993,"name":"widget","n":5,"tags":["a","b"],"nil":null}`)
	})
	mux.HandleFunc("GET /headers", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ua=%s;x=%s;host=%s", r.Header.Get("User-Agent"), r.Header.Get("X-Shared"), r.Host)
	})
	mux.HandleFunc("GET /notjson", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain text")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestMultiStepFlowWithExtract(t *testing.T) {
	srv := testServer(t)
	sc := &scenario.Scenario{
		Target: scenario.Target{BaseURL: srv.URL + "/", Headers: map[string]string{"X-Env": "default"}},
		Steps: []scenario.Step{
			{
				Name: "login", Method: "post", Path: "/login",
				Body: `{"user":"{{user}}"}`,
				Assert: []scenario.Assert{
					{Type: scenario.AssertStatus, Value: "200"},
					{Type: scenario.AssertJSON, Expr: "data.user.id", Value: "7"},
				},
				Extract: []scenario.Extract{
					{Var: "token", From: scenario.FromJSON, Expr: "data.token"},
					{Var: "sess", From: scenario.FromHeader, Expr: "x-session"},
					{Var: "item", From: scenario.FromJSON, Expr: "$.items[1].id"},
				},
			},
			{
				Path:    "me?item={{item}}",
				Headers: map[string]string{"Authorization": "Bearer {{token}}", "X-Session": "{{sess}}", "x-env": "step"},
				Extract: []scenario.Extract{{Var: "csrf", From: scenario.FromRegex, Expr: `id="csrf">([^<]+)<`}},
				Assert:  []scenario.Assert{{Type: scenario.AssertBodyContains, Value: "item=12"}},
			},
		},
	}
	e, err := New(sc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	vars := map[string]string{"user": "alice"}
	res := run(e, vars)
	if len(res) != 2 {
		t.Fatalf("got %d results: %+v", len(res), res)
	}
	for _, r := range res {
		if !r.OK() {
			t.Fatalf("step %s failed: %v", r.Step, r.Err)
		}
		if r.Status != 200 || r.Bytes == 0 || r.Latency <= 0 {
			t.Errorf("bad result %+v", r)
		}
	}
	if res[0].Step != "login" || res[1].Step != "GET me?item={{item}}" {
		t.Errorf("step names = %q, %q", res[0].Step, res[1].Step)
	}
	// Step header overrode the target header (case-insensitively).
	if vars["csrf"] != "abc-step" || vars["token"] != "tok-123" || vars["item"] != "12" {
		t.Errorf("vars = %v", vars)
	}
}

func TestStopsOnFirstFailure(t *testing.T) {
	srv := testServer(t)
	e := newExec(t, srv.URL,
		scenario.Step{Path: "/status/500"},
		scenario.Step{Path: "/status/200"},
	)
	res := run(e, nil)
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].OK() || res[0].Status != 500 || res[0].Err.Error() != "status 500" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestAssertions(t *testing.T) {
	srv := testServer(t)
	type tc struct {
		name    string
		path    string
		assert  scenario.Assert
		wantErr string // "" = pass
	}
	A := func(typ scenario.AssertType, expr, op, val string) scenario.Assert {
		return scenario.Assert{Type: typ, Expr: expr, Op: op, Value: val}
	}
	cases := []tc{
		{"status eq pass", "/status/201", A(scenario.AssertStatus, "", "", "201"), ""},
		{"status eq fail", "/status/500", A(scenario.AssertStatus, "", "eq", "200"), "assert status: got 500, want eq 200"},
		{"status allows 404 explicitly", "/status/404", A(scenario.AssertStatus, "", "eq", "404"), ""},
		{"status lt pass", "/status/302", A(scenario.AssertStatus, "", "lt", "400"), ""},
		{"status gte fail", "/status/200", A(scenario.AssertStatus, "", "gte", "300"), "assert status: got 200, want gte 300"},
		{"status ne pass", "/status/200", A(scenario.AssertStatus, "", "ne", "500"), ""},
		{"duration pass", "/json", A(scenario.AssertDuration, "", "lt", "5s"), ""},
		{"duration fail", "/json", A(scenario.AssertDuration, "", "lt", "1ns"), "assert duration: got"},
		{"json num eq", "/json", A(scenario.AssertJSON, "n", "", "5"), ""},
		{"json num gt", "/json", A(scenario.AssertJSON, "n", "gt", "4.5"), ""},
		{"json num lte fail", "/json", A(scenario.AssertJSON, "n", "lte", "4"), "assert json n: got 5, want lte 4"},
		{"json bigint eq", "/json", A(scenario.AssertJSON, "id", "eq", "9007199254740993"), ""},
		{"json str eq", "/json", A(scenario.AssertJSON, "name", "eq", "widget"), ""},
		{"json str ne fail", "/json", A(scenario.AssertJSON, "name", "ne", "widget"), `assert json name: got "widget", want ne widget`},
		{"json contains", "/json", A(scenario.AssertJSON, "name", "contains", "dg"), ""},
		{"json contains fail", "/json", A(scenario.AssertJSON, "tags", "contains", "z"), `assert json tags: got ["a","b"], want contains z`},
		{"json null eq", "/json", A(scenario.AssertJSON, "nil", "eq", "null"), ""},
		{"json gt on string", "/json", A(scenario.AssertJSON, "name", "gt", "1"), "assert json name"},
		{"json missing", "/json", A(scenario.AssertJSON, "nope.x", "", "1"), "assert json: path nope.x not found"},
		{"json invalid body", "/notjson", A(scenario.AssertJSON, "a", "", "1"), "assert json a: invalid JSON body"},
		{"body contains", "/json", A(scenario.AssertBodyContains, "", "", "widget"), ""},
		{"body contains fail", "/json", A(scenario.AssertBodyContains, "", "contains", "gadget"), `assert body_contains: body does not contain "gadget"`},
		{"body ne", "/json", A(scenario.AssertBodyContains, "", "ne", "gadget"), ""},
		{"body ne fail", "/json", A(scenario.AssertBodyContains, "", "ne", "widget"), `assert body_contains: body contains "widget"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newExec(t, srv.URL, scenario.Step{Path: c.path, Assert: []scenario.Assert{c.assert}})
			res := run(e, nil)
			if len(res) != 1 {
				t.Fatalf("got %d results", len(res))
			}
			err := res[0].Err
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("error %q does not contain %q", err, c.wantErr)
			}
		})
	}
}

func TestExtractErrors(t *testing.T) {
	srv := testServer(t)
	cases := []struct {
		ex   scenario.Extract
		want string
	}{
		{scenario.Extract{Var: "token", From: scenario.FromJSON, Expr: "data.token"}, "extract token: json path data.token not found"},
		{scenario.Extract{Var: "h", From: scenario.FromHeader, Expr: "X-Missing"}, "extract h: header X-Missing not found"},
		{scenario.Extract{Var: "r", From: scenario.FromRegex, Expr: `zzz(\d+)`}, "extract r: regex zzz(\\d+) did not match"},
	}
	for _, c := range cases {
		e := newExec(t, srv.URL, scenario.Step{Path: "/json", Extract: []scenario.Extract{c.ex}}, scenario.Step{Path: "/json"})
		vars := map[string]string{}
		res := run(e, vars)
		if len(res) != 1 || res[0].Err == nil || res[0].Err.Error() != c.want {
			t.Errorf("results = %+v, want single error %q", res, c.want)
		}
		if len(vars) != 0 {
			t.Errorf("vars modified on failure: %v", vars)
		}
	}
}

func TestRegexWithoutGroupAndHostHeader(t *testing.T) {
	srv := testServer(t)
	sc := &scenario.Scenario{
		Target: scenario.Target{BaseURL: srv.URL, Headers: map[string]string{"User-Agent": "glt", "X-Shared": "t"}},
		Steps: []scenario.Step{{
			Path:    "/headers",
			Headers: map[string]string{"host": "example.test", "X-Shared": "s{{n}}"},
			Extract: []scenario.Extract{{Var: "all", From: scenario.FromRegex, Expr: `ua=\w+`}},
		}},
	}
	e, err := New(sc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"n": "1"}
	res := run(e, vars)
	if !res[0].OK() {
		t.Fatal(res[0].Err)
	}
	if vars["all"] != "ua=glt" {
		t.Errorf("all = %q", vars["all"])
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	srv := testServer(t)
	e := newExec(t, srv.URL, scenario.Step{
		Path:    "/redirect",
		Assert:  []scenario.Assert{{Type: scenario.AssertStatus, Value: "302"}},
		Extract: []scenario.Extract{{Var: "loc", From: scenario.FromHeader, Expr: "Location"}},
	})
	vars := map[string]string{}
	res := run(e, vars)
	if len(res) != 1 || !res[0].OK() || res[0].Status != 302 {
		t.Fatalf("results = %+v", res)
	}
	if vars["loc"] != "/status/200" {
		t.Errorf("loc = %q", vars["loc"])
	}
	// A 3xx without a status assertion is still a success (< 400).
	e2 := newExec(t, srv.URL, scenario.Step{Path: "/redirect"})
	if r := run(e2, nil); !r[0].OK() || r[0].Status != 302 {
		t.Errorf("result = %+v", r[0])
	}
}

func TestTimeoutProducesErrorResult(t *testing.T) {
	srv := testServer(t)
	sc := &scenario.Scenario{
		Target: scenario.Target{BaseURL: srv.URL, Timeout: dur(100 * time.Millisecond)},
		Steps:  []scenario.Step{{Path: "/slow"}},
	}
	e, err := New(sc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res := run(e, nil)
	if time.Since(start) > time.Second {
		t.Errorf("timeout not enforced: took %s", time.Since(start))
	}
	if len(res) != 1 || res[0].OK() || res[0].Status != 0 {
		t.Fatalf("results = %+v", res)
	}
	if !strings.Contains(res[0].Err.Error(), "timeout after 100ms") {
		t.Errorf("err = %v", res[0].Err)
	}
	if res[0].Latency < 100*time.Millisecond {
		t.Errorf("latency = %s", res[0].Latency)
	}
}

func TestCoordinatedOmission(t *testing.T) {
	srv := testServer(t)
	e := newExec(t, srv.URL, scenario.Step{Path: "/json"}, scenario.Step{Path: "/json"})
	const lag = 250 * time.Millisecond
	var c collector
	e.Iterate(context.Background(), nil, time.Now().Add(-lag), c.sink)
	res := c.all()
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	if res[0].Latency < lag {
		t.Errorf("first step latency %s < scheduling lag %s", res[0].Latency, lag)
	}
	if res[1].Latency >= lag {
		t.Errorf("second step latency %s should be measured from its own start", res[1].Latency)
	}
}

func TestContextCancelNoFailureResult(t *testing.T) {
	srv := testServer(t)
	e := newExec(t, srv.URL, scenario.Step{Path: "/slow"}, scenario.Step{Path: "/json"})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	var c collector
	start := time.Now()
	e.Iterate(ctx, nil, time.Now(), c.sink)
	if time.Since(start) > time.Second {
		t.Errorf("Iterate did not return promptly on cancel")
	}
	if res := c.all(); len(res) != 0 {
		t.Errorf("got results after cancel: %+v", res)
	}

	// Cancel during think time: first result reported, nothing after.
	e2 := newExec(t, srv.URL, scenario.Step{Path: "/json", ThinkTime: dur(5 * time.Second)}, scenario.Step{Path: "/json"})
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	var c2 collector
	start = time.Now()
	e2.Iterate(ctx2, nil, time.Now(), c2.sink)
	if time.Since(start) > time.Second {
		t.Errorf("think time not interrupted by cancel")
	}
	if res := c2.all(); len(res) != 1 || !res[0].OK() {
		t.Errorf("results = %+v", res)
	}
}

func TestThinkTime(t *testing.T) {
	srv := testServer(t)
	e := newExec(t, srv.URL, scenario.Step{Path: "/json", ThinkTime: dur(60 * time.Millisecond)}, scenario.Step{Path: "/json"})
	start := time.Now()
	if res := run(e, nil); len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	if el := time.Since(start); el < 60*time.Millisecond {
		t.Errorf("elapsed %s < think time", el)
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listening now
	e := newExec(t, url, scenario.Step{Path: "/"})
	res := run(e, nil)
	if len(res) != 1 || res[0].OK() || res[0].Status != 0 || !strings.HasPrefix(res[0].Err.Error(), "send: ") {
		t.Fatalf("results = %+v", res)
	}
}

func TestNewValidation(t *testing.T) {
	ok := scenario.Target{BaseURL: "http://localhost:1"}
	cases := []struct {
		name string
		sc   *scenario.Scenario
		want string
	}{
		{"nil", nil, "nil scenario"},
		{"no steps", &scenario.Scenario{Target: ok}, "no steps"},
		{"bad base", &scenario.Scenario{Target: scenario.Target{BaseURL: "ftp://x"}, Steps: []scenario.Step{{Path: "/"}}}, "base_url"},
		{"relative without base", &scenario.Scenario{Steps: []scenario.Step{{Path: "/"}}}, "needs target base_url"},
		{"bad regex", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Extract: []scenario.Extract{{Var: "x", From: scenario.FromRegex, Expr: "("}}}}}, "extract x"},
		{"bad extract source", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Extract: []scenario.Extract{{Var: "x", From: "xml"}}}}}, "unknown source"},
		{"bad status value", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertStatus, Value: "ok"}}}}}, "not an integer"},
		{"bad duration value", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertDuration, Value: "fast"}}}}}, "duration value"},
		{"bad op", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertStatus, Op: "approx", Value: "200"}}}}}, "unknown op"},
		{"contains on status", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertStatus, Op: "contains", Value: "200"}}}}}, "not valid for status"},
		{"json lt non-numeric", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertJSON, Expr: "a", Op: "lt", Value: "x"}}}}}, "numeric value"},
		{"bad json path", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Name: "s", Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertJSON, Expr: "a..b", Value: "1"}}}}}, "step 1 (s)"},
		{"unknown assert", &scenario.Scenario{Target: ok, Steps: []scenario.Step{{Path: "/", Assert: []scenario.Assert{{Type: "xpath"}}}}}, "unknown assert type"},
	}
	for _, c := range cases {
		_, err := New(c.sc, Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}
	// Absolute step URLs work without a base.
	if _, err := New(&scenario.Scenario{Steps: []scenario.Step{{Path: "https://example.test/x"}}}, Options{}); err != nil {
		t.Errorf("absolute URL: %v", err)
	}
}

func TestConcurrentIterate(t *testing.T) {
	srv := testServer(t)
	e := newExec(t, srv.URL,
		scenario.Step{
			Method: "POST", Path: "/login", Body: `{"user":"{{user}}"}`,
			Extract: []scenario.Extract{
				{Var: "token", From: scenario.FromJSON, Expr: "data.token"},
				{Var: "sess", From: scenario.FromHeader, Expr: "X-Session"},
			},
			Assert: []scenario.Assert{{Type: scenario.AssertJSON, Expr: "ok", Value: "true"}},
		},
		scenario.Step{
			Path:    "/me?item={{i}}",
			Headers: map[string]string{"Authorization": "Bearer {{token}}", "X-Session": "{{sess}}"},
			Assert:  []scenario.Assert{{Type: scenario.AssertBodyContains, Value: "item="}},
			Extract: []scenario.Extract{{Var: "item", From: scenario.FromRegex, Expr: `item=(\d+)`}},
		},
	)
	var (
		wg   sync.WaitGroup
		okN  atomic.Int64
		bad  atomic.Int64
		sink = func(r protocol.Result) {
			if r.OK() {
				okN.Add(1)
			} else {
				bad.Add(1)
			}
		}
	)
	const workers, iters = 16, 25
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				vars := map[string]string{"user": "alice", "i": fmt.Sprint(w*1000 + i)}
				e.Iterate(context.Background(), vars, time.Now(), sink)
				if vars["item"] != vars["i"] {
					t.Errorf("vars crossed between iterations: %v", vars)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if bad.Load() != 0 || okN.Load() != workers*iters*2 {
		t.Errorf("ok=%d bad=%d", okN.Load(), bad.Load())
	}
}

func TestLargeBodyCountedAndCapped(t *testing.T) {
	const size = maxBodyRead + 12345
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("a", 64<<10)
		for n := 0; n < size; n += len(chunk) {
			if size-n < len(chunk) {
				chunk = chunk[:size-n]
			}
			io.WriteString(w, chunk)
		}
	}))
	defer srv.Close()

	// Body-needing assert: buffered up to the cap, all bytes counted.
	e := newExec(t, srv.URL, scenario.Step{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertBodyContains, Value: "aaa"}}})
	res := run(e, nil)
	if !res[0].OK() || res[0].Bytes != size {
		t.Errorf("result = %+v, want %d bytes", res[0], size)
	}
	// JSON on a truncated body fails clearly.
	e2 := newExec(t, srv.URL, scenario.Step{Path: "/", Assert: []scenario.Assert{{Type: scenario.AssertJSON, Expr: "a", Value: "1"}}})
	res = run(e2, nil)
	if res[0].OK() || !strings.Contains(res[0].Err.Error(), "not parsed") || res[0].Bytes != size {
		t.Errorf("result = %+v", res[0])
	}
}

// BenchmarkIterate measures executor overhead for a two-step iteration
// with templating, a JSON assert and a JSON extract against a local server.
func BenchmarkIterate(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"token":"tok-123","id":42},"ok":true}`)
	}))
	defer srv.Close()
	e := newExec(b, srv.URL,
		scenario.Step{
			Method: "POST", Path: "/login", Body: `{"user":"{{user}}"}`,
			Headers: map[string]string{"Content-Type": "application/json"},
			Assert: []scenario.Assert{
				{Type: scenario.AssertStatus, Value: "200"},
				{Type: scenario.AssertJSON, Expr: "data.id", Value: "42"},
			},
			Extract: []scenario.Extract{{Var: "token", From: scenario.FromJSON, Expr: "data.token"}},
		},
		scenario.Step{
			Path:    "/items/{{token}}",
			Headers: map[string]string{"Authorization": "Bearer {{token}}"},
		},
	)
	sink := func(r protocol.Result) {
		if !r.OK() {
			b.Error(r.Err)
		}
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			vars := map[string]string{"user": "alice"}
			e.Iterate(ctx, vars, time.Now(), sink)
		}
	})
}

func TestRedirectTargetInFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/abs":
			w.Header().Set("Location", "https://www.example.test:443/")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/rel":
			http.Redirect(w, r, "/next?x=1", http.StatusFound)
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	status200 := []scenario.Assert{{Type: scenario.AssertStatus, Op: "eq", Value: "200"}}

	r := run(newExec(t, srv.URL, scenario.Step{Path: "/abs", Assert: status200}), nil)
	want := "assert status: got 301, want eq 200 (redirects to https://www.example.test:443/)"
	if len(r) != 1 || r[0].Err == nil || r[0].Err.Error() != want {
		t.Fatalf("abs: %+v, want %q", r, want)
	}
	// Relative Location is resolved against the request URL.
	r = run(newExec(t, srv.URL, scenario.Step{Path: "/rel", Assert: status200}), nil)
	if want := "(redirects to " + srv.URL + "/next?x=1)"; r[0].Err == nil || !strings.HasSuffix(r[0].Err.Error(), want) {
		t.Fatalf("rel: %v, want suffix %q", r[0].Err, want)
	}
	// Without a status assertion a 4xx message stays as before.
	r = run(newExec(t, srv.URL, scenario.Step{Path: "/missing"}), nil)
	if r[0].Err == nil || r[0].Err.Error() != "status 404" {
		t.Fatalf("404: %v", r[0].Err)
	}
}

func TestTraceDoesNotChangeOutcome(t *testing.T) {
	srv := testServer(t)
	var traces []*Trace
	sc := &scenario.Scenario{Target: scenario.Target{BaseURL: srv.URL}, Steps: []scenario.Step{{
		Name: "r", Path: "/redirect",
		Assert: []scenario.Assert{{Type: scenario.AssertStatus, Op: "eq", Value: "200"}, {Type: scenario.AssertBodyContains, Op: "contains", Value: "zzz"}},
	}}}
	e, err := New(sc, Options{Trace: func(tr *Trace) { traces = append(traces, tr) }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	res := run(e, nil)
	if len(res) != 1 || res[0].Err == nil || !strings.HasPrefix(res[0].Err.Error(), "assert status: got 302") {
		t.Fatalf("res = %+v", res)
	}
	if len(traces) != 1 {
		t.Fatalf("traces = %d", len(traces))
	}
	tr := traces[0]
	if !tr.Sent || tr.Status != 302 || tr.URL != srv.URL+"/redirect" || tr.Location != srv.URL+"/status/200" {
		t.Errorf("trace = %+v", tr)
	}
	// Every check is evaluated for display, even after the first failure.
	if len(tr.Checks) != 2 || tr.Checks[0].Pass || tr.Checks[1].Pass || tr.Checks[0].Desc != "status eq 200" {
		t.Errorf("checks = %+v", tr.Checks)
	}
	if tr.Err != res[0].Err {
		t.Errorf("trace err %v != result err %v", tr.Err, res[0].Err)
	}
}

func TestCheckURLBlocksBeforeSending(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	sc := &scenario.Scenario{Target: scenario.Target{BaseURL: srv.URL}, Steps: []scenario.Step{{Name: "s", Path: "/x"}}}
	e, err := New(sc, Options{CheckURL: func(u string) error { return fmt.Errorf("blocked %s", u) }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	res := run(e, nil)
	if hits != 0 || res[0].Err == nil || res[0].Err.Error() != "build request: blocked "+srv.URL+"/x" {
		t.Fatalf("hits=%d res=%+v", hits, res)
	}
}
