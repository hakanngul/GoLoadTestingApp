package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// twoStepYAML: step "home" hits /ok (200); step "fail" hits /bad, which
// the target answers with an error status, so every iteration fails once.
func twoStepYAML(baseURL, duration string, rate int) string {
	return fmt.Sprintf(`name: live
target:
  base_url: %[1]s
  timeout: 2s
load:
  model: open
  stages:
    - duration: 100ms
      target: %[3]d
    - duration: %[2]s
      target: %[3]d
steps:
  - name: home
    path: /ok
    assert:
      - type: status
        value: "200"
  - name: fail
    path: /bad
    assert:
      - type: status
        value: "200"
`, baseURL, duration, rate)
}

// failingTarget answers /bad with badStatus and counts those requests.
func failingTarget(t *testing.T, badStatus int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var bad atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			bad.Add(1)
			w.WriteHeader(badStatus)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &bad
}

// collect reads events until the channel closes.
func collect(t *testing.T, events <-chan Event, timeout time.Duration) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("channel not closed within %v", timeout)
		}
	}
}

func lastStats(t *testing.T, evs []Event) (*model.LiveStats, int) {
	t.Helper()
	var ls *model.LiveStats
	n := 0
	for _, ev := range evs {
		if ev.Type == EventStats {
			if ev.Stats == nil {
				t.Fatal("stats event without Stats")
			}
			ls = ev.Stats
			n++
		}
	}
	if ls == nil {
		t.Fatal("no stats event")
	}
	return ls, n
}

// sameSummary compares the fields of a live summary with the final one.
func sameSummary(t *testing.T, what string, got, want model.Summary) {
	t.Helper()
	if got.Requests != want.Requests || got.Errors != want.Errors ||
		got.P50Ms != want.P50Ms || got.P95Ms != want.P95Ms || got.P99Ms != want.P99Ms ||
		got.MaxMs != want.MaxMs || got.MeanMs != want.MeanMs {
		t.Fatalf("%s: live %+v != final %+v", what, got, want)
	}
}

func TestLocalRunPublishesLiveStats(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.MaxErrorSamples = 5 })
	srv, bad := failingTarget(t, http.StatusInternalServerError)

	run, err := m.Start(context.Background(), testFor(twoStepYAML(srv.URL, "2500ms", 200)), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// While active, the API source returns live groups.
	waitMetrics(t, st, run.ID, 1, 5*time.Second)
	live, err := m.ErrorGroups(run.ID)
	if err != nil || len(live) != 1 || live[0].Count == 0 {
		t.Fatalf("live ErrorGroups = %+v, %v", live, err)
	}

	evs := collect(t, events, 15*time.Second)
	done := evs[len(evs)-1]
	if done.Type != EventDone || done.Run.Summary == nil {
		t.Fatalf("last event %+v", done)
	}
	final := *done.Run.Summary
	ls, n := lastStats(t, evs)
	if n < 3 {
		t.Fatalf("got %d stats events, want >= 3", n)
	}

	// The last live table equals the final summary, per step.
	sameSummary(t, "total", ls.Total, final)
	if len(ls.Total.ByStep) != 2 || len(final.ByStep) != 2 {
		t.Fatalf("by_step: live %v final %v", ls.Total.ByStep, final.ByStep)
	}
	for name, want := range final.ByStep {
		sameSummary(t, "step "+name, ls.Total.ByStep[name], want)
	}
	if ls.Elapsed <= 2 {
		t.Fatalf("elapsed = %v", ls.Elapsed)
	}

	// Every failure is grouped (not just the 5 samples).
	wantGroups := []model.ErrorGroup{{Step: "fail", Status: 500, Count: bad.Load()}}
	checkGroups := func(what string, got []model.ErrorGroup) {
		t.Helper()
		if len(got) != 1 || got[0].Step != "fail" || got[0].Status != 500 || got[0].Count != wantGroups[0].Count ||
			got[0].Message == "" || got[0].First.IsZero() || got[0].Last.Before(got[0].First) {
			t.Fatalf("%s = %+v, want %+v", what, got, wantGroups)
		}
	}
	if bad.Load() < 400 || final.ByStep["fail"].Errors != bad.Load() {
		t.Fatalf("target saw %d failures, summary %d", bad.Load(), final.ByStep["fail"].Errors)
	}
	checkGroups("live stats errors", ls.Errors)
	stored, err := st.ListErrorGroups(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkGroups("stored groups", stored)
	samples, _ := st.ListErrorSamples(context.Background(), run.ID)
	if len(samples) != 5 {
		t.Fatalf("samples = %d, want 5", len(samples))
	}

	// Current is the last interval: never more than the total.
	for _, ev := range evs {
		if ev.Type == EventStats && ev.Stats.Current.Requests > ev.Stats.Total.Requests {
			t.Fatalf("current %d > total %d", ev.Stats.Current.Requests, ev.Stats.Total.Requests)
		}
	}

	// Once the run is gone the runner no longer answers.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := m.ErrorGroups(run.ID); errors.Is(err, ErrNotRunning) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run still active")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubscribeGetsLatestStatsFirst(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv, _ := failingTarget(t, http.StatusServiceUnavailable)

	run, err := m.Start(context.Background(), testFor(twoStepYAML(srv.URL, "30s", 20)), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitMetrics(t, st, run.ID, 2, 10*time.Second)
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	first, second := <-events, <-events
	if first.Type != EventStatus || first.Run == nil {
		t.Fatalf("first = %+v", first)
	}
	if second.Type != EventStats || second.Stats == nil || second.Stats.Total.Requests == 0 ||
		len(second.Stats.Errors) != 1 || second.Stats.Errors[0].Status != 503 {
		t.Fatalf("second = %+v", second)
	}
	if err := m.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	waitFinished(t, st, run.ID, 10*time.Second)
}

func TestNoErrorGroupsStoredWithoutFailures(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv := target(t, http.StatusOK)
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "500ms", 20, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, st, run.ID, 10*time.Second)
	got, err := st.ListErrorGroups(context.Background(), run.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("groups = %+v, %v", got, err)
	}
}

func TestRemoteRunMergesErrorGroupsExactly(t *testing.T) {
	srv, bad := failingTarget(t, http.StatusServiceUnavailable)

	c := newTestCluster(t)
	c.addWorker("w1")
	c.addWorker("w2")
	c.waitIdle(2)

	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Dispatcher = c.master; o.MaxErrorSamples = 3 })
	run, err := m.Start(context.Background(), testFor(twoStepYAML(srv.URL, "2s", 150)), model.StartRunRequest{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	evs := collect(t, events, 30*time.Second)
	done := evs[len(evs)-1]
	if done.Type != EventDone || done.Run.Summary == nil {
		t.Fatalf("last event %+v", done)
	}
	if done.Run.Status != model.RunPassed {
		t.Fatalf("status %s (%s) %v", done.Run.Status, done.Run.Error, done.Run.Warnings)
	}
	final := *done.Run.Summary
	ls, n := lastStats(t, evs)
	if n < 2 {
		t.Fatalf("stats events = %d", n)
	}
	sameSummary(t, "total", ls.Total, final)
	for name, want := range final.ByStep {
		sameSummary(t, "step "+name, ls.Total.ByStep[name], want)
	}

	want := bad.Load()
	if want < 250 {
		t.Fatalf("target saw only %d failures", want)
	}
	stored, err := st.ListErrorGroups(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for what, got := range map[string][]model.ErrorGroup{"stored": stored, "live": ls.Errors} {
		if len(got) != 1 || got[0].Step != "fail" || got[0].Status != 503 || got[0].Count != want ||
			got[0].First.IsZero() || got[0].Last.IsZero() {
			t.Fatalf("%s groups = %+v, want count %d", what, got, want)
		}
	}
}
