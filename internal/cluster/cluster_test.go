package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster/pb"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// startMaster serves a Master on a random local port.
func startMaster(t *testing.T, opts MasterOptions) (*Master, string) {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = quiet
	}
	m := NewMaster(opts)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	m.Register(s)
	go s.Serve(lis)
	t.Cleanup(s.Stop)
	return m, lis.Addr().String()
}

type testWorker struct {
	cancel context.CancelFunc
	done   chan error
}

// startWorker runs an in-process worker until the test ends (or cancel).
func startWorker(t *testing.T, addr, id, token string) *testWorker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &testWorker{cancel: cancel, done: make(chan error, 1)}
	go func() {
		w.done <- RunWorker(ctx, WorkerOptions{
			MasterAddr: addr,
			Token:      token,
			Insecure:   true,
			ID:         id,
			Logger:     quiet,
			heartbeat:  200 * time.Millisecond,
			minBackoff: 50 * time.Millisecond,
			maxBackoff: 200 * time.Millisecond,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			t.Errorf("worker %s did not stop", id)
		}
	})
	return w
}

func startWorkers(t *testing.T, m *Master, addr string, n int) []*testWorker {
	t.Helper()
	ws := make([]*testWorker, n)
	for i := range ws {
		ws[i] = startWorker(t, addr, fmt.Sprintf("w%d", i+1), "")
	}
	waitFor(t, "workers idle", func() bool { return m.Idle() == n })
	return ws
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// collect drains a run and returns its seconds and error-sample count.
// onSecond (optional) is called for each second as it arrives.
func collect(t *testing.T, run RemoteRun, onSecond func(Second)) ([]Second, int) {
	t.Helper()
	var secs []Second
	samples := 0
	timeout := time.After(30 * time.Second)
	secCh, errCh := run.Seconds(), run.Errors()
	for secCh != nil || errCh != nil {
		select {
		case s, ok := <-secCh:
			if !ok {
				secCh = nil
				continue
			}
			secs = append(secs, s)
			if onSecond != nil {
				onSecond(s)
			}
		case b, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			samples += len(b)
		case <-timeout:
			t.Fatal("run did not finish")
		}
	}
	select {
	case <-run.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after Seconds")
	}
	for i := 1; i < len(secs); i++ {
		if secs[i].Index <= secs[i-1].Index {
			t.Fatalf("seconds out of order: %d after %d", secs[i].Index, secs[i-1].Index)
		}
	}
	return secs, samples
}

func totalRequests(secs []Second) int64 {
	var n int64
	for _, s := range secs {
		n += s.Snapshot.Total.Requests
	}
	return n
}

// flatOpen is an open-model scenario at rps for dur (after a 1ms ramp).
func flatOpen(baseURL string, rps int, dur string, extra string) string {
	return fmt.Sprintf(`name: dist
target:
  base_url: %s
load:
  model: open
  stages:
    - {duration: 1ms, target: %d}
    - {duration: %s, target: %d}
%s
steps:
  - name: get
    path: /hit
`, baseURL, rps, dur, rps, extra)
}

func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestDistributedRun(t *testing.T) {
	srv, hits := countingServer(t)
	m, addr := startMaster(t, MasterOptions{})
	startWorkers(t, m, addr, 3)

	run, err := m.Start(context.Background(), RunSpec{
		RunID:   "r1",
		YAML:    flatOpen(srv.URL, 300, "3s", ""),
		StartAt: time.Now().Add(300 * time.Millisecond),
	}, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range m.Workers() {
		if w.State != StateBusy || w.RunID != "r1" {
			t.Errorf("worker %s: state %q run %q during the run", w.ID, w.State, w.RunID)
		}
	}
	secs, _ := collect(t, run, nil)

	res := run.Result()
	if res.Err != nil || res.DataExhausted || len(res.LostWorkers) != 0 {
		t.Fatalf("result = %+v", res)
	}
	total := totalRequests(secs)
	t.Logf("merged requests = %d over %d seconds, target server saw %d", total, len(secs), hits.Load())
	if total < 873 || total > 927 {
		t.Errorf("merged total = %d, want 900 ±3%%", total)
	}
	if total != hits.Load() {
		t.Errorf("merged total %d != requests seen by the target %d", total, hits.Load())
	}
	for _, s := range secs {
		if s.Workers != 3 {
			t.Errorf("second %d: Workers = %d, want 3", s.Index, s.Workers)
		}
		var byStep int64
		for _, st := range s.Snapshot.ByStep {
			byStep += st.Requests
		}
		if byStep != s.Snapshot.Total.Requests {
			t.Errorf("second %d: by-step sum %d != total %d", s.Index, byStep, s.Snapshot.Total.Requests)
		}
		if s.Snapshot.Total.Hist.TotalCount() != s.Snapshot.Total.Requests {
			t.Errorf("second %d: merged histogram count %d != requests %d", s.Index, s.Snapshot.Total.Hist.TotalCount(), s.Snapshot.Total.Requests)
		}
		if s.Index <= 2 && (s.Target < 299 || s.Target > 301) {
			t.Errorf("second %d: target = %v, want 300", s.Index, s.Target)
		}
	}
	if len(secs) < 3 || secs[0].Index != 1 {
		t.Errorf("got seconds %v", indices(secs))
	}
	if w, ok := run.(WarningReporter); !ok || len(w.Warnings()) != 0 {
		t.Errorf("warnings: %v", w.Warnings())
	}
	waitFor(t, "workers idle again", func() bool { return m.Idle() == 3 })
}

func indices(secs []Second) []int64 {
	out := make([]int64, len(secs))
	for i, s := range secs {
		out[i] = s.Index
	}
	return out
}

func TestTokenAuth(t *testing.T) {
	m, addr := startMaster(t, MasterOptions{Token: "s3cret"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := RunWorker(ctx, WorkerOptions{MasterAddr: addr, Token: "wrong", Insecure: true, ID: "bad", Logger: quiet})
	if status.Code(errors.Unwrap(err)) != codes.Unauthenticated {
		t.Fatalf("wrong token: err = %v, want Unauthenticated", err)
	}
	err = RunWorker(ctx, WorkerOptions{MasterAddr: addr, Insecure: true, ID: "none", Logger: quiet})
	if status.Code(errors.Unwrap(err)) != codes.Unauthenticated {
		t.Fatalf("no token: err = %v, want Unauthenticated", err)
	}
	if n := len(m.Workers()); n != 0 {
		t.Fatalf("%d workers registered after rejected connections", n)
	}

	startWorker(t, addr, "good", "s3cret")
	waitFor(t, "authorized worker", func() bool { return m.Idle() == 1 })
}

func TestWorkerLostMidRun(t *testing.T) {
	srv, _ := countingServer(t)
	m, addr := startMaster(t, MasterOptions{})
	ws := startWorkers(t, m, addr, 3)

	run, err := m.Start(context.Background(), RunSpec{
		RunID:   "r-lost",
		YAML:    flatOpen(srv.URL, 150, "3s", ""),
		StartAt: time.Now().Add(200 * time.Millisecond),
	}, 3)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var lostAfter int64
	secs, _ := collect(t, run, func(s Second) {
		once.Do(func() {
			lostAfter = s.Index
			ws[1].cancel() // w2 goes away
		})
	})
	res := run.Result()
	if len(res.LostWorkers) != 1 || res.LostWorkers[0] != "w2" {
		t.Fatalf("LostWorkers = %v, want [w2]", res.LostWorkers)
	}
	if res.Err != nil {
		t.Fatalf("Err = %v", res.Err)
	}
	last := secs[len(secs)-1]
	if last.Index < 3 {
		t.Fatalf("run ended early at second %d", last.Index)
	}
	for _, s := range secs {
		if s.Index > lostAfter+1 && s.Workers != 2 {
			t.Errorf("second %d after the loss: Workers = %d, want 2", s.Index, s.Workers)
		}
	}
	var lostState string
	for _, w := range m.Workers() {
		if w.ID == "w2" {
			lostState = w.State
		}
	}
	if lostState != StateLost {
		t.Errorf("w2 state = %q, want lost", lostState)
	}
	waitFor(t, "survivors idle", func() bool { return m.Idle() == 2 })
}

func TestStopMidRun(t *testing.T) {
	srv, _ := countingServer(t)
	m, addr := startMaster(t, MasterOptions{})
	startWorkers(t, m, addr, 2)

	run, err := m.Start(context.Background(), RunSpec{
		RunID:   "r-stop",
		YAML:    flatOpen(srv.URL, 50, "30s", ""),
		StartAt: time.Now().Add(100 * time.Millisecond),
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	secs, _ := collect(t, run, func(Second) { run.Stop(); run.Stop() })
	if el := time.Since(began); el > 5*time.Second {
		t.Fatalf("stop took %v", el)
	}
	res := run.Result()
	if res.Err != nil || len(res.LostWorkers) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(secs) == 0 || totalRequests(secs) == 0 {
		t.Fatalf("no traffic before stop: %v", indices(secs))
	}
	waitFor(t, "workers idle after stop", func() bool { return m.Idle() == 2 })
}

func TestNotEnoughWorkers(t *testing.T) {
	m, addr := startMaster(t, MasterOptions{})
	startWorkers(t, m, addr, 1)
	_, err := m.Start(context.Background(), RunSpec{RunID: "r", YAML: "x"}, 2)
	if !errors.Is(err, ErrNotEnoughWorkers) {
		t.Fatalf("err = %v, want ErrNotEnoughWorkers", err)
	}
	if m.Idle() != 1 {
		t.Fatalf("idle = %d after failed start", m.Idle())
	}
}

func TestWorkerReportsScenarioError(t *testing.T) {
	m, addr := startMaster(t, MasterOptions{})
	startWorkers(t, m, addr, 2)
	run, err := m.Start(context.Background(), RunSpec{RunID: "r-bad", YAML: "name: [", StartAt: time.Now()}, 2)
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run, nil)
	if res := run.Result(); res.Err == nil || !strings.Contains(res.Err.Error(), "scenario") {
		t.Fatalf("Err = %v, want a scenario error", res.Err)
	}
	waitFor(t, "workers idle", func() bool { return m.Idle() == 2 })
}

func TestUniqueDataPartition(t *testing.T) {
	const rows = 30
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Query().Get("u")]++
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	var csv strings.Builder
	csv.WriteString("username\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&csv, "user%02d\n", i)
	}
	yaml := fmt.Sprintf(`name: unique
target:
  base_url: %s
load:
  stages:
    - {duration: 1ms, target: 60}
    - {duration: 2s, target: 60}
data:
  file: users.csv
  mode: unique
steps:
  - name: get
    path: /?u={{username}}
`, srv.URL)

	m, addr := startMaster(t, MasterOptions{})
	startWorkers(t, m, addr, 3)
	run, err := m.Start(context.Background(), RunSpec{
		RunID:   "r-unique",
		YAML:    yaml,
		Files:   map[string][]byte{"users.csv": []byte(csv.String())},
		StartAt: time.Now().Add(100 * time.Millisecond),
	}, 3)
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := collect(t, run, nil)
	res := run.Result()
	if res.Err != nil || !res.DataExhausted {
		t.Fatalf("result = %+v, want data exhausted without error", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != rows {
		t.Errorf("target saw %d distinct users, want %d", len(seen), rows)
	}
	for u, n := range seen {
		if n != 1 {
			t.Errorf("user %q used %d times", u, n)
		}
	}
	if got := totalRequests(secs); got != rows {
		t.Errorf("merged requests = %d, want %d", got, rows)
	}
}

func TestHeartbeatTimeout(t *testing.T) {
	m, addr := startMaster(t, MasterOptions{HeartbeatTimeout: 300 * time.Millisecond})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := pb.NewCoordinatorClient(conn).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.WorkerMessage{Msg: &pb.WorkerMessage_Hello{Hello: &pb.Hello{WorkerId: "silent"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "registration", func() bool { return m.Idle() == 1 })
	// Say nothing more: the watchdog must fail the stream.
	_, err = stream.Recv()
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("Recv err = %v, want DeadlineExceeded", err)
	}
	ws := m.Workers()
	if len(ws) != 1 || ws[0].State != StateLost {
		t.Fatalf("workers = %+v, want one lost", ws)
	}
	if m.Idle() != 0 {
		t.Fatal("lost worker counted as idle")
	}
}

func TestFirstMessageMustBeHello(t *testing.T) {
	_, addr := startMaster(t, MasterOptions{})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stream, err := pb.NewCoordinatorClient(conn).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&pb.WorkerMessage{Msg: &pb.WorkerMessage_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}
