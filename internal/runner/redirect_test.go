package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// redirectPair starts an origin (reached as 127.0.0.1) whose /go answers
// with a 302 to /landing on a second server reached as "localhost", i.e.
// another host as far as the allowlist is concerned. /self on the origin
// redirects to itself until the cookie it sets comes back.
func redirectPair(t *testing.T) (origin *httptest.Server, otherURL string, otherHits *atomic.Int64) {
	t.Helper()
	otherHits = new(atomic.Int64)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		_, _ = io.WriteString(w, "landed")
	}))
	t.Cleanup(other.Close)
	u, err := url.Parse(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	otherURL = "http://localhost:" + u.Port()
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/go":
			http.Redirect(w, r, otherURL+"/landing", http.StatusFound)
		case "/self":
			if _, err := r.Cookie("sid"); err != nil {
				http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/"})
				http.Redirect(w, r, "/self", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(origin.Close)
	return origin, otherURL, otherHits
}

func allowing(t *testing.T, entries string) func(string) error {
	t.Helper()
	list, err := allowlist.Parse(entries)
	if err != nil {
		t.Fatal(err)
	}
	return list.Check
}

// errorMessages returns the run's error group messages (raw samples).
func errorMessages(t *testing.T, st *fakeStore, runID string) string {
	t.Helper()
	groups, err := st.ListErrorGroups(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range groups {
		out = append(out, g.Message+" | "+g.Sample)
	}
	return strings.Join(out, "\n")
}

// TestRedirectHopsAreCheckedAgainstAllowlist: in a real (local) run a
// redirect to another host is followed only if the allowlist has it.
func TestRedirectHopsAreCheckedAgainstAllowlist(t *testing.T) {
	origin, otherURL, otherHits := redirectPair(t)
	y := scenarioYAML(origin.URL, "300ms", 20, "/go", "")

	// Both hosts allowlisted: followed, the step reports the final 200.
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Allow = allowing(t, "127.0.0.1,localhost") })
	run, err := m.Start(context.Background(), testFor(y), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	final := waitFinished(t, st, run.ID, 10*time.Second)
	if final.Summary == nil || final.Summary.Requests == 0 || final.Summary.Errors != 0 {
		t.Fatalf("allowed: summary %+v errors: %s", final.Summary, errorMessages(t, st, run.ID))
	}
	if otherHits.Load() != final.Summary.Requests {
		t.Errorf("allowed: %d requests reached the redirect target, want %d", otherHits.Load(), final.Summary.Requests)
	}

	// Only the configured target allowlisted: the run starts (its target
	// is fine) but no request ever reaches the other host.
	otherHits.Store(0)
	st = newFakeStore()
	m = newManager(t, st, func(o *Options) { o.Allow = allowing(t, "127.0.0.1") })
	run, err = m.Start(context.Background(), testFor(y), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	final = waitFinished(t, st, run.ID, 10*time.Second)
	if final.Summary == nil || final.Summary.Requests == 0 || final.Summary.Errors != final.Summary.Requests {
		t.Fatalf("blocked: summary %+v", final.Summary)
	}
	if otherHits.Load() != 0 {
		t.Errorf("blocked: %d requests reached a host that is not on the allowlist", otherHits.Load())
	}
	want := "redirect to " + otherURL + "/landing not allowed (host not in allowlist)"
	if msgs := errorMessages(t, st, run.ID); !strings.Contains(msgs, want) {
		t.Errorf("blocked: error groups %q lack %q", msgs, want)
	}
}

// TestRedirectsOnWorkers: workers do not have the master's allowlist, so
// they follow redirects only within the scenario's own hosts, unless they
// were started with an allowlist of their own.
func TestRedirectsOnWorkers(t *testing.T) {
	origin, otherURL, otherHits := redirectPair(t)
	c := newTestCluster(t)
	c.addWorker("w1")
	c.addWorker("w2")
	c.waitIdle(2)

	start := func(path string) (*fakeStore, model.Run) {
		t.Helper()
		st := newFakeStore()
		m := newManager(t, st, func(o *Options) {
			o.Dispatcher = c.master
			o.Allow = allowing(t, "127.0.0.1,localhost") // the master would allow it
		})
		run, err := m.Start(context.Background(), testFor(scenarioYAML(origin.URL, "400ms", 20, path, "")), model.StartRunRequest{Workers: 2})
		if err != nil {
			t.Fatal(err)
		}
		return st, waitFinished(t, st, run.ID, 20*time.Second)
	}

	// Another host: refused by both workers, nothing is sent there.
	st, final := start("/go")
	if final.Summary == nil || final.Summary.Requests == 0 || final.Summary.Errors != final.Summary.Requests {
		t.Fatalf("cross-host: summary %+v", final.Summary)
	}
	if otherHits.Load() != 0 {
		t.Errorf("cross-host: %d requests reached a host the scenario does not target", otherHits.Load())
	}
	want := "redirect to " + otherURL + "/landing not allowed (host is not a target of this scenario"
	if msgs := errorMessages(t, st, final.ID); !strings.Contains(msgs, want) {
		t.Errorf("cross-host: error groups %q lack %q", msgs, want)
	}

	// The same host (the reported cookie check): followed on workers too.
	st, final = start("/self")
	if final.Status != model.RunPassed || final.Summary.Requests == 0 || final.Summary.Errors != 0 {
		t.Fatalf("same host: status %s summary %+v errors: %s", final.Status, final.Summary, errorMessages(t, st, final.ID))
	}

	// Workers started with their own allowlist follow what it allows.
	c2 := newTestCluster(t)
	for _, id := range []string{"a1", "a2"} {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		c2.wg.Add(1)
		go func() {
			defer c2.wg.Done()
			_ = cluster.RunWorker(ctx, cluster.WorkerOptions{
				MasterAddr: c2.addr, Token: "s3cret", Insecure: true, ID: id, Logger: quietLogger(),
				DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
				Allow:       allowing(t, "localhost"),
			})
		}()
	}
	c2.waitIdle(2)
	st = newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Dispatcher = c2.master })
	run, err := m.Start(context.Background(), testFor(scenarioYAML(origin.URL, "400ms", 20, "/go", "")), model.StartRunRequest{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	final = waitFinished(t, st, run.ID, 20*time.Second)
	if final.Summary == nil || final.Summary.Requests == 0 || final.Summary.Errors != 0 || otherHits.Load() != final.Summary.Requests {
		t.Fatalf("worker allowlist: summary %+v, other hits %d, errors: %s", final.Summary, otherHits.Load(), errorMessages(t, st, final.ID))
	}
}
