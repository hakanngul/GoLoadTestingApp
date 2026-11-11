package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

const fakeToken = "s3cr3t-token-value"

// fakeAPI is a minimal GoLoad API. Runs and suite runs are served from the
// maps; polls return the state in the order given by the *Polls slices.
type fakeAPI struct {
	t        *testing.T
	tests    []model.Test
	suites   []model.Suite
	startErr *model.APIError // returned with 400 on any start

	mu        sync.Mutex
	runs      map[string][]model.Run // polled in order, last one repeats
	suiteRuns map[string][]model.SuiteRun
	stopped   []string
	badAuth   atomic.Int64
	getFails  atomic.Int64 // number of GET /api/runs/... to fail with 503 first
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	next := func(f *fakeAPI, id string) (any, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if rs, ok := f.runs[id]; ok {
			r := rs[0]
			if len(rs) > 1 {
				f.runs[id] = rs[1:]
			}
			return r, true
		}
		if ss, ok := f.suiteRuns[id]; ok {
			s := ss[0]
			if len(ss) > 1 {
				f.suiteRuns[id] = ss[1:]
			}
			return s, true
		}
		return nil, false
	}
	mux.HandleFunc("GET /api/tests", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, f.tests) })
	mux.HandleFunc("GET /api/suites", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, f.suites) })
	start := func(w http.ResponseWriter, id string, body any) {
		if f.startErr != nil {
			writeJSON(w, 400, f.startErr)
			return
		}
		v, _ := next(f, id)
		writeJSON(w, 201, v)
	}
	mux.HandleFunc("POST /api/tests/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		var req model.StartRunRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.t.Logf("start test %s workers=%d label=%q", r.PathValue("id"), req.Workers, req.Label)
		f.mu.Unlock()
		start(w, "run-"+r.PathValue("id"), req)
	})
	mux.HandleFunc("POST /api/suites/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		start(w, "srun-"+r.PathValue("id"), nil)
	})
	poll := func(w http.ResponseWriter, r *http.Request) {
		if f.getFails.Add(-1) >= 0 && strings.HasPrefix(r.URL.Path, "/api/runs/") {
			writeJSON(w, 503, model.APIError{Error: "warming up"})
			return
		}
		v, ok := next(f, r.PathValue("id"))
		if !ok {
			writeJSON(w, 404, model.APIError{Error: "not found"})
			return
		}
		writeJSON(w, 200, v)
	}
	mux.HandleFunc("GET /api/runs/{id}", poll)
	mux.HandleFunc("GET /api/suite-runs/{id}", poll)
	stop := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.stopped = append(f.stopped, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(202)
	}
	mux.HandleFunc("POST /api/runs/{id}/stop", stop)
	mux.HandleFunc("POST /api/suite-runs/{id}/stop", stop)

	// Auth wrapper: every request must carry the bearer token.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			f.badAuth.Add(1)
			writeJSON(w, 401, model.APIError{Error: "unauthorized"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func newFake(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	remoteRetryBackoff = time.Millisecond
	f := &fakeAPI{t: t, runs: map[string][]model.Run{}, suiteRuns: map[string][]model.SuiteRun{}}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, srv
}

func ptr[T any](v T) *T { return &v }

func runRemote(t *testing.T, srv *httptest.Server, extra ...string) (code int, out, errOut string) {
	t.Helper()
	t.Setenv("GOLOAD_TOKEN", fakeToken)
	args := append([]string{"remote", "run", "--server", srv.URL, "--poll", "5ms"}, extra...)
	var o, e bytes.Buffer
	code = dispatch(args, &o, &e)
	return code, o.String(), e.String()
}

var t0 = time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

func finishedRun(id, name string, st model.RunStatus, ths ...model.ThresholdResult) model.Run {
	return model.Run{
		ID: id, TestID: "t1", TestName: name, Status: st, StartedAt: t0, FinishedAt: ptr(t0.Add(12 * time.Second)),
		Summary:    &model.Summary{Requests: 1200, Errors: 6, ErrorRate: 0.005, RPS: 100, P50Ms: 20, P95Ms: 180, P99Ms: 300},
		Thresholds: ths,
	}
}

func TestRemoteTestPassed(t *testing.T) {
	f, srv := newFake(t)
	f.tests = []model.Test{{ID: "t1", Name: "smoke"}, {ID: "t2", Name: "other"}}
	running := finishedRun("r1", "smoke", model.RunRunning)
	running.FinishedAt = nil
	f.runs["r1"] = []model.Run{running, finishedRun("r1", "smoke", model.RunPassed,
		model.ThresholdResult{Metric: "p95_ms", Op: "<", Value: 500, Actual: 180, Passed: true})}
	// The start response carries the new run's id.
	f.runs["run-t1"] = []model.Run{{ID: "r1", TestName: "smoke", Status: model.RunQueued, StartedAt: t0}}

	dir := t.TempDir()
	jfile, jsonFile := filepath.Join(dir, "r.xml"), filepath.Join(dir, "r.json")
	code, out, errOut := runRemote(t, srv, "--test", "smoke", "--junit", jfile, "--json", jsonFile, "--label", "v1")
	if code != exitOK {
		t.Fatalf("exit %d, want 0\nout:%s\nerr:%s", code, out, errOut)
	}
	if !strings.Contains(out, srv.URL+"/runs/r1") || !strings.Contains(out, "PASSED") {
		t.Errorf("missing URL or verdict:\n%s", out)
	}
	if f.badAuth.Load() != 0 {
		t.Errorf("%d requests without the bearer token", f.badAuth.Load())
	}
	if strings.Contains(out+errOut, fakeToken) {
		t.Error("token was printed")
	}
	var run model.Run
	b, err := os.ReadFile(jsonFile)
	if err != nil || json.Unmarshal(b, &run) != nil || run.ID != "r1" || run.Status != model.RunPassed {
		t.Errorf("json file bad: %v %s", err, b)
	}
	ju := parseJUnit(t, jfile)
	if len(ju.Suites) != 1 || ju.Suites[0].Name != "smoke" || len(ju.Suites[0].Cases) != 1 {
		t.Fatalf("junit shape: %+v", ju)
	}
	if tc := ju.Suites[0].Cases[0]; tc.Failure != nil || tc.Error != nil || tc.Time != "12.000" {
		t.Errorf("testcase: %+v", tc)
	}
}

func parseJUnit(t *testing.T, path string) junitSuites {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ju junitSuites
	if err := xml.Unmarshal(b, &ju); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, b)
	}
	return ju
}

func TestRemoteSuiteFailedItem(t *testing.T) {
	f, srv := newFake(t)
	f.suites = []model.Suite{{ID: "s1", Name: "nightly"}}
	items := []model.SuiteRunItem{
		{TestID: "t1", TestName: "login", RunID: "ra", Status: "passed", StartedAt: ptr(t0), FinishedAt: ptr(t0.Add(10 * time.Second)),
			Summary: &model.Summary{Requests: 500, RPS: 50, P95Ms: 90}},
		{TestID: "t2", TestName: "checkout", RunID: "rb", Status: "failed", StartedAt: ptr(t0.Add(10 * time.Second)), FinishedAt: ptr(t0.Add(25 * time.Second)),
			Summary: &model.Summary{Requests: 800, Errors: 80, ErrorRate: 0.1, RPS: 53, P95Ms: 950}},
		{TestID: "t3", TestName: "search", Status: "skipped"},
	}
	sr := model.SuiteRun{ID: "sr1", SuiteID: "s1", SuiteName: "nightly", Status: model.RunFailed, StartedAt: t0, FinishedAt: ptr(t0.Add(25 * time.Second)), Items: items}
	f.suiteRuns["srun-s1"] = []model.SuiteRun{{ID: "sr1", SuiteName: "nightly", Status: model.RunQueued, StartedAt: t0, Items: items}}
	f.suiteRuns["sr1"] = []model.SuiteRun{sr}
	f.runs["rb"] = []model.Run{finishedRun("rb", "checkout", model.RunFailed,
		model.ThresholdResult{Metric: "p95_ms", Op: "<", Value: 500, Actual: 950, Passed: false},
		model.ThresholdResult{Metric: "error_rate", Op: "<", Value: 0.01, Actual: 0.1, Passed: false},
		model.ThresholdResult{Metric: "rps", Op: ">", Value: 10, Actual: 53, Passed: true})}
	f.runs["ra"] = []model.Run{finishedRun("ra", "login", model.RunPassed)}

	jfile := filepath.Join(t.TempDir(), "junit.xml")
	code, out, errOut := runRemote(t, srv, "--suite", "nightly", "--junit", jfile)
	if code != exitThreshold {
		t.Fatalf("exit %d, want 1\nout:%s\nerr:%s", code, out, errOut)
	}
	for _, want := range []string{"checkout", "p95_ms < 500 (actual 950)", "error_rate < 0.01 (actual 0.1)", srv.URL + "/suite-runs/sr1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	ju := parseJUnit(t, jfile)
	if ju.Tests != 3 || ju.Fails != 1 || ju.Skipped != 1 || ju.Errors != 0 {
		t.Errorf("counts: %+v", ju)
	}
	s := ju.Suites[0]
	if s.Name != "nightly" || len(s.Cases) != 3 {
		t.Fatalf("suite: %+v", s)
	}
	byName := map[string]junitCase{}
	for _, c := range s.Cases {
		byName[c.Name] = c
	}
	if c := byName["login"]; c.Failure != nil || c.Skipped != nil || c.Time != "10.000" {
		t.Errorf("login: %+v", c)
	}
	c := byName["checkout"]
	if c.Failure == nil || !strings.Contains(c.Failure.Message, "p95_ms < 500 (actual 950)") ||
		!strings.Contains(c.Failure.Message, "error_rate < 0.01 (actual 0.1)") || strings.Contains(c.Failure.Message, "rps") {
		t.Errorf("checkout failure: %+v", c.Failure)
	}
	if c.Time != "15.000" || !strings.Contains(c.SystemOut, "requests: 800") || !strings.Contains(c.SystemOut, srv.URL+"/runs/rb") {
		t.Errorf("checkout time/system-out: %q %q", c.Time, c.SystemOut)
	}
	if byName["search"].Skipped == nil {
		t.Error("search should be skipped")
	}
	if !strings.Contains(s.SystemOut, srv.URL+"/suite-runs/sr1") {
		t.Errorf("suite system-out: %q", s.SystemOut)
	}
}

func TestRemoteStartRejected(t *testing.T) {
	f, srv := newFake(t)
	f.tests = []model.Test{{ID: "t1", Name: "smoke"}}
	f.startErr = &model.APIError{Error: "invalid scenario", Details: []string{"target.url: host not allowed", "data.file: missing"}}
	code, _, errOut := runRemote(t, srv, "--test", "smoke")
	if code != exitRun {
		t.Fatalf("exit %d, want 3", code)
	}
	for _, want := range []string{"invalid scenario", "target.url: host not allowed", "data.file: missing"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestRemoteResolve(t *testing.T) {
	f, srv := newFake(t)
	f.tests = []model.Test{{ID: "a", Name: "smoke"}, {ID: "b", Name: "smoke"}, {ID: "c", Name: "checkout flow"}}
	code, _, errOut := runRemote(t, srv, "--test", "smoke")
	if code != exitUsage || !strings.Contains(errOut, "2 tests are named") || !strings.Contains(errOut, "a, b") {
		t.Errorf("ambiguous: exit %d, %q", code, errOut)
	}
	code, _, errOut = runRemote(t, srv, "--test", "checkout")
	if code != exitUsage || !strings.Contains(errOut, `no test named`) || !strings.Contains(errOut, `"checkout flow"`) {
		t.Errorf("not found: exit %d, %q", code, errOut)
	}
	f.suites = []model.Suite{{ID: "s1", Name: "nightly"}}
	code, _, errOut = runRemote(t, srv, "--suite", "nope")
	if code != exitUsage || !strings.Contains(errOut, `"nightly"`) {
		t.Errorf("suite not found: exit %d, %q", code, errOut)
	}
}

func TestRemoteTimeoutStops(t *testing.T) {
	f, srv := newFake(t)
	f.tests = []model.Test{{ID: "t1", Name: "smoke"}}
	running := model.Run{ID: "r1", TestName: "smoke", Status: model.RunRunning, StartedAt: t0}
	f.runs["run-t1"] = []model.Run{running}
	f.runs["r1"] = []model.Run{running}
	code, out, errOut := runRemote(t, srv, "--test", "smoke", "--timeout", "150ms")
	if code != exitRun {
		t.Fatalf("exit %d, want 3\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "timed out") {
		t.Errorf("stderr: %q", errOut)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stopped) != 1 || f.stopped[0] != "/api/runs/r1/stop" {
		t.Errorf("stop calls: %v", f.stopped)
	}
}

func TestRemoteTimeoutNoStop(t *testing.T) {
	f, srv := newFake(t)
	f.suites = []model.Suite{{ID: "s1", Name: "nightly"}}
	running := model.SuiteRun{ID: "sr1", SuiteName: "nightly", Status: model.RunRunning, StartedAt: t0,
		Items: []model.SuiteRunItem{{TestName: "x", Status: "running"}}}
	f.suiteRuns["srun-s1"] = []model.SuiteRun{running}
	f.suiteRuns["sr1"] = []model.SuiteRun{running}
	jfile := filepath.Join(t.TempDir(), "j.xml")
	code, _, _ := runRemote(t, srv, "--suite", "nightly", "--timeout", "100ms", "--no-stop-on-cancel", "--junit", jfile)
	if code != exitRun {
		t.Fatalf("exit %d, want 3", code)
	}
	f.mu.Lock()
	n := len(f.stopped)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("stop called %d times with --no-stop-on-cancel", n)
	}
	if ju := parseJUnit(t, jfile); ju.Errors != 1 {
		t.Errorf("unfinished item should be a JUnit error: %+v", ju)
	}
}

func TestRemoteRetriesTransientPolls(t *testing.T) {
	f, srv := newFake(t)
	f.tests = []model.Test{{ID: "t1", Name: "smoke"}}
	f.runs["run-t1"] = []model.Run{{ID: "r1", TestName: "smoke", Status: model.RunQueued, StartedAt: t0}}
	f.runs["r1"] = []model.Run{finishedRun("r1", "smoke", model.RunPassed)}
	f.getFails.Store(2)
	code, out, errOut := runRemote(t, srv, "--test", "t1") // by id
	if code != exitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestRemoteErrorStatusExits3(t *testing.T) {
	f, srv := newFake(t)
	f.tests = []model.Test{{ID: "t1", Name: "smoke"}}
	f.runs["run-t1"] = []model.Run{{ID: "r1", TestName: "smoke", Status: model.RunQueued, StartedAt: t0}}
	bad := finishedRun("r1", "smoke", model.RunError)
	bad.Error = "worker lost"
	f.runs["r1"] = []model.Run{bad}
	jfile := filepath.Join(t.TempDir(), "j.xml")
	code, out, _ := runRemote(t, srv, "--test", "smoke", "--junit", jfile)
	if code != exitRun || !strings.Contains(out, "worker lost") {
		t.Errorf("exit %d out %s", code, out)
	}
	ju := parseJUnit(t, jfile)
	if c := ju.Suites[0].Cases[0]; c.Error == nil || c.Error.Message != "worker lost" {
		t.Errorf("case: %+v", c)
	}
}

func TestRemoteUnauthorizedNeverPrintsToken(t *testing.T) {
	_, srv := newFake(t)
	t.Setenv("GOLOAD_TOKEN", "wrong-token-xyz")
	var o, e bytes.Buffer
	code := dispatch([]string{"remote", "run", "--server", srv.URL, "--test", "x"}, &o, &e)
	if code != exitRun || !strings.Contains(e.String(), "unauthorized") {
		t.Errorf("exit %d: %s", code, e.String())
	}
	if strings.Contains(o.String()+e.String(), "wrong-token-xyz") {
		t.Error("token printed")
	}
}

func TestRemoteUnreachable(t *testing.T) {
	remoteRetryBackoff = time.Millisecond
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	var o, e bytes.Buffer
	code := dispatch([]string{"remote", "run", "--server", url, "--test", "x"}, &o, &e)
	if code != exitRun {
		t.Errorf("exit %d: %s", code, e.String())
	}
}

func TestRemoteUsage(t *testing.T) {
	cases := [][]string{
		{"remote"},
		{"remote", "bogus"},
		{"remote", "run", "--server", "http://x"},
		{"remote", "run", "--server", "http://x", "--test", "a", "--suite", "b"},
		{"remote", "run", "--test", "a"},
		{"remote", "run", "--server", "ftp://x", "--test", "a"},
		{"remote", "run", "--server", "http://x", "--suite", "a", "--workers", "2"},
	}
	t.Setenv("GOLOAD_SERVER", "")
	for _, args := range cases {
		var o, e bytes.Buffer
		if code := dispatch(args, &o, &e); code != exitUsage {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, e.String())
		}
	}
}
