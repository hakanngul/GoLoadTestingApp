package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// recv records the bodies posted to a test server.
type recv struct {
	mu     sync.Mutex
	bodies []string
	hits   atomic.Int32
}

func (r *recv) add(b string) {
	r.mu.Lock()
	r.bodies = append(r.bodies, b)
	r.mu.Unlock()
}

func (r *recv) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func newServer(t *testing.T, r *recv, status func(hit int32) int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		hit := r.hits.Add(1)
		r.add(string(b))
		w.WriteHeader(status(hit))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func ok(int32) int { return 200 }

func newNotifier(t *testing.T, st store.NotificationStore, logBuf *bytes.Buffer) *Notifier {
	t.Helper()
	o := Options{Store: st, PublicURL: "https://goload.example.com/", Backoff: time.Millisecond, Timeout: 2 * time.Second}
	if logBuf != nil {
		o.Logger = slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	n := New(o)
	t.Cleanup(func() { n.Close(context.Background()) })
	return n
}

func addChannel(t *testing.T, st store.NotificationStore, c model.NotificationChannel) {
	t.Helper()
	if _, err := st.CreateNotificationChannel(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func finishedRun(status model.RunStatus) model.Run {
	return model.Run{
		ID: "run-1", TestName: "checkout", Status: status, YAML: "name: secret-scenario",
		Summary: &model.Summary{Requests: 1234, Errors: 26, ErrorRate: 0.021, P95Ms: 45},
	}
}

func TestSlackRunMessage(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, ok)
	addChannel(t, st, model.NotificationChannel{Name: "ops", Kind: model.NotifySlack, URL: srv.URL + "/hook",
		Events: []string{model.EventRunFinished}, Enabled: true})
	n := newNotifier(t, st, nil)
	n.RunFinished(finishedRun(model.RunFailed))
	n.Close(context.Background())

	bodies := r.all()
	if len(bodies) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(bodies))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	want := "GoLoad: run FAILED — checkout · 1,234 req · p95 45.0 ms · errors 2.1% · https://goload.example.com/runs/run-1"
	if got["text"] != want {
		t.Fatalf("text = %q\nwant   %q", got["text"], want)
	}
}

func TestSlackSuiteMessageAndNoLinkWithoutBase(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, ok)
	addChannel(t, st, model.NotificationChannel{Name: "ops", Kind: model.NotifySlack, URL: srv.URL,
		Events: []string{model.EventSuiteFinished}, Enabled: true})
	n := New(Options{Store: st, Backoff: time.Millisecond}) // no public URL
	n.SuiteFinished(model.SuiteRun{ID: "s1", SuiteName: "smoke <&>", Status: model.RunFailed, Items: []model.SuiteRunItem{
		{TestName: "a", Status: "passed", Summary: &model.Summary{Requests: 10, P95Ms: 150, ErrorRate: 0}},
		{TestName: "b", Status: "failed", Error: "boom"},
	}})
	n.Close(context.Background())

	bodies := r.all()
	if len(bodies) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(bodies))
	}
	var got map[string]string
	_ = json.Unmarshal([]byte(bodies[0]), &got)
	text := got["text"]
	for _, w := range []string{"suite FAILED — smoke &lt;&amp;&gt; · 2 tests", "• a: passed (10 req · p95 150 ms · errors 0.0%)", "• b: failed — boom"} {
		if !strings.Contains(text, w) {
			t.Errorf("text missing %q:\n%s", w, text)
		}
	}
	if strings.Contains(text, "http") {
		t.Errorf("link present without public URL:\n%s", text)
	}
}

func TestWebhookPayloads(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, ok)
	addChannel(t, st, model.NotificationChannel{Name: "w", Kind: model.NotifyWebhook, URL: srv.URL,
		Events: []string{model.EventRunFinished, model.EventSuiteFinished}, Enabled: true})
	n := newNotifier(t, st, nil)
	n.RunFinished(finishedRun(model.RunPassed))
	n.SuiteFinished(model.SuiteRun{ID: "s1", SuiteName: "x", Status: model.RunPassed})
	n.Close(context.Background())

	bodies := r.all()
	if len(bodies) != 2 {
		t.Fatalf("got %d deliveries, want 2", len(bodies))
	}
	seen := map[string]map[string]json.RawMessage{}
	for _, b := range bodies {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		var ev string
		_ = json.Unmarshal(m["event"], &ev)
		seen[ev] = m
	}
	run := seen["run_finished"]["run"]
	if run == nil || strings.Contains(string(run), "secret-scenario") || strings.Contains(string(run), `"yaml"`) {
		t.Fatalf("run payload = %s (must exist and omit yaml)", run)
	}
	if !strings.Contains(string(run), `"test_name":"checkout"`) {
		t.Fatalf("run payload lacks fields: %s", run)
	}
	if seen["suite_finished"]["suite_run"] == nil {
		t.Fatalf("suite payload = %v", seen["suite_finished"])
	}
}

func TestFiltering(t *testing.T) {
	st := store.NewMemory()
	var all, onlyFail, suiteOnly, disabled recv
	mk := func(name string, r *recv, ev []string, onlyFailures, enabled bool) {
		srv := newServer(t, r, ok)
		addChannel(t, st, model.NotificationChannel{Name: name, Kind: model.NotifyWebhook, URL: srv.URL, Events: ev,
			OnlyFailures: onlyFailures, Enabled: enabled})
	}
	mk("all", &all, []string{model.EventRunFinished}, false, true)
	mk("fail", &onlyFail, []string{model.EventRunFinished}, true, true)
	mk("suite", &suiteOnly, []string{model.EventSuiteFinished}, false, true)
	mk("off", &disabled, []string{model.EventRunFinished}, false, false)

	n := newNotifier(t, st, nil)
	for _, s := range []model.RunStatus{model.RunPassed, model.RunFailed, model.RunError, model.RunStopped, model.RunRunning} {
		n.RunFinished(finishedRun(s))
	}
	n.Close(context.Background())

	if got := all.hits.Load(); got != 4 { // running is not final
		t.Errorf("all-channel deliveries = %d, want 4", got)
	}
	if got := onlyFail.hits.Load(); got != 2 { // failed + error; stopped is not a failure
		t.Errorf("only-failures deliveries = %d, want 2", got)
	}
	if got := suiteOnly.hits.Load(); got != 0 {
		t.Errorf("suite-only channel got %d run events", got)
	}
	if got := disabled.hits.Load(); got != 0 {
		t.Errorf("disabled channel got %d events", got)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, func(hit int32) int {
		if hit < 3 {
			return 503
		}
		return 200
	})
	addChannel(t, st, model.NotificationChannel{Name: "w", Kind: model.NotifyWebhook, URL: srv.URL,
		Events: []string{model.EventRunFinished}, Enabled: true})
	n := newNotifier(t, st, nil)
	n.RunFinished(finishedRun(model.RunPassed))
	n.Close(context.Background())
	if got := r.hits.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3 (2 retries)", got)
	}
}

func TestRetriesGiveUpAfterTwo(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, func(int32) int { return 500 })
	addChannel(t, st, model.NotificationChannel{Name: "w", Kind: model.NotifyWebhook, URL: srv.URL,
		Events: []string{model.EventRunFinished}, Enabled: true})
	n := newNotifier(t, st, nil)
	n.RunFinished(finishedRun(model.RunPassed))
	n.Close(context.Background())
	if got := r.hits.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, func(int32) int { return 404 })
	addChannel(t, st, model.NotificationChannel{Name: "w", Kind: model.NotifyWebhook, URL: srv.URL,
		Events: []string{model.EventRunFinished}, Enabled: true})
	n := newNotifier(t, st, nil)
	n.RunFinished(finishedRun(model.RunPassed))
	n.Close(context.Background())
	if got := r.hits.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestRetryOnNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused from now on
	var hits atomic.Int32
	n := New(Options{Store: store.NewMemory(), Backoff: time.Millisecond, Client: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			hits.Add(1)
			return http.DefaultTransport.RoundTrip(r)
		})}})
	defer n.Close(context.Background())
	err := n.SendTest(context.Background(), model.NotificationChannel{Name: "x", Kind: model.NotifyWebhook, URL: url + "/secret-path-1234"})
	if err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", hits.Load())
	}
	if strings.Contains(err.Error(), "secret-path-1234") || strings.Contains(err.Error(), url) {
		t.Fatalf("error leaks the URL: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestURLNeverLogged(t *testing.T) {
	st := store.NewMemory()
	var r recv
	srv := newServer(t, &r, func(int32) int { return 500 })
	secret := srv.URL + "/services/T000/B000/supersecrettoken"
	addChannel(t, st, model.NotificationChannel{Name: "w", Kind: model.NotifySlack, URL: secret,
		Events: []string{model.EventRunFinished}, Enabled: true})
	// A dead address too, so the client's own url.Error path is logged.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/deadsecrettoken"
	dead.Close()
	addChannel(t, st, model.NotificationChannel{Name: "d", Kind: model.NotifyWebhook, URL: deadURL,
		Events: []string{model.EventRunFinished}, Enabled: true})

	var logs bytes.Buffer
	n := newNotifier(t, st, &logs)
	n.RunFinished(finishedRun(model.RunPassed))
	n.Close(context.Background())

	out := logs.String()
	if !strings.Contains(out, "delivery failed") {
		t.Fatalf("expected a failure log, got %q", out)
	}
	for _, s := range []string{"supersecrettoken", "deadsecrettoken", srv.URL, deadURL} {
		if strings.Contains(out, s) {
			t.Fatalf("log leaks %q:\n%s", s, out)
		}
	}
}

func TestSendTest(t *testing.T) {
	var r recv
	srv := newServer(t, &r, ok)
	n := newNotifier(t, store.NewMemory(), nil)
	if err := n.SendTest(context.Background(), model.NotificationChannel{Name: "ops", Kind: model.NotifySlack, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if b := r.all(); len(b) != 1 || !strings.Contains(b[0], `"text"`) || !strings.Contains(b[0], "ops") {
		t.Fatalf("slack test body = %v", b)
	}
	if err := n.SendTest(context.Background(), model.NotificationChannel{Name: "w", Kind: model.NotifyWebhook, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if b := r.all(); !strings.Contains(b[1], `"event":"test"`) {
		t.Fatalf("webhook test body = %s", b[1])
	}

	bad := newServer(t, &recv{}, func(int32) int { return 400 })
	err := n.SendTest(context.Background(), model.NotificationChannel{Kind: model.NotifySlack, URL: bad.URL})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("SendTest 400 error = %v", err)
	}
}

func TestEnqueueNeverBlocksAndCloseIsSafe(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	st := store.NewMemory()
	addChannel(t, st, model.NotificationChannel{Name: "w", Kind: model.NotifyWebhook, URL: srv.URL,
		Events: []string{model.EventRunFinished}, Enabled: true})
	n := New(Options{Store: st, Workers: 1, Queue: 2, Timeout: time.Second, Backoff: time.Millisecond})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			n.RunFinished(finishedRun(model.RunPassed)) // queue overflows: must drop, not block
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunFinished blocked")
	}
	close(block)
	n.Close(context.Background())
	n.Close(context.Background()) // idempotent
	n.RunFinished(finishedRun(model.RunPassed))
}

func TestMaskURL(t *testing.T) {
	cases := map[string]string{
		"https://hooks.slack.com/services/T0/B0/abcdWXYZ": "https://hooks.slack.com/…WXYZ",
		"http://localhost:8080/x":                         "http://localhost:8080/…80/x",
		"not a url":                                       "…",
		"":                                                "…",
	}
	for in, want := range cases {
		if got := MaskURL(in); got != want {
			t.Errorf("MaskURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := MaskURL("https://a.test/abcdefgh"); strings.Contains(got, "abcd") {
		t.Errorf("mask leaks path: %q", got)
	}
}

func TestIsFailure(t *testing.T) {
	for s, want := range map[model.RunStatus]bool{model.RunFailed: true, model.RunError: true,
		model.RunStopped: false, model.RunPassed: false} {
		if IsFailure(s) != want {
			t.Errorf("IsFailure(%s) != %v", s, want)
		}
	}
}

func TestFormatInt(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := formatInt(in); got != want {
			t.Errorf("formatInt(%d) = %q, want %q", in, got, want)
		}
	}
}
