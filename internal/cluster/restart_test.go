package cluster

import (
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// TestWorkersReconnectAfterMasterRestart: the master process dies and a
// new one starts on the same address (plan exit criterion for Phase 6).
// Workers must re-register by themselves and be usable for a new run.
func TestWorkersReconnectAfterMasterRestart(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()

	m1 := NewMaster(MasterOptions{Logger: quiet})
	s1 := grpc.NewServer()
	m1.Register(s1)
	go s1.Serve(lis)

	startWorker(t, addr, "w1", "")
	startWorker(t, addr, "w2", "")
	waitFor(t, "workers on first master", func() bool { return m1.Idle() == 2 })

	// Crash: drop every connection without a graceful goodbye.
	s1.Stop()

	var lis2 net.Listener
	deadline := time.Now().Add(5 * time.Second)
	for {
		if lis2, err = net.Listen("tcp", addr); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("re-listen on %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	m2 := NewMaster(MasterOptions{Logger: quiet})
	s2 := grpc.NewServer()
	m2.Register(s2)
	go s2.Serve(lis2)
	t.Cleanup(s2.Stop)

	waitFor(t, "workers reconnected to new master", func() bool { return m2.Idle() == 2 })

	// And they can run load again.
	srv, hits := countingServer(t)
	run, err := m2.Start(t.Context(), RunSpec{RunID: "after-restart", YAML: flatOpen(srv.URL, 40, "1s", "")}, 2)
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := collect(t, run, nil)
	if res := run.Result(); res.Err != nil || len(res.LostWorkers) > 0 {
		t.Fatalf("result %+v", res)
	}
	if got := totalRequests(secs); got == 0 || got != hits.Load() {
		t.Fatalf("requests merged %d, target saw %d", got, hits.Load())
	}
}
