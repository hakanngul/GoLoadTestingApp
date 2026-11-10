package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// idTarget hands out a random UUID on /new and answers /items/{id} with
// 404 and a JSON error that embeds the id and a timestamp, so every
// failure message is different before normalization.
func idTarget(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var misses atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/new":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": newUUID()})
		case strings.HasPrefix(r.URL.Path, "/items/"):
			misses.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/items/")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("item %s not found at %s", id, time.Now().UTC().Format(time.RFC3339Nano)),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &misses
}

// newUUID returns a random UUID-shaped string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	h := fmt.Sprintf("%x", b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func idYAML(baseURL string) string {
	return fmt.Sprintf(`name: ids
target:
  base_url: %s
  timeout: 2s
load:
  model: open
  stages:
    - duration: 1500ms
      target: 200
steps:
  - name: new
    path: /new
    extract:
      - var: id
        from: json
        expr: id
  - name: get
    path: /items/{{id}}
    assert:
      - type: json
        expr: error
        value: ok
      - type: status
        value: "200"
`, baseURL)
}

func TestRunGroupsFailuresWithRandomIDs(t *testing.T) {
	t.Parallel()
	checkIDRun(t, nil, model.StartRunRequest{})
}

// The normalized message and the raw sample survive the worker -> master
// path (proto ErrorCount.sample, master-side merge).
func TestRemoteRunGroupsFailuresWithRandomIDs(t *testing.T) {
	c := newTestCluster(t)
	c.addWorker("w1")
	c.addWorker("w2")
	c.waitIdle(2)
	checkIDRun(t, func(o *Options) { o.Dispatcher = c.master }, model.StartRunRequest{Workers: 2})
}

// checkIDRun runs idYAML and expects exactly one error group counting
// every 404, with a normalized message and a raw sample.
func checkIDRun(t *testing.T, opt func(*Options), req model.StartRunRequest) {
	t.Helper()
	st := newFakeStore()
	m := newManager(t, st, opt)
	srv, misses := idTarget(t)
	run, err := m.Start(context.Background(), testFor(idYAML(srv.URL)), req)
	if err != nil {
		t.Fatal(err)
	}
	events, cancel, err := m.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	evs := collect(t, events, 30*time.Second)
	if done := evs[len(evs)-1]; done.Type != EventDone {
		t.Fatalf("last event %+v", done)
	}
	if misses.Load() < 100 {
		t.Fatalf("target saw only %d misses", misses.Load())
	}
	stored, err := st.ListErrorGroups(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ls, _ := lastStats(t, evs)
	for what, got := range map[string][]model.ErrorGroup{"stored": stored, "live": ls.Errors} {
		if len(got) != 1 || got[0].Step != "get" || got[0].Status != 404 || got[0].Count != misses.Load() {
			t.Fatalf("%s groups = %+v, want 1 group of %d", what, got, misses.Load())
		}
		g := got[0]
		if !strings.Contains(g.Message, `got "item {uuid} not found at {ts}"`) {
			t.Fatalf("%s message = %q", what, g.Message)
		}
		if g.Sample == g.Message || !strings.Contains(g.Sample, "item ") || strings.Contains(g.Sample, "{uuid}") {
			t.Fatalf("%s sample = %q", what, g.Sample)
		}
	}
}
