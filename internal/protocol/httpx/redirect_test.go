package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// slotSite reproduces the site of the bug report: a request to /tr/slot
// without the session cookie is answered with "302 Location: /tr/slot" and
// a Set-Cookie; with the cookie it answers 200. Every issued cookie value
// is distinct.
type slotSite struct {
	*httptest.Server
	redirects atomic.Int64 // 302s served
	pages     atomic.Int64 // 200s served
	issued    atomic.Int64

	mu   sync.Mutex
	seen map[string]int // cookie value -> 200s served with it
}

func newSlotSite(t testing.TB) *slotSite {
	t.Helper()
	s := &slotSite{seen: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tr/slot" {
			http.NotFound(w, r)
			return
		}
		c, err := r.Cookie("sid")
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "v" + strconv.FormatInt(s.issued.Add(1), 10), Path: "/", HttpOnly: true})
			w.Header().Set("Location", "/tr/slot")
			w.WriteHeader(http.StatusFound)
			s.redirects.Add(1)
			return
		}
		s.mu.Lock()
		s.seen[c.Value]++
		s.mu.Unlock()
		s.pages.Add(1)
		fmt.Fprint(w, "slots")
	}))
	t.Cleanup(s.Close)
	return s
}

func newExecSc(t testing.TB, sc *scenario.Scenario, opts Options) *Executor {
	t.Helper()
	e, err := New(sc, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

func runCtx(ctx context.Context, e *Executor) []protocol.Result {
	var c collector
	e.Iterate(ctx, map[string]string{}, time.Now(), c.sink)
	return c.all()
}

func TestSelfRedirectResolvesWithCookie(t *testing.T) {
	site := newSlotSite(t)
	e := newExec(t, site.URL, scenario.Step{Name: "slot", Path: "/tr/slot",
		Assert: []scenario.Assert{{Type: scenario.AssertStatus, Value: "200"}, {Type: scenario.AssertBodyContains, Value: "slots"}}})

	// One virtual user (a session attached, as the closed model does):
	// the first iteration is redirected once, later ones are not.
	vu := protocol.WithSession(context.Background(), protocol.NewSession())
	for i := 0; i < 4; i++ {
		r := runCtx(vu, e)
		if len(r) != 1 || !r[0].OK() || r[0].Status != 200 || r[0].Bytes != 5 {
			t.Fatalf("iteration %d: %+v", i, r)
		}
	}
	if site.redirects.Load() != 1 || site.pages.Load() != 4 {
		t.Fatalf("VU: redirects=%d pages=%d, want 1 and 4", site.redirects.Load(), site.pages.Load())
	}

	// No session attached (the open model): every iteration is a new
	// visitor, so each one is redirected once.
	for i := 0; i < 3; i++ {
		if r := run(e, nil); !r[0].OK() || r[0].Status != 200 {
			t.Fatalf("visitor %d: %+v", i, r)
		}
	}
	if site.redirects.Load() != 4 || site.pages.Load() != 7 {
		t.Fatalf("visitors: redirects=%d pages=%d, want 4 and 7", site.redirects.Load(), site.pages.Load())
	}
}

func TestCookiesDisabledReportsLoop(t *testing.T) {
	site := newSlotSite(t)
	sc := &scenario.Scenario{
		Target: scenario.Target{BaseURL: site.URL, Cookies: boolp(false)},
		Steps:  []scenario.Step{{Name: "slot", Path: "/tr/slot"}},
	}
	r := run(newExecSc(t, sc, Options{}), nil)
	want := "redirect loop: /tr/slot → /tr/slot (the server may require cookies, which target.cookies: false disables)"
	if len(r) != 1 || r[0].Err == nil || r[0].Err.Error() != want || r[0].Status != 302 {
		t.Fatalf("res = %+v\nwant %q", r, want)
	}
	if site.redirects.Load() != 1 {
		t.Errorf("the loop was detected after %d requests, want 1", site.redirects.Load())
	}

	// follow_redirects: false reports the 302 as before.
	sc.Steps[0].FollowRedirects = boolp(false)
	sc.Steps[0].Assert = []scenario.Assert{{Type: scenario.AssertStatus, Value: "200"}}
	r = run(newExecSc(t, sc, Options{}), nil)
	if want := "assert status: got 302, want eq 200 (redirects to " + site.URL + "/tr/slot)"; r[0].Err == nil || r[0].Err.Error() != want || r[0].Status != 302 {
		t.Fatalf("res = %+v\nwant %q", r, want)
	}
}

func TestRedirectLoopAndMaxHops(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case r.URL.Path == "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case r.URL.Path == "/b":
			http.Redirect(w, r, "/a", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/n/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/n/"))
			if n == 0 {
				fmt.Fprint(w, "end")
				return
			}
			http.Redirect(w, r, "/n/"+strconv.Itoa(n-1), http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)

	// A loop is reported as soon as a request would repeat, cookies or not.
	r := run(newExec(t, srv.URL, scenario.Step{Path: "/a"}), nil)
	if r[0].Err == nil || r[0].Err.Error() != "redirect loop: /a → /b → /a" || hits.Load() != 2 {
		t.Fatalf("loop: %+v (hits %d)", r, hits.Load())
	}

	// Exactly max_redirects hops are followed (default 10).
	if r := run(newExec(t, srv.URL, scenario.Step{Path: "/n/10"}), nil); !r[0].OK() || r[0].Status != 200 || r[0].Bytes != 3 {
		t.Fatalf("10 hops: %+v", r)
	}
	r = run(newExec(t, srv.URL, scenario.Step{Path: "/n/11"}), nil)
	if r[0].Err == nil || !strings.HasPrefix(r[0].Err.Error(), "too many redirects (stopped after 10, target.max_redirects): /n/11 → /n/10 → /n/9 → … → ") || r[0].Status != 302 {
		t.Fatalf("11 hops: %+v", r)
	}
	sc := &scenario.Scenario{Target: scenario.Target{BaseURL: srv.URL, MaxRedirects: 2}, Steps: []scenario.Step{{Path: "/n/3"}}}
	r = run(newExecSc(t, sc, Options{}), nil)
	if r[0].Err == nil || r[0].Err.Error() != "too many redirects (stopped after 2, target.max_redirects): /n/3 → /n/2 → /n/1 → /n/0" {
		t.Fatalf("max 2: %+v", r)
	}
}

func TestRedirectMethodSemantics(t *testing.T) {
	type seen struct{ method, body, ctype string }
	var (
		mu   sync.Mutex
		last seen
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/")); err == nil {
			http.Redirect(w, r, "/final", code)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = seen{r.Method, string(b), r.Header.Get("Content-Type")}
		mu.Unlock()
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		method string
		code   int
		want   seen
	}{
		{"POST", 301, seen{"GET", "", ""}},
		{"POST", 302, seen{"GET", "", ""}},
		{"POST", 303, seen{"GET", "", ""}},
		{"PUT", 303, seen{"GET", "", ""}},
		{"POST", 307, seen{"POST", "a=1", "application/x-www-form-urlencoded"}},
		{"POST", 308, seen{"POST", "a=1", "application/x-www-form-urlencoded"}},
		{"DELETE", 307, seen{"DELETE", "a=1", "application/x-www-form-urlencoded"}},
		{"HEAD", 302, seen{"HEAD", "", ""}},
		{"GET", 301, seen{"GET", "", ""}},
	} {
		st := scenario.Step{Method: tc.method, Path: fmt.Sprintf("/r/%d", tc.code)}
		if tc.method != "GET" && tc.method != "HEAD" {
			st.Body = "a={{n}}"
			st.Headers = map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
		}
		var c collector
		newExec(t, srv.URL, st).Iterate(context.Background(), map[string]string{"n": "1"}, time.Now(), c.sink)
		r := c.all()
		mu.Lock()
		got := last
		mu.Unlock()
		if len(r) != 1 || !r[0].OK() || r[0].Status != 200 || got != tc.want {
			t.Errorf("%s %d: result %+v, final request %+v, want %+v", tc.method, tc.code, r, got, tc.want)
		}
	}
}

// otherHost returns srv's URL with the host name "localhost" instead of
// "127.0.0.1": the same server under another host name.
func otherHost(t testing.TB, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return "http://localhost:" + u.Port()
}

// twoHosts starts an "origin" (127.0.0.1) whose /go redirects to /landing
// on "other" (localhost), which records the headers it receives.
func twoHosts(t testing.TB, code int) (origin *httptest.Server, otherURL string, got func() http.Header) {
	t.Helper()
	var (
		mu  sync.Mutex
		hdr http.Header
	)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hdr = r.Header.Clone()
		hdr.Set("X-Seen-Host", r.Host)
		mu.Unlock()
		fmt.Fprint(w, "landed")
	}))
	t.Cleanup(other.Close)
	otherURL = otherHost(t, other)
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/go":
			http.SetCookie(w, &http.Cookie{Name: "origin_sid", Value: "o1", Path: "/"})
			http.Redirect(w, r, otherURL+"/landing", code)
		case "/same":
			http.Redirect(w, r, "/echo", code)
		default:
			w.Header().Set("X-Auth", r.Header.Get("Authorization"))
			w.Header().Set("X-Cookie", r.Header.Get("Cookie"))
			w.Header().Set("X-Key", r.Header.Get("X-Api-Key"))
			fmt.Fprint(w, "echo")
		}
	}))
	t.Cleanup(origin.Close)
	return origin, otherURL, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return hdr
	}
}

func TestCredentialsStayOnTheirHost(t *testing.T) {
	origin, _, got := twoHosts(t, http.StatusFound)
	hdrs := map[string]string{
		"Authorization": "Bearer secret", "Cookie": "mine=1", "X-Api-Key": "k-123",
		"X-Trace": "t1", "Host": "virtual.test",
	}
	sc := &scenario.Scenario{
		Target: scenario.Target{BaseURL: origin.URL},
		Steps: []scenario.Step{
			{Name: "cross", Path: "/go", Headers: hdrs},
			{Name: "same", Path: "/same", Headers: hdrs,
				Extract: []scenario.Extract{
					{Var: "auth", From: scenario.FromHeader, Expr: "X-Auth"},
					{Var: "cookie", From: scenario.FromHeader, Expr: "X-Cookie"},
					{Var: "key", From: scenario.FromHeader, Expr: "X-Key"},
				}},
		},
	}
	e := newExecSc(t, sc, Options{SensitiveHeaders: []string{"x-api-key"}})
	var c collector
	vars := map[string]string{}
	e.Iterate(context.Background(), vars, time.Now(), c.sink)
	r := c.all()
	if len(r) != 2 || !r[0].OK() || !r[1].OK() || r[0].Bytes != 6 {
		t.Fatalf("results = %+v", r)
	}
	h := got()
	for _, k := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if v := h.Get(k); v != "" {
			t.Errorf("%s was forwarded to another host: %q", k, v)
		}
	}
	if h.Get("X-Trace") != "t1" || !strings.HasPrefix(h.Get("User-Agent"), "GoLoad") {
		t.Errorf("ordinary headers were not forwarded: %v", h)
	}
	if strings.HasPrefix(h.Get("X-Seen-Host"), "virtual.test") {
		t.Errorf("the Host override followed the redirect to another host: %q", h.Get("X-Seen-Host"))
	}
	// A redirect on the same host keeps credentials; the explicit cookie
	// is merged with the jar's (set by step 1) and listed first.
	if vars["auth"] != "Bearer secret" || vars["key"] != "k-123" || vars["cookie"] != "mine=1; origin_sid=o1" {
		t.Errorf("same-host redirect: auth=%q key=%q cookie=%q", vars["auth"], vars["key"], vars["cookie"])
	}
}

func TestExplicitCookieWinsOverJar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "a", Value: "jar", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "b", Value: "jar", Path: "/"})
		}
		fmt.Fprint(w, r.Header.Get("Cookie"))
	}))
	t.Cleanup(srv.Close)
	body := func(want string) []scenario.Assert {
		return []scenario.Assert{{Type: scenario.AssertBodyContains, Value: want}}
	}
	e := newExec(t, srv.URL,
		scenario.Step{Name: "set", Path: "/set"},
		scenario.Step{Name: "jar", Path: "/x", Assert: body("a=jar; b=jar")},
		scenario.Step{Name: "merge", Path: "/x", Headers: map[string]string{"cookie": "b={{v}}; c=3"}, Assert: body("b=mine; c=3; a=jar")},
	)
	var c collector
	e.Iterate(context.Background(), map[string]string{"v": "mine"}, time.Now(), c.sink)
	for _, r := range c.all() {
		if !r.OK() {
			t.Errorf("%s: %v", r.Step, r.Err)
		}
	}
	if len(c.all()) != 3 {
		t.Fatalf("results = %+v", c.all())
	}
}

func TestRedirectAllowRules(t *testing.T) {
	origin, otherURL, _ := twoHosts(t, http.StatusMovedPermanently)
	sc := func() *scenario.Scenario {
		return &scenario.Scenario{Target: scenario.Target{BaseURL: origin.URL}, Steps: []scenario.Step{{Name: "go", Path: "/go"}}}
	}
	errNope := errors.New("nope")
	var asked []string
	allowNone := func(u string) error { asked = append(asked, u); return errNope }
	allowAll := func(string) error { return nil }
	blockedMsg := func(reason string) string {
		return "redirect to " + otherURL + "/landing not allowed (" + reason + ")"
	}

	// No allowlist (CLI): any host.
	if r := run(newExecSc(t, sc(), Options{}), nil); !r[0].OK() || r[0].Status != 200 {
		t.Fatalf("unrestricted: %+v", r)
	}
	// Allowlist allows the new host.
	if r := run(newExecSc(t, sc(), Options{CheckRedirect: allowAll}), nil); !r[0].OK() || r[0].Status != 200 {
		t.Fatalf("allowed: %+v", r)
	}
	// Allowlist rejects it: the step fails with the 301 and nothing is
	// sent to the other host.
	r := run(newExecSc(t, sc(), Options{CheckRedirect: allowNone}), nil)
	var re *RedirectError
	if r[0].Err == nil || r[0].Err.Error() != blockedMsg("host not in allowlist") || r[0].Status != 301 ||
		!errors.As(r[0].Err, &re) || !errors.Is(r[0].Err, errNope) {
		t.Fatalf("blocked: %+v", r)
	}
	if len(asked) != 1 || asked[0] != otherURL+"/landing" {
		t.Errorf("allowlist asked about %v", asked)
	}
	// A redirect on the same host is never a new target: not asked.
	asked = nil
	same := sc()
	same.Steps[0].Path = "/same"
	if r := run(newExecSc(t, same, Options{CheckRedirect: allowNone}), nil); !r[0].OK() || len(asked) != 0 {
		t.Fatalf("same host: %+v asked %v", r, asked)
	}

	// Workers (no allowlist): only the scenario's own hosts.
	r = run(newExecSc(t, sc(), Options{RestrictRedirects: true}), nil)
	if r[0].Err == nil || !strings.HasPrefix(r[0].Err.Error(), blockedMsg("host is not a target of this scenario")[:len(blockedMsg(""))-1]) || r[0].Status != 301 {
		t.Fatalf("restricted: %+v", r)
	}
	own := sc()
	own.Steps = append(own.Steps, scenario.Step{Name: "abs", Path: otherURL + "/landing"})
	if r := run(newExecSc(t, own, Options{RestrictRedirects: true}), nil); len(r) != 2 || !r[0].OK() || !r[1].OK() {
		t.Fatalf("restricted, own host: %+v", r)
	}
	// An allowlist takes precedence over the restriction.
	if r := run(newExecSc(t, sc(), Options{CheckRedirect: allowAll, RestrictRedirects: true}), nil); !r[0].OK() {
		t.Fatalf("allowlist + restrict: %+v", r)
	}

	// Only http(s) targets are ever followed.
	weird := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "ftp://files.test/x")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(weird.Close)
	r = run(newExec(t, weird.URL, scenario.Step{Path: "/"}), nil)
	if r[0].Err == nil || r[0].Err.Error() != "redirect to ftp://files.test/x not allowed (not an http(s) URL)" {
		t.Fatalf("ftp: %+v", r)
	}
}

func TestJarCookiesAreScopedToTheirHost(t *testing.T) {
	origin, _, got := twoHosts(t, http.StatusFound)
	e := newExec(t, origin.URL, scenario.Step{Name: "go", Path: "/go"})
	vu := protocol.WithSession(context.Background(), protocol.NewSession())
	for i := 0; i < 2; i++ { // the second time the jar holds origin_sid
		if r := runCtx(vu, e); !r[0].OK() {
			t.Fatalf("res = %+v", r)
		}
	}
	if c := got().Get("Cookie"); c != "" {
		t.Errorf("the origin's cookie was sent to another host: %q", c)
	}
}

func TestDefaultUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.UserAgent())
	}))
	t.Cleanup(srv.Close)
	if got := protocol.UserAgent(); got != "GoLoad" {
		t.Fatalf("UserAgent() = %q before SetVersion", got)
	}
	protocol.SetVersion("1.2.3")
	t.Cleanup(func() { protocol.SetVersion("") })
	contains := func(v string) []scenario.Assert {
		return []scenario.Assert{{Type: scenario.AssertBodyContains, Value: v}}
	}
	e := newExec(t, srv.URL,
		scenario.Step{Name: "default", Path: "/", Assert: contains("GoLoad/1.2.3")},
		scenario.Step{Name: "override", Path: "/", Headers: map[string]string{"user-agent": "probe/9"}, Assert: contains("probe/9")},
	)
	for _, r := range run(e, nil) {
		if !r.OK() {
			t.Errorf("%s: %v", r.Step, r.Err)
		}
	}
	// A target-level User-Agent also replaces the default.
	sc := &scenario.Scenario{
		Target: scenario.Target{BaseURL: srv.URL, Headers: map[string]string{"User-Agent": "team/1"}},
		Steps:  []scenario.Step{{Path: "/", Assert: contains("team/1")}},
	}
	if r := run(newExecSc(t, sc, Options{}), nil); !r[0].OK() {
		t.Errorf("target UA: %v", r[0].Err)
	}
}

func TestTraceRecordsHops(t *testing.T) {
	site := newSlotSite(t)
	var traces []*Trace
	sc := &scenario.Scenario{Target: scenario.Target{BaseURL: site.URL}, Steps: []scenario.Step{{Name: "slot", Path: "/tr/slot"}}}
	e := newExecSc(t, sc, Options{Trace: func(tr *Trace) { traces = append(traces, tr) }})
	if r := run(e, nil); !r[0].OK() {
		t.Fatalf("res = %+v", r)
	}
	tr := traces[0]
	if tr.Status != 200 || tr.URL != site.URL+"/tr/slot" || tr.FinalURL != site.URL+"/tr/slot" || tr.Location != "" || string(tr.Body) != "slots" {
		t.Errorf("trace = %+v", tr)
	}
	if len(tr.Hops) != 1 {
		t.Fatalf("hops = %+v", tr.Hops)
	}
	h := tr.Hops[0]
	if h.Status != 302 || h.Method != "GET" || h.URL != site.URL+"/tr/slot" || h.Location != site.URL+"/tr/slot" ||
		len(h.CookiesSent) != 0 || len(h.CookiesSet) != 1 || h.CookiesSet[0] != "sid" {
		t.Errorf("hop = %+v", h)
	}
	if len(tr.CookiesSent) != 1 || tr.CookiesSent[0] != "sid" || len(tr.CookiesSet) != 0 {
		t.Errorf("final cookies: sent %v set %v", tr.CookiesSent, tr.CookiesSet)
	}
	// The trace keeps the first request's headers, without the jar cookie
	// that only the second request carried.
	if tr.ReqHeader.Get("Cookie") != "" {
		t.Errorf("first request cookie = %q", tr.ReqHeader.Get("Cookie"))
	}
}

// TestNoJarSharingBetweenVUs runs many virtual users concurrently through
// one Executor (run with -race): each must keep its own cookie.
func TestNoJarSharingBetweenVUs(t *testing.T) {
	site := newSlotSite(t)
	e := newExec(t, site.URL, scenario.Step{Name: "slot", Path: "/tr/slot"})
	const vus, iters = 64, 20
	var wg sync.WaitGroup
	var failed atomic.Int64
	for v := 0; v < vus; v++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := protocol.WithSession(context.Background(), protocol.NewSession())
			for i := 0; i < iters; i++ {
				e.Iterate(ctx, map[string]string{}, time.Now(), func(r protocol.Result) {
					if !r.OK() || r.Status != 200 {
						failed.Add(1)
					}
				})
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d failed steps", failed.Load())
	}
	if site.redirects.Load() != vus || site.pages.Load() != vus*iters {
		t.Fatalf("redirects=%d pages=%d, want %d and %d", site.redirects.Load(), site.pages.Load(), vus, vus*iters)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.seen) != vus {
		t.Fatalf("%d distinct cookies were used, want %d (one per VU)", len(site.seen), vus)
	}
	for v, n := range site.seen {
		if n != iters {
			t.Errorf("cookie %s was used for %d pages, want %d: jars are shared", v, n, iters)
		}
	}
}

// BenchmarkJar measures what the per-VU cookie jar adds to a request:
// "off" disables cookies, "empty" is a VU whose target never sets a
// cookie, "2cookies" a VU that sends two cookies with every request.
func BenchmarkJar(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "0123456789abcdef0123456789abcdef", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "lang", Value: "tr", Path: "/"})
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	mk := func(cookies bool) *Executor {
		sc := &scenario.Scenario{Target: scenario.Target{BaseURL: srv.URL, Cookies: &cookies}, Steps: []scenario.Step{{Name: "get", Path: "/"}}}
		return newExecSc(b, sc, Options{})
	}
	bench := func(e *Executor, seed bool) func(*testing.B) {
		return func(b *testing.B) {
			ctx := protocol.WithSession(context.Background(), protocol.NewSession())
			if seed {
				set := newExec(b, srv.URL, scenario.Step{Path: "/set"})
				set.Iterate(ctx, nil, time.Time{}, func(protocol.Result) {})
			}
			sink := func(r protocol.Result) {
				if r.Err != nil {
					b.Fatal(r.Err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e.Iterate(ctx, nil, time.Time{}, sink)
			}
		}
	}
	b.Run("off", bench(mk(false), false))
	b.Run("empty", bench(mk(true), false))
	b.Run("2cookies", bench(mk(true), true))
}
