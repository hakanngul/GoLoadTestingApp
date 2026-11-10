package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// --- helpers ---------------------------------------------------------------

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newManager(t *testing.T, st *fakeStore, mod func(*Options)) *Manager {
	t.Helper()
	opts := Options{Store: st, Logger: quietLogger(), Version: "test-1"}
	if mod != nil {
		mod(&opts)
	}
	m := NewManager(opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

// target starts an HTTP server answering every request with status.
func target(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// scenarioYAML builds an open-model, single-step scenario that ramps to
// rate within 100ms and holds it for duration. extra is appended verbatim
// (thresholds, data, ...).
func scenarioYAML(baseURL, duration string, rate int, path, extra string) string {
	return fmt.Sprintf(`name: test
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
    path: %[4]s
    assert:
      - type: status
        value: "200"
%[5]s`, baseURL, duration, rate, path, extra)
}

func testFor(yaml string) model.Test {
	return model.Test{ID: "test-1", Name: "test", YAML: yaml}
}

// waitFinished polls the store until the run reaches a terminal status.
func waitFinished(t *testing.T, st *fakeStore, id string, timeout time.Duration) model.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r, err := st.GetRun(context.Background(), id)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if r.Status.Finished() {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish within %v", id, timeout)
	return model.Run{}
}

// waitMetrics polls until the run has at least n stored metric points.
func waitMetrics(t *testing.T, st *fakeStore, id string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ps, _ := st.ListMetrics(context.Background(), id)
		if len(ps) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s: fewer than %d metric points after %v", id, n, timeout)
}

func hasWarning(r model.Run, substr string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// --- tests -----------------------------------------------------------------

func TestHappyPathPassed(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv := target(t, http.StatusOK)

	yaml := scenarioYAML(srv.URL, "2s", 20, "/", `thresholds:
  - metric: error_rate
    op: lt
    value: 0.01
`)
	run, err := m.Start(context.Background(), testFor(yaml), model.StartRunRequest{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != model.RunRunning || run.Workers != 1 || run.YAML != yaml || run.TestName != "test" {
		t.Fatalf("unexpected initial run: %+v", run)
	}
	if run.Thresholds == nil || run.Warnings == nil {
		t.Fatalf("Thresholds/Warnings must be empty slices, got %#v / %#v", run.Thresholds, run.Warnings)
	}
	if got := m.Active(); len(got) != 1 || got[0] != run.ID {
		t.Fatalf("Active = %v, want [%s]", got, run.ID)
	}

	fin := waitFinished(t, st, run.ID, 10*time.Second)
	if fin.Status != model.RunPassed {
		t.Fatalf("status = %s (err %q), want passed", fin.Status, fin.Error)
	}
	if fin.FinishedAt == nil || fin.Summary == nil {
		t.Fatalf("FinishedAt/Summary not set: %+v", fin)
	}
	if fin.Summary.Requests < 20 || fin.Summary.Errors != 0 {
		t.Fatalf("summary = %+v, want >=20 requests, 0 errors", fin.Summary)
	}
	if s, ok := fin.Summary.ByStep["home"]; !ok || s.Requests != fin.Summary.Requests {
		t.Fatalf("ByStep = %+v", fin.Summary.ByStep)
	}
	if fin.Summary.P95Ms <= 0 || fin.Summary.MaxMs < fin.Summary.P95Ms {
		t.Fatalf("latencies not in ms: %+v", fin.Summary)
	}
	if len(fin.Thresholds) != 1 || !fin.Thresholds[0].Passed || fin.Thresholds[0].Metric != "error_rate" {
		t.Fatalf("thresholds = %+v", fin.Thresholds)
	}

	points, _ := st.ListMetrics(context.Background(), run.ID)
	if len(points) < 2 || len(points) > 4 {
		t.Fatalf("got %d metric points for a 2s run, want ~2-3", len(points))
	}
	var total int64
	for _, p := range points {
		total += p.Requests
		if p.Workers != 1 {
			t.Fatalf("point Workers = %d", p.Workers)
		}
	}
	if total != fin.Summary.Requests {
		t.Fatalf("points sum to %d requests, summary has %d", total, fin.Summary.Requests)
	}
	if points[0].TargetRate <= 0 || points[0].Elapsed < 0.9 || points[0].Elapsed > 1.5 {
		t.Fatalf("first point = %+v", points[0])
	}
	// Snapshots are stored alongside and decode.
	snaps, _ := st.ListSnapshots(context.Background(), run.ID)
	if len(snaps) != len(points) {
		t.Fatalf("%d snapshots for %d points", len(snaps), len(points))
	}
	var snap metrics.Snapshot
	if err := snap.UnmarshalBinary(snaps[0]); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(m.Active()) != 0 {
		t.Fatalf("Active after finish = %v", m.Active())
	}
}

func TestThresholdFailureAndErrorSamplesCapped(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.MaxErrorSamples = 5 })
	srv := target(t, http.StatusInternalServerError)

	yaml := scenarioYAML(srv.URL, "1500ms", 30, "/", `thresholds:
  - metric: error_rate
    op: lt
    value: 0.01
`)
	run, err := m.Start(context.Background(), testFor(yaml), model.StartRunRequest{Workers: 1})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	fin := waitFinished(t, st, run.ID, 10*time.Second)
	if fin.Status != model.RunFailed {
		t.Fatalf("status = %s (err %q), want failed", fin.Status, fin.Error)
	}
	if len(fin.Thresholds) != 1 || fin.Thresholds[0].Passed || fin.Thresholds[0].Actual != 1 {
		t.Fatalf("thresholds = %+v", fin.Thresholds)
	}
	if fin.Summary.Errors <= 5 {
		t.Fatalf("expected more than 5 errors, got %d", fin.Summary.Errors)
	}
	samples, _ := st.ListErrorSamples(context.Background(), run.ID)
	if len(samples) != 5 {
		t.Fatalf("got %d error samples, want 5 (cap)", len(samples))
	}
	for _, s := range samples {
		if s.Step != "home" || s.Status != 500 || s.Message == "" || s.T.IsZero() {
			t.Fatalf("bad sample %+v", s)
		}
	}
}

func TestStopEndsStoppedWithPartialSummary(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv := target(t, http.StatusOK)

	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "30s", 20, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitMetrics(t, st, run.ID, 1, 5*time.Second)
	stopAt := time.Now()
	if err := m.Stop(run.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	fin := waitFinished(t, st, run.ID, 5*time.Second)
	if took := time.Since(stopAt); took > 3*time.Second {
		t.Fatalf("stop took %v", took)
	}
	if fin.Status != model.RunStopped || fin.Error != "" {
		t.Fatalf("status = %s (err %q), want stopped", fin.Status, fin.Error)
	}
	if fin.Summary == nil || fin.Summary.Requests == 0 || fin.FinishedAt == nil {
		t.Fatalf("partial summary missing: %+v", fin)
	}
	if err := m.Stop(run.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("second Stop = %v, want ErrNotRunning", err)
	}
	if _, _, err := m.Subscribe(run.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Subscribe after finish = %v, want ErrNotRunning", err)
	}
}

func TestUnknownRun(t *testing.T) {
	t.Parallel()
	m := newManager(t, newFakeStore(), nil)
	if err := m.Stop("nope"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Stop = %v", err)
	}
	if _, _, err := m.Subscribe("nope"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Subscribe = %v", err)
	}
}

func TestStartRejections(t *testing.T) {
	t.Parallel()
	valid := scenarioYAML("http://127.0.0.1:1", "1s", 1, "/", "")
	errBlocked := errors.New("host not on allowlist")

	cases := []struct {
		name    string
		yaml    string
		req     model.StartRunRequest
		allow   func(string) error
		check   func(error) bool
		details string // substring expected in ValidationError details
	}{
		{
			name: "malformed yaml", yaml: "name: [", details: "decode",
		},
		{
			name:    "invalid scenario",
			yaml:    "name: x\ntarget:\n  base_url: ftp://x\nload:\n  stages: []\nsteps: []\n",
			details: "target.base_url",
		},
		{
			name:    "missing data file",
			yaml:    valid + "data:\n  file: data/users.csv\n",
			details: `data.file: file "users.csv" is not attached`,
		},
		{
			name: "negative workers", yaml: valid, req: model.StartRunRequest{Workers: -1}, details: "workers",
		},
		{
			name: "label too long", yaml: valid, req: model.StartRunRequest{Label: strings.Repeat("é", MaxLabelLen+1)}, details: "label",
		},
		{
			name: "notes too long", yaml: valid, req: model.StartRunRequest{Notes: strings.Repeat("x", MaxNotesLen+1)}, details: "notes",
		},
		{
			name: "distributed", yaml: valid, req: model.StartRunRequest{Workers: 3},
			check: func(err error) bool { return errors.Is(err, ErrDistributedUnavailable) },
		},
		{
			name: "allowlist", yaml: valid,
			allow: func(u string) error {
				if u != "http://127.0.0.1:1" {
					return errors.New("unexpected url " + u)
				}
				return errBlocked
			},
			check: func(err error) bool { return errors.Is(err, ErrTargetNotAllowed) && errors.Is(err, errBlocked) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			m := newManager(t, st, func(o *Options) { o.Allow = tc.allow })
			_, err := m.Start(context.Background(), testFor(tc.yaml), tc.req)
			if err == nil {
				t.Fatal("Start succeeded, want error")
			}
			if tc.check != nil && !tc.check(err) {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.details != "" {
				var vd interface{ ValidationDetails() []string }
				if !errors.As(fmt.Errorf("wrapped: %w", err), &vd) || len(vd.ValidationDetails()) == 0 {
					t.Fatalf("error %v does not expose ValidationDetails through wrapping", err)
				}
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("error %T %v is not a *ValidationError", err, err)
				}
				if !strings.Contains(strings.Join(ve.Details, "\n"), tc.details) {
					t.Fatalf("details %q do not mention %q", ve.Details, tc.details)
				}
			}
			if n := st.runCount(); n != 0 {
				t.Fatalf("%d runs created, want 0", n)
			}
		})
	}
}

func TestLabelAndNotesPersisted(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv := target(t, http.StatusOK)
	notes := strings.Repeat("n", MaxNotesLen)
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "1s", 5, "/", "")),
		model.StartRunRequest{Label: "  v1.4.2 ", Notes: notes})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Label != "v1.4.2" || run.Notes != notes {
		t.Fatalf("Start label/notes = %q / %d chars", run.Label, len(run.Notes))
	}
	fin := waitFinished(t, st, run.ID, 10*time.Second)
	if fin.Label != "v1.4.2" || fin.Notes != notes {
		t.Fatalf("finished label/notes = %q / %d chars", fin.Label, len(fin.Notes))
	}
}

func TestInvalidScenarioListsEveryProblem(t *testing.T) {
	t.Parallel()
	m := newManager(t, newFakeStore(), nil)
	_, err := m.Start(context.Background(), testFor("name: x\ntarget:\n  base_url: ftp://x\nload:\n  stages: []\nsteps: []\n"), model.StartRunRequest{})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Details) < 3 {
		t.Fatalf("want several details, got %v", err)
	}
}

func TestCSVUniqueExhaustion(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)

	var seen atomic.Int64
	var bad atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if u := r.URL.Query().Get("u"); u != "alice" && u != "bob" && u != "carol" {
			bad.Add(1)
		}
	}))
	t.Cleanup(srv.Close)

	yaml := scenarioYAML(srv.URL, "10s", 20, "/?u={{user}}", "data:\n  file: fixtures/users.csv\n  mode: unique\n")
	test := testFor(yaml)
	// Stored by base name; the scenario's directory prefix is ignored.
	_ = st.PutFile(context.Background(), test.ID, "users.csv", []byte("user\nalice\nbob\ncarol\n"))

	start := time.Now()
	run, err := m.Start(context.Background(), test, model.StartRunRequest{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	fin := waitFinished(t, st, run.ID, 8*time.Second)
	if time.Since(start) > 5*time.Second {
		t.Fatal("run did not stop early on exhausted data")
	}
	if fin.Status != model.RunPassed || fin.Error != "" {
		t.Fatalf("status = %s (err %q), want passed", fin.Status, fin.Error)
	}
	if !hasWarning(fin, "exhausted") {
		t.Fatalf("warnings %q lack data exhaustion", fin.Warnings)
	}
	if seen.Load() != 3 || bad.Load() != 0 || fin.Summary.Requests != 3 {
		t.Fatalf("seen=%d bad=%d summary=%d, want 3 unique rows used", seen.Load(), bad.Load(), fin.Summary.Requests)
	}
}

func TestInvalidCSVIsValidationError(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	test := testFor(scenarioYAML("http://127.0.0.1:1", "1s", 1, "/", "data:\n  file: users.csv\n"))
	_ = st.PutFile(context.Background(), test.ID, "users.csv", []byte("user\n")) // header only
	_, err := m.Start(context.Background(), test, model.StartRunRequest{})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Details[0], "no data rows") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(ve.Error(), "goloadtest-run-") {
		t.Fatalf("temp path leaked: %v", ve)
	}
	if st.runCount() != 0 {
		t.Fatal("run created")
	}
}

func TestSubscribeReceivesStatusMetricsDone(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv := target(t, http.StatusOK)

	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "2s", 10, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	var got []Event
	timeout := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatal("channel not closed after run end")
		}
	}
	if len(got) < 3 {
		t.Fatalf("got %d events, want status + metrics + done", len(got))
	}
	if got[0].Type != EventStatus || got[0].Run == nil || got[0].Run.Status != model.RunRunning {
		t.Fatalf("first event = %+v", got[0])
	}
	last := got[len(got)-1]
	if last.Type != EventDone || last.Run == nil || last.Run.Status != model.RunPassed || last.Run.Summary == nil {
		t.Fatalf("last event = %+v", last)
	}
	metricsSeen, statsSeen := 0, 0
	for _, ev := range got[1 : len(got)-1] {
		switch {
		case ev.Type == EventMetric && ev.Metric != nil:
			metricsSeen++
		case ev.Type == EventStats && ev.Stats != nil:
			statsSeen++
		default:
			t.Fatalf("unexpected middle event %+v", ev)
		}
	}
	if statsSeen < 2 {
		t.Fatalf("got %d stats events, want >= 2", statsSeen)
	}
	if metricsSeen < 2 {
		t.Fatalf("got %d metric events, want >= 2", metricsSeen)
	}
	cancel() // idempotent after close
	cancel()
}

func TestSubscribeCancel(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	srv := target(t, http.StatusOK)
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "30s", 5, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cancel()
	for range events { // drains the status event, then must be closed
	}
	_ = m.Stop(run.ID)
	waitFinished(t, st, run.ID, 5*time.Second)
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	m.subBuffer = 1 // status alone fills the buffer
	srv := target(t, http.StatusOK)

	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "2s", 10, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Never read while running: the run must still finish on time.
	fin := waitFinished(t, st, run.ID, 6*time.Second)
	if fin.Status != model.RunPassed {
		t.Fatalf("status = %s", fin.Status)
	}
	points, _ := st.ListMetrics(context.Background(), run.ID)
	if len(points) < 2 {
		t.Fatalf("metrics were not stored while subscriber was stuck: %d", len(points))
	}

	var got []Event
	deadline := time.After(5 * time.Second)
	for closed := false; !closed; {
		select {
		case ev, ok := <-events:
			if !ok {
				closed = true
				break
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatal("channel not closed")
		}
	}
	if len(got) != 1 || got[0].Type != EventDone {
		t.Fatalf("slow subscriber got %+v, want only done (metrics dropped)", got)
	}
}

func TestShutdownStopsActiveRuns(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := NewManager(Options{Store: st, Logger: quietLogger()})
	srv := target(t, http.StatusOK)

	var ids []string
	for i := 0; i < 2; i++ {
		r, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "30s", 10, "/", "")), model.StartRunRequest{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if len(m.Active()) != 2 {
		t.Fatalf("Active = %v", m.Active())
	}
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, id := range ids {
		r, _ := st.GetRun(context.Background(), id)
		if r.Status != model.RunStopped || r.FinishedAt == nil {
			t.Fatalf("run %s after shutdown: %s", id, r.Status)
		}
	}
	if len(m.Active()) != 0 {
		t.Fatalf("Active after shutdown = %v", m.Active())
	}
	if _, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "1s", 1, "/", "")), model.StartRunRequest{}); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start after Shutdown = %v", err)
	}
}

func TestWorkersState(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := newManager(t, st, func(o *Options) { o.Now = func() time.Time { return fixed } })
	srv := target(t, http.StatusOK)

	ws := m.Workers()
	if len(ws) != 1 {
		t.Fatalf("workers = %+v", ws)
	}
	w := ws[0]
	if w.ID != "local" || w.Addr != "in-process" || w.State != "idle" || w.Version != "test-1" || !w.LastSeen.Equal(fixed) || w.RunID != "" {
		t.Fatalf("idle worker = %+v", w)
	}

	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "30s", 10, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !run.StartedAt.Equal(fixed) {
		t.Fatalf("StartedAt = %v, want injected clock", run.StartedAt)
	}
	if w := m.Workers()[0]; w.State != "busy" || w.RunID != run.ID {
		t.Fatalf("busy worker = %+v", w)
	}
	waitMetrics(t, st, run.ID, 1, 5*time.Second)
	if w := m.Workers()[0]; w.CPU < 0 || w.CPU > 1.5 {
		t.Fatalf("cpu = %v", w.CPU)
	}
	if err := m.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	fin := waitFinished(t, st, run.ID, 5*time.Second)
	if fin.FinishedAt == nil || !fin.FinishedAt.Equal(fixed) {
		t.Fatalf("FinishedAt = %v", fin.FinishedAt)
	}
	// finish() persists before the run leaves the active set; allow a beat.
	deadline := time.Now().Add(2 * time.Second)
	for m.Workers()[0].State != "idle" {
		if time.Now().After(deadline) {
			t.Fatalf("worker still %+v", m.Workers()[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStoreFailuresDoNotCrashRun(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	st.failMetrics = true
	m := newManager(t, st, nil)
	srv := target(t, http.StatusInternalServerError)

	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "1200ms", 10, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	fin := waitFinished(t, st, run.ID, 8*time.Second)
	if fin.Status != model.RunPassed { // no thresholds: errors alone do not fail a run
		t.Fatalf("status = %s (err %q)", fin.Status, fin.Error)
	}
	if fin.Summary == nil || fin.Summary.Errors == 0 {
		t.Fatalf("summary = %+v", fin.Summary)
	}
}

func TestConcurrentRunsAreIndependent(t *testing.T) {
	t.Parallel()
	st := newFakeStore()
	m := newManager(t, st, nil)
	ok := target(t, http.StatusOK)
	bad := target(t, http.StatusServiceUnavailable)
	th := "thresholds:\n  - metric: error_rate\n    op: lt\n    value: 0.5\n"

	r1, err := m.Start(context.Background(), testFor(scenarioYAML(ok.URL, "1500ms", 10, "/", th)), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := m.Start(context.Background(), testFor(scenarioYAML(bad.URL, "1500ms", 10, "/", th)), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if f := waitFinished(t, st, r1.ID, 8*time.Second); f.Status != model.RunPassed {
		t.Fatalf("r1 = %s", f.Status)
	}
	if f := waitFinished(t, st, r2.ID, 8*time.Second); f.Status != model.RunFailed {
		t.Fatalf("r2 = %s", f.Status)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("short", 10); got != "short" {
		t.Fatal(got)
	}
	got := truncate(strings.Repeat("é", 10), 5) // 2-byte runes; cut must not split one
	if got != "éé…" {
		t.Fatalf("got %q", got)
	}
}
