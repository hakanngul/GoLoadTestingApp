package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/api"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// httpEnv is the real API wrapped by HTTPHandler on /mcp, behind httptest.
type httpEnv struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string // Authorization header of every in-process API call
}

func newHTTPEnv(t *testing.T, authToken string, mutate ...func(*HTTPOptions)) *httpEnv {
	t.Helper()
	st := store.NewMemory()
	mgr := runner.NewManager(runner.Options{Store: st})
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })
	apiH := api.NewServer(api.Config{Store: st, Runner: mgr, Version: "vtest", AuthToken: authToken})

	e := &httpEnv{}
	recording := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.auth = append(e.auth, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		e.mu.Unlock()
		apiH.ServeHTTP(w, r)
	})
	opts := HTTPOptions{Version: "test", AuthRequired: authToken != "", PollInterval: 20 * time.Millisecond}
	for _, m := range mutate {
		m(&opts)
	}
	mux := http.NewServeMux()
	mux.Handle(HTTPPath, HTTPHandler(recording, opts))
	mux.Handle("/", recording)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (e *httpEnv) connect(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   e.srv.URL + HTTPPath,
		HTTPClient: &http.Client{Transport: bearerRT{token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolSet(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, tl := range res.Tools {
		m[tl.Name] = true
	}
	return m
}

func TestHTTPListTools(t *testing.T) {
	e := newHTTPEnv(t, "tok-1")
	tools := toolSet(t, e.connect(t, "tok-1"))
	for _, n := range []string{"list_tests", "create_test", "start_run", "wait_run"} {
		if !tools[n] {
			t.Errorf("missing tool %s (have %v)", n, tools)
		}
	}
}

func TestHTTPAuthRequired(t *testing.T) {
	e := newHTTPEnv(t, "tok-1")
	for name, hdr := range map[string]string{
		"none": "", "wrong": "Bearer nope", "basic": "Basic dG9rLTE=",
	} {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+HTTPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: no WWW-Authenticate header", name)
		}
	}
	// The SDK client cannot connect with a wrong token either.
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.srv.URL + HTTPPath, HTTPClient: &http.Client{Transport: bearerRT{"nope"}}, MaxRetries: -1,
	}, nil); err == nil {
		t.Error("connect with a wrong token succeeded")
	}
}

func TestHTTPOpenWithoutAuth(t *testing.T) {
	e := newHTTPEnv(t, "")
	if !toolSet(t, e.connect(t, ""))["list_tests"] {
		t.Error("no tools without auth")
	}
}

func TestHTTPToolCallsForwardToken(t *testing.T) {
	e := newHTTPEnv(t, "tok-1")
	cs := e.connect(t, "tok-1")
	ctx := context.Background()

	yaml := `name: remote
target:
  base_url: http://127.0.0.1:9
load:
  model: closed
  stages: [{duration: 2s, target: 1}]
steps:
  - name: ping
    path: /ping
    assert:
      - {type: status, value: "200"}
`
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "create_test", Arguments: map[string]any{"name": "remote", "yaml": yaml}})
	if err != nil || res.IsError {
		t.Fatalf("create_test: err=%v res=%+v", err, res)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_tests", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_tests: err=%v res=%+v", err, res)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "remote") {
		t.Errorf("list_tests does not show the created test: %s", text)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	var posts int
	for _, a := range e.auth {
		if !strings.HasSuffix(a, " Bearer tok-1") {
			t.Errorf("API call without the caller's token: %q", a)
		}
		if strings.HasPrefix(a, "POST /api/tests ") {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("POST /api/tests calls = %d, want 1 (calls: %v)", posts, e.auth)
	}
}

func TestHTTPReadOnlyHidesMutatingTools(t *testing.T) {
	e := newHTTPEnv(t, "tok-1", func(o *HTTPOptions) { o.ReadOnly = true })
	tools := toolSet(t, e.connect(t, "tok-1"))
	if !tools["list_tests"] {
		t.Error("read tool list_tests missing")
	}
	for _, n := range []string{"create_test", "update_test", "start_run", "stop_run", "set_baseline", "create_suite", "run_suite"} {
		if tools[n] {
			t.Errorf("mutating tool %s visible in read-only mode", n)
		}
	}
}

// createToken makes a scoped API token through the API with the master token.
func (e *httpEnv) createToken(t *testing.T, master, name, scope string) string {
	t.Helper()
	body := strings.NewReader(`{"name":"` + name + `","scope":"` + scope + `"}`)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/tokens", body)
	req.Header.Set("Authorization", "Bearer "+master)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %d %v", res.StatusCode, err)
	}
	return out.Secret
}

func TestHTTPReadScopedTokenSeesReadOnlyTools(t *testing.T) {
	e := newHTTPEnv(t, "tok-1")
	read := e.createToken(t, "tok-1", "viewer", "read")
	run := e.createToken(t, "tok-1", "agent", "run")
	if tools := toolSet(t, e.connect(t, read)); tools["start_run"] || tools["create_test"] || !tools["list_tests"] {
		t.Errorf("read token tools = %v", tools)
	}
	if tools := toolSet(t, e.connect(t, run)); !tools["start_run"] {
		t.Errorf("run token lacks start_run: %v", tools)
	}
}

func TestHTTPForbiddenMessageAndLinks(t *testing.T) {
	e := newHTTPEnv(t, "tok-1")
	run := e.createToken(t, "tok-1", "agent", "run")
	cs := e.connect(t, run)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_test", Arguments: map[string]any{
		"name": "x", "yaml": "name: x\ntarget: {base_url: http://127.0.0.1:1}\nload: {stages: [{duration: 1s, target: 1}]}\nsteps: [{path: /}]\n"}})
	if err != nil {
		t.Fatal(err)
	}
	txt := textOf(res)
	if !res.IsError || !strings.Contains(txt, "not allowed") || strings.Contains(txt, "GOLOAD_TOKEN") {
		t.Errorf("403 message = %q", txt)
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_server_info", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if txt := textOf(res); !strings.Contains(txt, e.srv.URL) || strings.Contains(txt, "inprocess") {
		t.Errorf("server_info should link to the request's host: %q", txt)
	}
}

func TestRequestBaseURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://10.0.0.5:8080/mcp", nil)
	if got := requestBaseURL(r); got != "http://10.0.0.5:8080" {
		t.Errorf("plain = %q", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "goload.internal.example, proxy")
	if got := requestBaseURL(r); got != "https://goload.internal.example" {
		t.Errorf("forwarded = %q", got)
	}
}
