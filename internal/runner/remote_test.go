package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/provision"
)

// testCluster runs a real gRPC master on 127.0.0.1 and lets tests attach
// in-process workers.
type testCluster struct {
	t      *testing.T
	master *cluster.Master
	addr   string
	wg     sync.WaitGroup
}

func newTestCluster(t *testing.T) *testCluster {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := cluster.NewMaster(cluster.MasterOptions{Token: "s3cret", Logger: quietLogger()})
	srv := grpc.NewServer()
	m.Register(srv)
	go srv.Serve(lis)
	c := &testCluster{t: t, master: m, addr: lis.Addr().String()}
	t.Cleanup(func() {
		srv.Stop()
		c.wg.Wait()
	})
	return c
}

// addWorker starts a worker and returns a func that kills it.
func (c *testCluster) addWorker(id string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		_ = cluster.RunWorker(ctx, cluster.WorkerOptions{
			MasterAddr:  c.addr,
			Token:       "s3cret",
			Insecure:    true,
			ID:          id,
			Logger:      quietLogger(),
			DialOptions: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		})
	}()
	c.t.Cleanup(cancel)
	return cancel
}

func (c *testCluster) waitIdle(n int) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.master.Idle() < n {
		if time.Now().After(deadline) {
			c.t.Fatalf("only %d of %d workers idle", c.master.Idle(), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRemoteRunMergesWorkers(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	c := newTestCluster(t)
	c.addWorker("w1")
	c.addWorker("w2")
	c.waitIdle(2)

	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Dispatcher = c.master })
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "2s", 100, "/", "")), model.StartRunRequest{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if run.Workers != 2 {
		t.Fatalf("Workers = %d", run.Workers)
	}
	final := waitFinished(t, st, run.ID, 20*time.Second)
	if final.Status != model.RunPassed {
		t.Fatalf("status %s (%s) warnings %v", final.Status, final.Error, final.Warnings)
	}
	got := final.Summary.Requests
	if got != hits.Load() {
		t.Errorf("summary requests %d != target hits %d", got, hits.Load())
	}
	// 100 rps × 2.1s ≈ 205 iterations split across two workers.
	if got < 195 || got > 215 {
		t.Errorf("requests = %d, want ≈205", got)
	}
	points, _ := st.ListMetrics(context.Background(), run.ID)
	if len(points) < 2 {
		t.Fatalf("points = %d", len(points))
	}
	for _, p := range points[:len(points)-1] {
		if p.Workers != 2 {
			t.Errorf("point at %.0fs has %d workers", p.Elapsed, p.Workers)
		}
	}
	// Workers lists local pseudo-worker plus the two remote ones.
	if ws := m.Workers(); len(ws) != 3 {
		t.Errorf("Workers() = %d entries", len(ws))
	}
}

func TestRemoteNotEnoughWorkers(t *testing.T) {
	c := newTestCluster(t)
	c.addWorker("w1")
	c.waitIdle(1)
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Dispatcher = c.master })
	_, err := m.Start(context.Background(), testFor(scenarioYAML("http://127.0.0.1:1", "1s", 10, "/", "")), model.StartRunRequest{Workers: 3})
	if err == nil || !errors.Is(err, cluster.ErrNotEnoughWorkers) {
		t.Fatalf("err = %v", err)
	}
	if st.runCount() != 0 {
		t.Error("run record created")
	}
}

func TestRemoteStop(t *testing.T) {
	srv := target(t, 200)
	c := newTestCluster(t)
	c.addWorker("w1")
	c.waitIdle(1)
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Dispatcher = c.master })
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "30s", 50, "/", "")), model.StartRunRequest{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitMetrics(t, st, run.ID, 1, 10*time.Second)
	if err := m.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	final := waitFinished(t, st, run.ID, 10*time.Second)
	if final.Status != model.RunStopped || final.Summary == nil || final.Summary.Requests == 0 {
		t.Fatalf("status %s summary %+v", final.Status, final.Summary)
	}
	c.waitIdle(1)
}

// fakeProvisioner "launches" instances by starting in-process workers.
type fakeProvisioner struct {
	c          *testCluster
	mu         sync.Mutex
	kill       map[string]context.CancelFunc
	terminated []string
}

func (p *fakeProvisioner) Launch(_ context.Context, req provision.LaunchRequest) ([]provision.Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []provision.Instance
	for i := range req.Count {
		id := fmt.Sprintf("i-%s-%d", req.RunID[:4], i)
		p.kill[id] = p.c.addWorker(id)
		out = append(out, provision.Instance{ID: id, Spot: true})
	}
	return out, nil
}

func (p *fakeProvisioner) Terminate(_ context.Context, ids []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		if k := p.kill[id]; k != nil {
			k()
		}
		p.terminated = append(p.terminated, id)
	}
	return nil
}

func (p *fakeProvisioner) List(context.Context, string) ([]provision.Instance, error) {
	return nil, nil
}

func TestRemoteProvisionsAndTerminatesWorkers(t *testing.T) {
	srv := target(t, 200)
	c := newTestCluster(t)
	prov := &fakeProvisioner{c: c, kill: map[string]context.CancelFunc{}}
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) {
		o.Dispatcher = c.master
		o.Provisioner = prov
		o.ProvisionTimeout = 10 * time.Second
	})
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "1s", 40, "/", "")), model.StartRunRequest{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != model.RunQueued {
		t.Errorf("initial status %s, want queued while provisioning", run.Status)
	}
	final := waitFinished(t, st, run.ID, 30*time.Second)
	if final.Status != model.RunPassed || final.Summary.Requests == 0 {
		t.Fatalf("status %s err %q", final.Status, final.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		prov.mu.Lock()
		n := len(prov.terminated)
		prov.mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminated %d instances, want 2", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
