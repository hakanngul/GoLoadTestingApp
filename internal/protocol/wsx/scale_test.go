package wsx

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// The scale tests hold many connections from this process to a WebSocket
// server running in a child process (the test binary re-executed as
// TestHelperWSServer). That keeps the memory numbers client-only and
// splits the file descriptors (one per connection on each side) across
// two processes, each subject to its own RLIMIT_NOFILE.

const helperEnv = "GOLOAD_WS_HELPER"

// TestHelperWSServer is not a real test: when run as a child process with
// GOLOAD_WS_HELPER=1 it serves WebSockets on a loopback port, prints the
// address, and exits when its stdin is closed.
func TestHelperWSServer(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process for the scale tests")
	}
	raiseNoFile()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			if strings.Contains(string(msg), `"join"`) {
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"joined"}`))
			}
		}
	})}
	go srv.Serve(ln)
	fmt.Println("ADDR", ln.Addr().String())
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // until parent closes stdin
	os.Exit(0)
}

// startHelper launches the helper server and returns its http:// URL.
func startHelper(t testing.TB) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWSServer$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if addr, ok := strings.CutPrefix(sc.Text(), "ADDR "); ok {
			go func() {
				for sc.Scan() { // drain
				}
			}()
			return "http://" + addr
		}
	}
	t.Fatal("helper server did not report an address")
	return ""
}

// raiseNoFile lifts the soft open-files limit to the hard limit.
func raiseNoFile() uint64 {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0
	}
	if lim.Cur < lim.Max {
		lim.Cur = lim.Max
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim)
		_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim)
	}
	return lim.Cur
}

// holdMany holds n concurrent connections (one goroutine = one closed
// model VU running a single iteration: connect, join + expect, hold with
// pings) and reports memory per connection measured while all are held.
func holdMany(t *testing.T, n int, hold time.Duration) {
	if limit := raiseNoFile(); limit < uint64(n)+256 {
		t.Skipf("RLIMIT_NOFILE %d too low for %d connections", limit, n)
	}
	base := startHelper(t)
	e := newExec(t, base, 60*time.Second, wsStep("chat", "/ws", scenario.WSStep{
		Hold:        dur(hold),
		Messages:    []scenario.WSMessage{{Send: `{"type":"join"}`, Expect: `"joined"`}},
		PingEvery:   dur(time.Second),
		PingMessage: `{"type":"ping"}`,
	}))

	var connected, okN, failN atomic.Int64
	var firstErr atomic.Value
	sink := func(r protocol.Result) {
		if r.Err != nil {
			failN.Add(1)
			firstErr.CompareAndSwap(nil, r.Step+": "+r.Err.Error())
			return
		}
		okN.Add(1)
		if r.Step == "chat:1" {
			connected.Add(1)
		}
	}

	runtime.GC()
	var before, during runtime.MemStats
	runtime.ReadMemStats(&before)
	g0 := runtime.NumGoroutine()

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Iterate(context.Background(), nil, time.Now(), sink)
		}()
	}
	// Wait until every connection has joined (or failed), then measure
	// while they are all being held.
	for connected.Load()+failN.Load() < int64(n) {
		if time.Since(start) > hold {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rampUp := time.Since(start)
	held := connected.Load()
	g1 := runtime.NumGoroutine()
	runtime.GC()
	runtime.ReadMemStats(&during)
	wg.Wait()
	total := time.Since(start)

	if failN.Load() > 0 {
		t.Fatalf("%d failed results; first: %v", failN.Load(), firstErr.Load())
	}
	if held != int64(n) {
		t.Fatalf("only %d of %d connections were up at the same time (ramp-up %v)", held, n, rampUp)
	}
	if okN.Load() != int64(2*n) {
		t.Fatalf("ok results = %d, want %d", okN.Load(), 2*n)
	}
	perConn := func(a, b uint64) float64 { return float64(int64(b)-int64(a)) / float64(n) / 1024 }
	t.Logf("%d connections: all up after %v, iteration wall time %v", n, rampUp.Round(time.Millisecond), total.Round(time.Millisecond))
	t.Logf("per connection while held: heap-in-use %.1f KiB, stack-in-use %.1f KiB, total Sys %.1f KiB; goroutines %d -> %d (%.2f per conn)",
		perConn(before.HeapInuse, during.HeapInuse),
		perConn(before.StackInuse, during.StackInuse),
		perConn(before.Sys, during.Sys),
		g0, g1, float64(g1-g0)/float64(n))
}

func TestWSScale2k(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test skipped in -short mode")
	}
	holdMany(t, 2000, 2*time.Second)
}

// TestWS10k holds 10,000 concurrent connections (plan goal). Run with:
//
//	GOLOAD_WS_10K=1 go test -run TestWS10k -v ./internal/protocol/wsx/
//
// without -race: the race detector caps a process at 8128 goroutines.
func TestWS10k(t *testing.T) {
	if os.Getenv("GOLOAD_WS_10K") != "1" {
		t.Skip("set GOLOAD_WS_10K=1 to hold 10,000 connections")
	}
	holdMany(t, 10000, 10*time.Second)
}
