package cluster

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster/pb"
	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
)

func fakeConns(ids ...string) []*workerConn {
	out := make([]*workerConn, len(ids))
	for i, id := range ids {
		out[i] = &workerConn{id: id, send: make(chan *pb.MasterMessage, 8), gone: make(chan struct{})}
	}
	return out
}

// snapBlob encodes an interval snapshot holding n requests.
func snapBlob(t *testing.T, n int) []byte {
	t.Helper()
	start := time.Now()
	col := metrics.NewCollector(start)
	for i := 0; i < n; i++ {
		col.Record(protocol.Result{Step: "s", Latency: time.Duration(i+1) * time.Millisecond})
	}
	b, err := col.Flush(start.Add(time.Second)).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func metric(t *testing.T, sec int64, n int) *pb.MetricReport {
	return &pb.MetricReport{RunId: "r", Second: sec, Target: 10, Active: 1, Cpu: float64(n) / 100, Snapshot: snapBlob(t, n)}
}

func nextSecond(t *testing.T, r *remoteRun, within time.Duration) Second {
	t.Helper()
	select {
	case s := <-r.Seconds():
		return s
	case <-time.After(within):
		t.Fatal("no second emitted")
		return Second{}
	}
}

func expectNoSecond(t *testing.T, r *remoteRun, within time.Duration) {
	t.Helper()
	select {
	case s := <-r.Seconds():
		t.Fatalf("unexpected second %d (workers %d)", s.Index, s.Workers)
	case <-time.After(within):
	}
}

func TestRunMergesCompleteSecondImmediately(t *testing.T) {
	r := newRemoteRun("r", fakeConns("a", "b"), time.Hour, quiet)
	r.report("a", metric(t, 1, 3))
	expectNoSecond(t, r, 50*time.Millisecond)
	r.report("b", metric(t, 1, 4))
	s := nextSecond(t, r, time.Second)
	if s.Index != 1 || s.Workers != 2 || s.Snapshot.Total.Requests != 7 || s.Target != 20 || s.Active != 2 || s.CPU != 0.04 {
		t.Fatalf("second = %+v (requests %d)", s, s.Snapshot.Total.Requests)
	}
	if s.Snapshot.Total.Hist.TotalCount() != 7 || s.Snapshot.ByStep["s"].Requests != 7 {
		t.Fatal("histograms not merged exactly")
	}
}

func TestRunStragglerGraceAndLateReports(t *testing.T) {
	r := newRemoteRun("r", fakeConns("a", "b"), 100*time.Millisecond, quiet)
	r.report("a", metric(t, 1, 5))
	s := nextSecond(t, r, 2*time.Second) // after the grace, without b
	if s.Index != 1 || s.Workers != 1 || s.Snapshot.Total.Requests != 5 {
		t.Fatalf("second = %+v", s)
	}
	r.report("b", metric(t, 1, 5)) // late: dropped
	r.report("a", metric(t, 2, 1))
	r.report("a", metric(t, 2, 1)) // duplicate: ignored
	r.report("b", metric(t, 2, 2))
	s = nextSecond(t, r, time.Second)
	if s.Index != 2 || s.Workers != 2 || s.Snapshot.Total.Requests != 3 {
		t.Fatalf("second = %+v", s)
	}
	r.finished("a", &pb.RunFinished{RunId: "r"})
	r.finished("b", &pb.RunFinished{RunId: "r"})
	if _, ok := <-r.Seconds(); ok {
		t.Fatal("Seconds not closed")
	}
	<-r.Done()
	w := strings.Join(r.Warnings(), "; ")
	if !strings.Contains(w, "1 worker metric report(s) arrived after") || !strings.Contains(w, "1 duplicate") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestRunOrderingWaitsForLowestSecond(t *testing.T) {
	r := newRemoteRun("r", fakeConns("a", "b"), time.Hour, quiet)
	r.report("a", metric(t, 1, 1))
	r.report("a", metric(t, 2, 1))
	r.report("b", metric(t, 2, 1)) // b skipped second 1 (e.g. slow start)
	expectNoSecond(t, r, 50*time.Millisecond)
	r.report("b", metric(t, 3, 1))
	r.workerLost("b", errors.New("gone")) // stop waiting for b
	if s := nextSecond(t, r, time.Second); s.Index != 1 || s.Workers != 1 {
		t.Fatalf("second = %+v", s)
	}
	if s := nextSecond(t, r, time.Second); s.Index != 2 || s.Workers != 2 {
		t.Fatalf("second = %+v", s)
	}
	expectNoSecond(t, r, 50*time.Millisecond) // second 3 still waits for live a
	r.finished("a", &pb.RunFinished{RunId: "r", Error: "boom", DataExhausted: true})
	if s := nextSecond(t, r, time.Second); s.Index != 3 || s.Workers != 1 {
		t.Fatalf("second = %+v", s)
	}
	for range r.Seconds() {
	}
	<-r.Done()
	res := r.Result()
	if len(res.LostWorkers) != 1 || res.LostWorkers[0] != "b" || !res.DataExhausted || res.Err == nil || !strings.Contains(res.Err.Error(), "boom") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunErrorsNeverBlock(t *testing.T) {
	r := newRemoteRun("r", fakeConns("a"), time.Hour, quiet)
	batch := []*pb.ErrorSample{{Step: "s", Status: 500, Message: "x"}}
	for i := 0; i < errorBatchBuffer+10; i++ {
		r.errorSamples(batch) // nobody reads Errors
	}
	r.finished("a", &pb.RunFinished{RunId: "r"})
	for range r.Seconds() {
	}
	<-r.Done()
	n := 0
	for range r.Errors() {
		n++
	}
	if n != errorBatchBuffer {
		t.Fatalf("buffered %d batches, want %d", n, errorBatchBuffer)
	}
	if w := strings.Join(r.Warnings(), ";"); !strings.Contains(w, "10 error sample batch") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestRunStopSendsStopRun(t *testing.T) {
	conns := fakeConns("a", "b")
	r := newRemoteRun("r", conns, time.Hour, quiet)
	r.setSent("a", true)
	r.setSent("b", true)
	r.finished("b", &pb.RunFinished{RunId: "r"})
	r.Stop()
	r.Stop()
	select {
	case msg := <-conns[0].send:
		if msg.GetStop().GetRunId() != "r" {
			t.Fatalf("got %v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("StopRun not sent")
	}
	select {
	case msg := <-conns[0].send:
		t.Fatalf("second StopRun: %v", msg)
	case msg := <-conns[1].send:
		t.Fatalf("StopRun sent to finished worker: %v", msg)
	case <-time.After(50 * time.Millisecond):
	}
	r.finished("a", &pb.RunFinished{RunId: "r"})
	for range r.Seconds() {
	}
	<-r.Done()
}

func errMetric(t *testing.T, sec int64, step string, count int64) *pb.MetricReport {
	m := metric(t, sec, 1)
	m.ErrorCounts = []*pb.ErrorCount{{Step: step, Status: 503, Message: "status 503", Count: count, FirstUnixNano: 1, LastUnixNano: 2}}
	return m
}

func sumErrors(ss ...Second) int64 {
	var n int64
	for _, s := range ss {
		for _, e := range s.Errors {
			n += e.Count
		}
	}
	return n
}

func TestRunErrorCountsMergedAndLateCarried(t *testing.T) {
	r := newRemoteRun("r", fakeConns("a", "b"), 100*time.Millisecond, quiet)
	r.report("a", errMetric(t, 1, "x", 3))
	s1 := nextSecond(t, r, 2*time.Second) // grace expired without b
	if sumErrors(s1) != 3 {
		t.Fatalf("second 1 errors = %+v", s1.Errors)
	}
	r.report("b", errMetric(t, 1, "x", 4)) // late: snapshot dropped, errors carried
	r.report("a", errMetric(t, 2, "x", 1))
	r.report("b", errMetric(t, 2, "y", 2))
	s2 := nextSecond(t, r, 2*time.Second)
	if s2.Index != 2 || sumErrors(s2) != 7 || len(s2.Errors) != 3 {
		t.Fatalf("second 2 errors = %+v", s2.Errors)
	}
	if e := s2.Errors[0]; e.Step != "x" || e.Status != 503 || e.Message != "status 503" || e.First.UnixNano() != 1 || e.Last.UnixNano() != 2 {
		t.Fatalf("converted = %+v", e)
	}

	// A late report right before the end arrives in a final errors-only second.
	r.report("a", errMetric(t, 3, "x", 1))
	s3 := nextSecond(t, r, 2*time.Second)
	r.report("b", errMetric(t, 3, "x", 5))
	r.finished("a", &pb.RunFinished{RunId: "r"})
	r.finished("b", &pb.RunFinished{RunId: "r"})
	var rest []Second
	for s := range r.Seconds() {
		rest = append(rest, s)
	}
	if sumErrors(s3) != 1 || len(rest) != 1 || rest[0].Snapshot != nil || sumErrors(rest...) != 5 {
		t.Fatalf("s3 %+v rest %+v", s3.Errors, rest)
	}
	<-r.Done()
}
