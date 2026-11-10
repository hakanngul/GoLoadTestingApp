package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
)

// authServer issues tokens on POST /login ({"u": user}; user "bad" is
// rejected), accepts them on GET /data (401 otherwise or once revoked),
// echoes headers on GET /echo and accepts ws on GET /ws only with a valid
// Authorization header.
type authServer struct {
	*httptest.Server
	logins     atomic.Int64
	dataHits   atomic.Int64
	loginDelay time.Duration

	mu      sync.Mutex
	perUser map[string]int
	valid   map[string]bool
}

func newAuthServer(t *testing.T, loginDelay time.Duration) *authServer {
	t.Helper()
	s := &authServer{loginDelay: loginDelay, perUser: map[string]int{}, valid: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ U string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if s.loginDelay > 0 {
			time.Sleep(s.loginDelay)
		}
		s.logins.Add(1)
		if in.U == "bad" {
			http.Error(w, "wrong password", http.StatusForbidden)
			return
		}
		s.mu.Lock()
		s.perUser[in.U]++
		tok := fmt.Sprintf("tok-%s-%d", in.U, s.perUser[in.U])
		s.valid[tok] = true
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"access_token":%q}}`, tok)
	})
	mux.HandleFunc("GET /data", func(w http.ResponseWriter, r *http.Request) {
		s.dataHits.Add(1)
		if !s.ok(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("data for " + r.Header.Get("Authorization")))
	})
	mux.HandleFunc("GET /echo", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "auth=[%s] x=[%s]", r.Header.Get("Authorization"), r.Header.Get("X-Token"))
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		if !s.ok(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Write(context.Background(), websocket.MessageText, []byte("hello "+r.Header.Get("Authorization")))
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *authServer) ok(r *http.Request) bool {
	tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	return found && s.valid[tok]
}

func (s *authServer) revokeAll() {
	s.mu.Lock()
	clear(s.valid)
	s.mu.Unlock()
}

const authLoginYAML = `
name: auth
target:
  base_url: %BASE%
  timeout: 5s
load:
  model: closed
  stages: [{duration: 1s, target: 1}]
auth:
  type: login
  %EXTRA%
  login:
    path: /login
    body: '{"u":"{{user}}"}'
    extract: [{var: token, from: json, expr: data.access_token}]
steps:
  - name: data
    path: /data
`

func authExecutor(t *testing.T, base, y, extra string) protocol.Executor {
	t.Helper()
	y = strings.ReplaceAll(y, "%BASE%", base)
	y = strings.ReplaceAll(y, "%EXTRA%", extra)
	ex, closeEx, err := New(parse(t, y), Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(closeEx)
	return ex
}

func iterate(ex protocol.Executor, vars map[string]string, c *collector) {
	ex.Iterate(context.Background(), vars, time.Now(), c.sink)
}

func (c *collector) count(step string, failed bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.res {
		if r.Step == step && (r.Err != nil) == failed {
			n++
		}
	}
	return n
}

func TestAuthPerUserLogsInOncePerUser(t *testing.T) {
	srv := newAuthServer(t, 20*time.Millisecond)
	ex := authExecutor(t, srv.URL, authLoginYAML, "scope: per_user")
	c := &collector{}
	const users, workers, perWorker = 5, 20, 15
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				iterate(ex, map[string]string{"user": fmt.Sprintf("u%d", (w+i)%users)}, c)
			}
		}(w)
	}
	wg.Wait()
	if got := srv.logins.Load(); got != users {
		t.Errorf("logins = %d, want %d (one per user)", got, users)
	}
	if got := c.count("auth:login", false); got != users {
		t.Errorf("auth:login results = %d, want %d", got, users)
	}
	if got := c.count("data", false); got != workers*perWorker {
		t.Errorf("ok data results = %d, want %d; results: %s", got, workers*perWorker, c.steps())
	}
}

func TestAuthSharedSingleflight(t *testing.T) {
	srv := newAuthServer(t, 100*time.Millisecond)
	ex := authExecutor(t, srv.URL, authLoginYAML, "scope: shared")
	c := &collector{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			iterate(ex, map[string]string{"user": fmt.Sprintf("u%d", i)}, c)
		}(i)
	}
	wg.Wait()
	if got := srv.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
	if got := c.count("auth:login", false); got != 1 {
		t.Errorf("auth:login results = %d, want 1", got)
	}
	if got := c.count("data", false); got != 50 {
		t.Errorf("ok data = %d, want 50: %s", got, c.steps())
	}
}

func TestAuthRefresh(t *testing.T) {
	srv := newAuthServer(t, 0)
	ex := authExecutor(t, srv.URL, authLoginYAML, "refresh: 200ms")
	c := &collector{}
	vars := func() map[string]string { return map[string]string{"user": "a"} }
	iterate(ex, vars(), c)
	iterate(ex, vars(), c)
	if got := srv.logins.Load(); got != 1 {
		t.Fatalf("logins before expiry = %d, want 1", got)
	}
	time.Sleep(250 * time.Millisecond)
	iterate(ex, vars(), c)
	iterate(ex, vars(), c)
	if got := srv.logins.Load(); got != 2 {
		t.Fatalf("logins after expiry = %d, want 2", got)
	}
	if got := c.count("data", false); got != 4 {
		t.Errorf("ok data = %d: %s", got, c.steps())
	}
}

func TestAuth401InvalidatesAndRelogins(t *testing.T) {
	srv := newAuthServer(t, 0)
	ex := authExecutor(t, srv.URL, authLoginYAML, "")
	c := &collector{}
	vars := func() map[string]string { return map[string]string{"user": "a"} }
	iterate(ex, vars(), c)
	srv.revokeAll()
	iterate(ex, vars(), c) // cached token rejected -> 401, cache dropped
	iterate(ex, vars(), c) // logs in again
	want := "auth:login,data,data!status 401,auth:login,data"
	if got := c.steps(); got != want {
		t.Fatalf("results = %s, want %s", got, want)
	}
	if got := srv.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestAuthFailedLoginEndsIteration(t *testing.T) {
	srv := newAuthServer(t, 0)
	ex := authExecutor(t, srv.URL, authLoginYAML, "")
	c := &collector{}
	iterate(ex, map[string]string{"user": "bad"}, c)
	iterate(ex, map[string]string{"user": "bad"}, c) // not cached: retried
	if got := c.count("auth:login", true); got != 2 {
		t.Fatalf("failed auth:login = %d: %s", got, c.steps())
	}
	if got := srv.dataHits.Load(); got != 0 {
		t.Errorf("steps executed after failed login: %d", got)
	}
	c.mu.Lock()
	st := c.res[0].Status
	c.mu.Unlock()
	if st != http.StatusForbidden {
		t.Errorf("status = %d, want 403", st)
	}
	if got := srv.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestAuthCoordinatedOmission(t *testing.T) {
	srv := newAuthServer(t, 0)
	ex := authExecutor(t, srv.URL, authLoginYAML, "")
	c := &collector{}
	late := time.Now().Add(-time.Second)
	ex.Iterate(context.Background(), map[string]string{"user": "a"}, late, c.sink)
	ex.Iterate(context.Background(), map[string]string{"user": "a"}, time.Now().Add(-time.Second), c.sink)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.res) != 3 {
		t.Fatalf("results: %+v", c.res)
	}
	if c.res[0].Latency < time.Second {
		t.Errorf("login latency %v not measured from intended", c.res[0].Latency)
	}
	if c.res[1].Latency >= time.Second {
		t.Errorf("first step after login measured from intended: %v", c.res[1].Latency)
	}
	if c.res[2].Latency < time.Second {
		t.Errorf("cache hit: first step %v not measured from intended", c.res[2].Latency)
	}
}

func TestAuthInjectionAndOverride(t *testing.T) {
	srv := newAuthServer(t, 0)
	y := strings.Replace(authLoginYAML, `steps:
  - name: data
    path: /data
`, `steps:
  - name: injected
    path: /echo
    assert: [{type: body_contains, value: "auth=[Bearer tok-a-1]"}]
  - name: override
    path: /echo
    headers: {authorization: Custom xyz}
    assert: [{type: body_contains, value: "auth=[Custom xyz]"}]
  - name: ws
    type: ws
    path: /ws
    ws:
      messages: [{expect: "hello Bearer tok-a-1"}]
`, 1)
	ex := authExecutor(t, srv.URL, y, "")
	c := &collector{}
	iterate(ex, map[string]string{"user": "a"}, c)
	if got, want := c.steps(), "auth:login,injected,override,ws,ws:1"; got != want {
		t.Fatalf("results = %s, want %s", got, want)
	}
}

func TestAuthInjectFalseExposesVars(t *testing.T) {
	srv := newAuthServer(t, 0)
	y := strings.Replace(authLoginYAML, `steps:
  - name: data
    path: /data
`, `steps:
  - name: echo
    path: /echo
    headers: {X-Token: "{{token}}"}
    assert: [{type: body_contains, value: "auth=[] x=[tok-a-1]"}]
`, 1)
	ex := authExecutor(t, srv.URL, y, "inject: false")
	c := &collector{}
	vars := map[string]string{"user": "a"}
	iterate(ex, vars, c)
	if got := c.steps(); got != "auth:login,echo" {
		t.Fatalf("results = %s", got)
	}
	if vars["token"] != "tok-a-1" {
		t.Errorf("vars not exposed: %v", vars)
	}
}

const echoYAML = `
name: echo
target:
  base_url: %BASE%
load:
  stages: [{duration: 1s, target: 1}]
auth: %EXTRA%
steps:
  - name: echo
    path: /echo
    assert: [{type: body_contains, value: "%WANT%"}]
`

func TestAuthBearerAndBasic(t *testing.T) {
	srv := newAuthServer(t, 0)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		auth string
		vars map[string]string
		want string
	}{
		{`{type: bearer, token: "{{api_key}}"}`, map[string]string{"api_key": "k1"}, "auth=[Bearer k1]"},
		{`{type: bearer, token: fixed}`, nil, "auth=[Bearer fixed]"},
		{`{type: bearer, token: "{{k}}", header: X-Token, value: "Key {{token}}"}`, map[string]string{"k": "z"}, "auth=[] x=[Key z]"},
		{`{type: basic, username: alice, password: "s3:cr"}`, nil, "auth=[Basic " + b64("alice:s3:cr") + "]"},
		{`{type: basic, username: "{{u}}", password: "{{p}}"}`, map[string]string{"u": "bob", "p": "pw"}, "auth=[Basic " + b64("bob:pw") + "]"},
	}
	for _, tc := range cases {
		t.Run(tc.auth, func(t *testing.T) {
			y := strings.ReplaceAll(echoYAML, "%WANT%", tc.want)
			ex := authExecutor(t, srv.URL, y, tc.auth)
			c := &collector{}
			iterate(ex, tc.vars, c)
			if got := c.steps(); got != "echo" {
				t.Fatalf("results = %s", got)
			}
		})
	}
}

func TestNoAuthUnchanged(t *testing.T) {
	srv := newAuthServer(t, 0)
	y := strings.ReplaceAll(strings.Replace(echoYAML, "auth: %EXTRA%\n", "", 1), "%WANT%", "auth=[]")
	ex := authExecutor(t, srv.URL, y, "")
	if _, ok := ex.(*httpx.Executor); !ok {
		t.Fatalf("executor without auth is %T, want *httpx.Executor", ex)
	}
	c := &collector{}
	iterate(ex, nil, c)
	if got := c.steps(); got != "echo" {
		t.Fatalf("results = %s", got)
	}
}
