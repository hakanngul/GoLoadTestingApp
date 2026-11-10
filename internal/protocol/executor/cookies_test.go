package executor

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

	"github.com/coder/websocket"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
)

// cookieSite authenticates by session cookie only:
//
//	POST /login   body "u=<user>": sets cookie session=<user>-<n>, returns a token
//	GET  /me?u=   needs a valid session cookie of user u; answers "me:<cookie value>"
//	GET  /ws      needs a valid session cookie; answers "hi" with "hello <value>"
//	GET  /visit   sets cookie visit=<n> unless the request carries one
//	GET  /ws-open sets cookie wsid=<n> in the handshake unless it carries one
type cookieSite struct {
	*httptest.Server
	logins, visits, wsNew, wsKnown atomic.Int64

	mu    sync.Mutex
	valid map[string]bool
	seen  map[string]int // session value -> authenticated requests
}

func newCookieSite(t *testing.T) *cookieSite {
	t.Helper()
	s := &cookieSite{valid: map[string]bool{}, seen: map[string]int{}}
	session := func(r *http.Request) (string, bool) {
		c, err := r.Cookie("session")
		if err != nil {
			return "", false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.valid[c.Value] {
			return "", false
		}
		s.seen[c.Value]++
		return c.Value, true
	}
	echo := func(w http.ResponseWriter, r *http.Request, greeting string) {
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
			if string(msg) == "hi" {
				c.Write(context.Background(), websocket.MessageText, []byte(greeting))
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		user := strings.TrimPrefix(string(b), "u=")
		v := fmt.Sprintf("%s-%d", user, s.logins.Add(1))
		s.mu.Lock()
		s.valid[v] = true
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Value: v, Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"unused"}`)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		v, ok := session(r)
		if !ok {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		if u := r.URL.Query().Get("u"); !strings.HasPrefix(v, u+"-") {
			http.Error(w, "session of another user: "+v, http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "me:"+v)
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		v, ok := session(r)
		if !ok {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		echo(w, r, "hello "+v)
	})
	mux.HandleFunc("GET /visit", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("visit"); err != nil {
			http.SetCookie(w, &http.Cookie{Name: "visit", Value: fmt.Sprint(s.visits.Add(1)), Path: "/"})
		}
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /ws-open", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("wsid"); err != nil {
			http.SetCookie(w, &http.Cookie{Name: "wsid", Value: fmt.Sprint(s.wsNew.Add(1)), Path: "/"})
		} else {
			s.wsKnown.Add(1)
		}
		_, err := r.Cookie("visit")
		hasVisit := err == nil
		echo(w, r, fmt.Sprintf("hello visit=%v", hasVisit))
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *cookieSite) revokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.valid)
}

func (s *cookieSite) sessions() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.seen))
	for k, v := range s.seen {
		out[k] = v
	}
	return out
}

func loginYAML(base, scope, target string) string {
	return fmt.Sprintf(`name: cookies
target:
  base_url: %s
%s
load:
  stages: [{duration: 1s, target: 1}]
auth:
  type: login
  scope: %s
  inject: false
  login:
    path: /login
    body: 'u={{u}}'
    extract: [{var: token, from: json, expr: token}]
steps:
  - name: me
    path: /me?u={{u}}
    extract: [{var: who, from: regex, expr: 'me:(\S+)'}]
  - name: sock
    type: ws
    path: /ws
    ws:
      messages: [{send: hi, expect: "hello {{who}}"}]
`, base, target, scope)
}

// iterOnce runs one iteration and fails the test on any failed step.
func iterOnce(t *testing.T, ex protocol.Executor, ctx context.Context, user string) []protocol.Result {
	t.Helper()
	var c collector
	ex.Iterate(ctx, map[string]string{"u": user}, time.Now(), c.sink)
	return c.res
}

func allOK(t *testing.T, res []protocol.Result) {
	t.Helper()
	for _, r := range res {
		if r.Err != nil {
			t.Errorf("%s: status %d: %v", r.Step, r.Status, r.Err)
		}
	}
}

// TestLoginCookiesSharedLogin: the session cookie set by auth.login is
// sent on later HTTP steps and WebSocket handshakes, also by virtual
// users that reuse the cached login (scope shared: one login for all).
func TestLoginCookiesSharedLogin(t *testing.T) {
	site := newCookieSite(t)
	ex, closeEx, err := New(parse(t, loginYAML(site.URL, "shared", "")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEx()

	var wg sync.WaitGroup
	for v := 0; v < 6; v++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background() // open model: no session attached
			if v%2 == 0 {               // closed model: one session per VU
				ctx = protocol.WithSession(ctx, protocol.NewSession())
			}
			for i := 0; i < 3; i++ {
				allOK(t, iterOnce(t, ex, ctx, "svc"))
			}
		}()
	}
	wg.Wait()
	if site.logins.Load() != 1 {
		t.Errorf("logins = %d, want 1 (cached)", site.logins.Load())
	}
	// 18 iterations × (GET /me + ws handshake), all with the one session.
	if got := site.sessions(); len(got) != 1 || got["svc-1"] != 36 {
		t.Errorf("sessions = %v, want svc-1: 36", got)
	}
}

// TestLoginCookiesPerUser: with scope per_user every distinct user gets
// (and keeps) the cookies of its own login.
func TestLoginCookiesPerUser(t *testing.T) {
	site := newCookieSite(t)
	ex, closeEx, err := New(parse(t, loginYAML(site.URL, "per_user", "")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEx()

	// One closed-model VU that alternates between two users (CSV rows),
	// and new visitors: each iteration must carry its own user's cookie
	// (/me rejects another user's session; the ws must greet the same).
	vu := protocol.WithSession(context.Background(), protocol.NewSession())
	for i := 0; i < 3; i++ {
		for _, u := range []string{"alice", "bob"} {
			allOK(t, iterOnce(t, ex, vu, u))
			allOK(t, iterOnce(t, ex, context.Background(), u))
		}
	}
	if site.logins.Load() != 2 {
		t.Errorf("logins = %d, want 2 (one per user)", site.logins.Load())
	}
	got := site.sessions()
	if len(got) != 2 || got["alice-1"] != 12 || got["bob-2"] != 12 {
		t.Errorf("sessions = %v", got)
	}
}

// TestReloginReplacesCookies: when the server drops the session, the 401
// invalidates the cached login; the next iteration logs in again and the
// VU's session gets the new cookie.
func TestReloginReplacesCookies(t *testing.T) {
	site := newCookieSite(t)
	ex, closeEx, err := New(parse(t, loginYAML(site.URL, "shared", "")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEx()
	vu := protocol.WithSession(context.Background(), protocol.NewSession())
	allOK(t, iterOnce(t, ex, vu, "svc"))

	site.revokeAll()
	res := iterOnce(t, ex, vu, "svc")
	if len(res) != 1 || res[0].Status != http.StatusUnauthorized {
		t.Fatalf("after revoke: %+v", res)
	}
	res = iterOnce(t, ex, vu, "svc")
	allOK(t, res)
	if len(res) != 4 || res[0].Step != "auth:login" || site.logins.Load() != 2 {
		t.Fatalf("re-login: %+v (logins %d)", res, site.logins.Load())
	}
	if got := site.sessions(); got["svc-2"] != 2 {
		t.Errorf("sessions = %v, want the new cookie svc-2 used twice", got)
	}
}

// TestLoginCookiesOff: with target.cookies: false nothing is stored, so a
// cookie-only login does not authenticate later steps.
func TestLoginCookiesOff(t *testing.T) {
	site := newCookieSite(t)
	ex, closeEx, err := New(parse(t, loginYAML(site.URL, "shared", "  cookies: false")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEx()
	res := iterOnce(t, ex, protocol.WithSession(context.Background(), protocol.NewSession()), "svc")
	if len(res) != 2 || res[0].Err != nil || res[1].Step != "me" || res[1].Status != http.StatusUnauthorized {
		t.Fatalf("res = %+v", res)
	}
}

// TestWebSocketHandshakeCookies: ws handshakes send the jar's cookies
// (set by an HTTP step or by an earlier handshake) and store the ones the
// handshake response sets.
func TestWebSocketHandshakeCookies(t *testing.T) {
	site := newCookieSite(t)
	y := fmt.Sprintf(`name: ws-cookies
target: {base_url: %s}
load: {stages: [{duration: 1s, target: 1}]}
steps:
  - {name: visit, path: /visit}
  - name: sock
    type: ws
    path: /ws-open
    ws:
      messages: [{send: hi, expect: "hello visit=true"}]
`, site.URL)
	ex, closeEx, err := New(parse(t, y), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeEx()

	// A VU: the cookie of the HTTP step reaches the handshake, and the
	// handshake's own cookie comes back on the VU's next handshake.
	vu := protocol.WithSession(context.Background(), protocol.NewSession())
	for i := 0; i < 3; i++ {
		allOK(t, iterOnce(t, ex, vu, ""))
	}
	if site.visits.Load() != 1 || site.wsNew.Load() != 1 || site.wsKnown.Load() != 2 {
		t.Errorf("VU: visits=%d wsNew=%d wsKnown=%d, want 1, 1, 2", site.visits.Load(), site.wsNew.Load(), site.wsKnown.Load())
	}
	// New visitors (no session attached): cookies are shared between the
	// steps of one iteration only.
	for i := 0; i < 2; i++ {
		allOK(t, iterOnce(t, ex, context.Background(), ""))
	}
	if site.visits.Load() != 3 || site.wsNew.Load() != 3 || site.wsKnown.Load() != 2 {
		t.Errorf("visitors: visits=%d wsNew=%d wsKnown=%d, want 3, 3, 2", site.visits.Load(), site.wsNew.Load(), site.wsKnown.Load())
	}

	// A scenario made only of ws steps (plain wsx executor).
	wsOnly := fmt.Sprintf(`name: ws-only
target: {base_url: %s}
load: {stages: [{duration: 1s, target: 1}]}
steps:
  - name: sock
    type: ws
    path: /ws-open
    ws:
      messages: [{send: hi, expect: "hello visit=false"}]
`, site.URL)
	wx, closeWx, err := New(parse(t, wsOnly), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeWx()
	vu = protocol.WithSession(context.Background(), protocol.NewSession())
	for i := 0; i < 3; i++ {
		allOK(t, iterOnce(t, wx, vu, ""))
	}
	if site.wsNew.Load() != 4 || site.wsKnown.Load() != 4 {
		t.Errorf("ws only: wsNew=%d wsKnown=%d, want 4, 4", site.wsNew.Load(), site.wsKnown.Load())
	}
}
