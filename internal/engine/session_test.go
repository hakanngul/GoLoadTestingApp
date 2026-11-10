package engine_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/engine"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/executor"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// slotSite reproduces the reported site exactly: GET /tr/slot without the
// cookie answers "302 Location: /tr/slot" plus Set-Cookie, with the cookie
// 200. Cookie values are distinct, so sharing between users is visible.
type slotSite struct {
	*httptest.Server
	redirects, pages, issued atomic.Int64

	mu      sync.Mutex
	perUser map[string]int // cookie value -> pages served
	unknown int            // pages requested with a cookie nobody was given
}

func newSlotSite(t *testing.T) *slotSite {
	t.Helper()
	s := &slotSite{perUser: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tr/slot" {
			http.NotFound(w, r)
			return
		}
		c, err := r.Cookie("sid")
		if err != nil {
			v := "user-" + strconv.FormatInt(s.issued.Add(1), 10)
			s.mu.Lock()
			s.perUser[v] = 0
			s.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: v, Path: "/", HttpOnly: true})
			w.Header().Set("Location", "/tr/slot")
			w.WriteHeader(http.StatusFound)
			s.redirects.Add(1)
			return
		}
		s.mu.Lock()
		if _, ok := s.perUser[c.Value]; ok {
			s.perUser[c.Value]++
		} else {
			s.unknown++
		}
		s.mu.Unlock()
		s.pages.Add(1)
		fmt.Fprint(w, "<html>slots</html>")
	}))
	t.Cleanup(s.Close)
	return s
}

// runScenario runs the YAML scenario through the real executor and engine
// and returns every step result.
func runScenario(t *testing.T, yaml string) []protocol.Result {
	t.Helper()
	sc, err := scenario.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, yaml)
	}
	exec, closeExec, err := executor.New(sc, executor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeExec()
	var (
		mu  sync.Mutex
		res []protocol.Result
	)
	eng := engine.New(sc.Load, engine.Options{Executor: exec, Sink: func(r protocol.Result) {
		mu.Lock()
		res = append(res, r)
		mu.Unlock()
	}})
	if err := eng.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func slotYAML(base, target, load string) string {
	return fmt.Sprintf(`name: slot
target:
  base_url: %s
%s
load:
%s
steps:
  - name: slot
    path: /tr/slot
    think_time: 40ms
`, base, target, load)
}

const (
	closed3 = "  model: closed\n  stages:\n    - {duration: 50ms, target: 3}\n    - {duration: 700ms, target: 3}"
	open20  = "  model: open\n  stages:\n    - {duration: 50ms, target: 20}\n    - {duration: 700ms, target: 20}"
)

func TestClosedModelKeepsCookiesPerVU(t *testing.T) {
	site := newSlotSite(t)
	res := runScenario(t, slotYAML(site.URL, "", closed3))
	if len(res) < 9 {
		t.Fatalf("only %d iterations ran, want several per VU", len(res))
	}
	for _, r := range res {
		if r.Err != nil || r.Status != 200 {
			t.Fatalf("step failed: %+v", r)
		}
	}
	// One redirect per virtual user for the whole run, not per iteration.
	if got := site.redirects.Load(); got != 3 {
		t.Errorf("redirects = %d, want exactly 3 (one per VU)", got)
	}
	if got := site.pages.Load(); got != int64(len(res)) {
		t.Errorf("pages = %d, results = %d", got, len(res))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.perUser) != 3 || site.unknown != 0 {
		t.Fatalf("cookies issued = %v (unknown %d), want 3 distinct", site.perUser, site.unknown)
	}
	for v, n := range site.perUser {
		// Every VU used its own cookie for all its iterations.
		if n < 3 {
			t.Errorf("cookie %s served %d pages: the VUs do not each keep their own jar", v, n)
		}
	}
}

func TestOpenModelStartsEveryIterationWithoutCookies(t *testing.T) {
	site := newSlotSite(t)
	res := runScenario(t, slotYAML(site.URL, "", open20))
	if len(res) < 8 {
		t.Fatalf("only %d iterations ran", len(res))
	}
	for _, r := range res {
		if r.Err != nil || r.Status != 200 {
			t.Fatalf("step failed: %+v", r)
		}
	}
	n := int64(len(res))
	if site.redirects.Load() != n || site.pages.Load() != n {
		t.Errorf("redirects = %d, pages = %d, want %d each (a new visitor per iteration)", site.redirects.Load(), site.pages.Load(), n)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	for v, c := range site.perUser {
		if c != 1 {
			t.Errorf("cookie %s served %d pages, want 1", v, c)
		}
	}
}

func TestCookiesOffAndRedirectsOff(t *testing.T) {
	site := newSlotSite(t)
	for _, load := range []string{closed3, open20} {
		// cookies: false: the site redirects forever; reported as a loop.
		res := runScenario(t, slotYAML(site.URL, "  cookies: false", load))
		if len(res) == 0 {
			t.Fatal("no results")
		}
		for _, r := range res {
			if r.Err == nil || r.Status != 302 || !strings.HasPrefix(r.Err.Error(), "redirect loop: /tr/slot → /tr/slot") ||
				!strings.Contains(r.Err.Error(), "the server may require cookies") {
				t.Fatalf("cookies off: %+v", r)
			}
		}
		// follow_redirects: false: the 302 is the result, as before
		// (closed model: later iterations of a VU hold the cookie).
		res = runScenario(t, slotYAML(site.URL, "  follow_redirects: false", load))
		var n302 int
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("redirects off: %+v", r)
			}
			if r.Status == 302 {
				n302++
			}
		}
		if n302 == 0 {
			t.Errorf("redirects off: no 302 reported in %d results", len(res))
		}
	}
	if site.pages.Load() == 0 {
		t.Error("closed model with follow_redirects: false never sent the stored cookie")
	}
}
