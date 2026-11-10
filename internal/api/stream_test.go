package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func fieldErr(path, msg string) error { return &scenario.FieldError{Path: path, Msg: msg} }

// sseFrame is one parsed SSE message (or a comment).
type sseFrame struct {
	event   string
	data    string
	comment string
}

// readFrame reads the next SSE frame (terminated by a blank line).
func readFrame(br *bufio.Reader) (sseFrame, error) {
	var f sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			return f, nil
		case strings.HasPrefix(line, ":"):
			f.comment = strings.TrimSpace(line[1:])
		case strings.HasPrefix(line, "event: "):
			f.event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			f.data = line[len("data: "):]
		}
	}
}

// startStreamEnv serves the API over a real listener (needed for flushing
// and client disconnects) and wraps handleStream to signal when it returns.
func startStreamEnv(t *testing.T, heartbeat time.Duration) (*env, *httptest.Server, <-chan struct{}) {
	t.Helper()
	e := newEnv(t)
	e.srv.heartbeat = heartbeat
	handlerDone := make(chan struct{}, 8)
	inner := e.h
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if strings.HasSuffix(r.URL.Path, "/stream") {
				handlerDone <- struct{}{}
			}
		}()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return e, ts, handlerDone
}

func openStream(t *testing.T, ctx context.Context, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestStreamLive(t *testing.T) {
	e, ts, handlerDone := startStreamEnv(t, time.Hour)
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunRunning})
	events, cancelled := e.runner.activate(run.ID)

	resp := openStream(t, context.Background(), ts.URL+"/api/runs/"+run.ID+"/stream")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for k, want := range map[string]string{
		"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	br := bufio.NewReader(resp.Body)

	// Each event must be flushed individually: send one, read one.
	running := run
	running.Status = model.RunRunning
	events <- runner.Event{Type: runner.EventStatus, Run: &running}
	f, err := readFrame(br)
	if err != nil || f.event != "status" {
		t.Fatalf("frame = %+v, %v", f, err)
	}
	var gotRun model.Run
	if err := json.Unmarshal([]byte(f.data), &gotRun); err != nil || gotRun.ID != run.ID || gotRun.Status != model.RunRunning {
		t.Fatalf("status data %q: %v", f.data, err)
	}

	events <- runner.Event{Type: runner.EventMetric, Metric: &model.MetricPoint{Elapsed: 1, RPS: 42}}
	f, err = readFrame(br)
	if err != nil || f.event != "metric" {
		t.Fatalf("frame = %+v, %v", f, err)
	}
	var mp model.MetricPoint
	if err := json.Unmarshal([]byte(f.data), &mp); err != nil || mp.RPS != 42 {
		t.Fatalf("metric data %q: %v", f.data, err)
	}

	finished := run
	finished.Status = model.RunPassed
	events <- runner.Event{Type: runner.EventDone, Run: &finished}
	f, err = readFrame(br)
	if err != nil || f.event != "done" || !strings.Contains(f.data, `"status":"passed"`) {
		t.Fatalf("frame = %+v, %v", f, err)
	}

	// The handler returns after done (stream ends) and cancels its subscription.
	if _, err := readFrame(br); err != io.EOF {
		t.Fatalf("after done: %v, want EOF", err)
	}
	waitClosed(t, handlerDone, "handler exit")
	waitClosed(t, cancelled, "subscription cancel")
}

func TestStreamChannelClose(t *testing.T) {
	e, ts, handlerDone := startStreamEnv(t, time.Hour)
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunRunning})
	events, cancelled := e.runner.activate(run.ID)

	resp := openStream(t, context.Background(), ts.URL+"/api/runs/"+run.ID+"/stream")
	close(events)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, handlerDone, "handler exit")
	waitClosed(t, cancelled, "subscription cancel")
}

func TestStreamHeartbeat(t *testing.T) {
	e, ts, _ := startStreamEnv(t, 20*time.Millisecond)
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunRunning})
	events, _ := e.runner.activate(run.ID)
	defer close(events)

	resp := openStream(t, context.Background(), ts.URL+"/api/runs/"+run.ID+"/stream")
	f, err := readFrame(bufio.NewReader(resp.Body))
	if err != nil || f.comment != "ping" || f.event != "" {
		t.Fatalf("frame = %+v, %v", f, err)
	}
}

func TestStreamClientDisconnect(t *testing.T) {
	e, ts, handlerDone := startStreamEnv(t, time.Hour)
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunRunning})
	events, cancelled := e.runner.activate(run.ID)

	ctx, cancel := context.WithCancel(context.Background())
	resp := openStream(t, ctx, ts.URL+"/api/runs/"+run.ID+"/stream")
	events <- runner.Event{Type: runner.EventMetric, Metric: &model.MetricPoint{}}
	if f, err := readFrame(bufio.NewReader(resp.Body)); err != nil || f.event != "metric" {
		t.Fatalf("frame = %+v, %v", f, err)
	}

	cancel() // client goes away; no further events are sent
	waitClosed(t, handlerDone, "handler exit after disconnect")
	waitClosed(t, cancelled, "subscription cancel after disconnect")
}

func TestStreamFinishedRun(t *testing.T) {
	e, ts, handlerDone := startStreamEnv(t, time.Hour)
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunFailed, TestName: "x"})

	resp := openStream(t, context.Background(), ts.URL+"/api/runs/"+run.ID+"/stream")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, ct %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(strings.NewReader(string(body)))
	f, err := readFrame(br)
	if err != nil || f.event != "done" {
		t.Fatalf("frame = %+v, %v (body %q)", f, err, body)
	}
	var got model.Run
	if err := json.Unmarshal([]byte(f.data), &got); err != nil || got.ID != run.ID || got.Status != model.RunFailed {
		t.Fatalf("done data %q: %v", f.data, err)
	}
	if _, err := readFrame(br); err != io.EOF {
		t.Fatalf("expected exactly one frame, body %q", body)
	}
	waitClosed(t, handlerDone, "handler exit")
}

func TestStreamUnknownRun(t *testing.T) {
	e := newEnv(t)
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/stream", nil), 404, "not found")
}
