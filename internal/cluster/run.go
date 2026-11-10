package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster/pb"
	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

const (
	// errorBatchBuffer is the capacity of RemoteRun.Errors. Batches that do
	// not fit are dropped (and counted) rather than blocking the stream.
	errorBatchBuffer = 256
	// maxQueuedSeconds bounds the internal queue between the aggregator
	// and the Seconds channel (one hour of seconds). Beyond it the oldest
	// queued second is dropped and counted.
	maxQueuedSeconds = 3600
)

// WarningReporter is implemented by the RemoteRun returned by Master.Start.
// Warnings lists non-fatal problems seen during the run (late or dropped
// reports); it is complete once Done is closed.
type WarningReporter interface {
	Warnings() []string
}

// member is one worker assigned to a run.
type member struct {
	wc       *workerConn
	sent     bool // StartRun was queued
	acked    bool
	finished bool
	lost     bool
}

// live reports whether more reports may still come from the member.
func (mb *member) live() bool { return !mb.finished && !mb.lost }

// pendingSecond accumulates the reports of one second index.
type pendingSecond struct {
	snap      *metrics.Snapshot
	target    float64
	active    int64
	cpu       float64
	reporters map[string]bool
	first     time.Time // real clock time of the first report
	errs      []model.ErrorGroup
}

// remoteRun implements RemoteRun.
//
// Data flow: the master's per-worker read goroutines call report /
// errorSamples / finished / workerLost, which only take r.mu and never
// block. Completed seconds are appended to an internal queue that a pump
// goroutine forwards to the Seconds channel, so a slow Seconds consumer
// only grows the queue (bounded by maxQueuedSeconds) and never stalls a
// worker stream. A ticker goroutine emits seconds whose straggler grace
// has expired.
type remoteRun struct {
	id    string
	log   *slog.Logger
	grace time.Duration

	seconds   chan Second
	errs      chan []model.ErrorSample
	done      chan struct{}
	acked     chan struct{} // closed once every member acked, finished or was lost
	abandoned chan struct{} // closed when Start gave up: drop all output
	stopOnce  sync.Once
	abandonMu sync.Once

	mu         sync.Mutex
	wake       *sync.Cond // signals the pump
	members    map[string]*member
	order      []string // member ids in index order
	pending    map[int64]*pendingSecond
	emitted    int64 // highest emitted second index
	queue      []Second
	carryErrs  []model.ErrorGroup // error counts of late reports, for the next Second
	ackClosed  bool
	finishing  bool // every member finished or was lost; no more input
	late       int64
	dupes      int64
	badSnaps   int64
	droppedQ   int64
	droppedErr int64
	result     Result
}

var (
	_ RemoteRun       = (*remoteRun)(nil)
	_ WarningReporter = (*remoteRun)(nil)
)

func newRemoteRun(id string, workers []*workerConn, grace time.Duration, log *slog.Logger) *remoteRun {
	r := &remoteRun{
		id:        id,
		log:       log,
		grace:     grace,
		seconds:   make(chan Second),
		errs:      make(chan []model.ErrorSample, errorBatchBuffer),
		done:      make(chan struct{}),
		acked:     make(chan struct{}),
		abandoned: make(chan struct{}),
		members:   make(map[string]*member, len(workers)),
		pending:   make(map[int64]*pendingSecond),
	}
	r.wake = sync.NewCond(&r.mu)
	for _, wc := range workers {
		r.members[wc.id] = &member{wc: wc}
		r.order = append(r.order, wc.id)
	}
	go r.pump()
	go r.ticker()
	return r
}

// Seconds implements RemoteRun. Callers must drain it: Done is closed only
// after Seconds has been closed.
func (r *remoteRun) Seconds() <-chan Second { return r.seconds }

// Errors implements RemoteRun.
func (r *remoteRun) Errors() <-chan []model.ErrorSample { return r.errs }

// Done implements RemoteRun.
func (r *remoteRun) Done() <-chan struct{} { return r.done }

// Result implements RemoteRun.
func (r *remoteRun) Result() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := r.result
	res.LostWorkers = append([]string(nil), r.result.LostWorkers...)
	return res
}

// Warnings implements WarningReporter.
func (r *remoteRun) Warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var w []string
	if r.late > 0 {
		w = append(w, fmt.Sprintf("%d worker metric report(s) arrived after their second was already emitted and were dropped", r.late))
	}
	if r.dupes > 0 {
		w = append(w, fmt.Sprintf("%d duplicate worker metric report(s) were ignored", r.dupes))
	}
	if r.badSnaps > 0 {
		w = append(w, fmt.Sprintf("%d worker metric report(s) had an undecodable snapshot", r.badSnaps))
	}
	if r.droppedQ > 0 {
		w = append(w, fmt.Sprintf("%d merged second(s) were dropped because the consumer fell too far behind", r.droppedQ))
	}
	if r.droppedErr > 0 {
		w = append(w, fmt.Sprintf("%d error sample batch(es) were dropped because the consumer fell behind", r.droppedErr))
	}
	return w
}

// Stop implements RemoteRun: it sends StopRun to every assigned worker
// that is still running. It does not block.
func (r *remoteRun) Stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		var targets []*workerConn
		for _, id := range r.order {
			if mb := r.members[id]; mb.sent && mb.live() {
				targets = append(targets, mb.wc)
			}
		}
		r.mu.Unlock()
		msg := &pb.MasterMessage{Msg: &pb.MasterMessage_Stop{Stop: &pb.StopRun{RunId: r.id}}}
		for _, wc := range targets {
			go wc.enqueue(context.Background(), msg)
		}
	})
}

// abandon makes the run discard all output (Start failed; nobody reads).
func (r *remoteRun) abandon() {
	r.abandonMu.Do(func() { close(r.abandoned) })
}

func (r *remoteRun) setSent(id string, sent bool) {
	r.mu.Lock()
	if mb := r.members[id]; mb != nil {
		mb.sent = sent
	}
	r.mu.Unlock()
}

// release ends the participation of members that never got the StartRun,
// so an abandoned run can still finish and free its goroutines.
func (r *remoteRun) release(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if mb := r.members[id]; mb != nil && mb.live() {
			mb.finished = true
			r.memberEndedLocked(id)
		}
	}
}

func (r *remoteRun) wasSent(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	mb := r.members[id]
	return mb != nil && mb.sent
}

// ack records that worker id accepted the run.
func (r *remoteRun) ack(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ackLocked(id)
}

func (r *remoteRun) ackLocked(id string) {
	if mb := r.members[id]; mb != nil {
		mb.acked = true
	}
	if r.ackClosed {
		return
	}
	for _, mb := range r.members {
		if !mb.acked && mb.live() {
			return
		}
	}
	r.ackClosed = true
	close(r.acked)
}

// report merges one worker's MetricReport into its second.
func (r *remoteRun) report(id string, mr *pb.MetricReport) {
	var snap *metrics.Snapshot
	if b := mr.GetSnapshot(); len(b) > 0 {
		snap = new(metrics.Snapshot)
		if err := snap.UnmarshalBinary(b); err != nil {
			r.log.Error("cluster: decode worker snapshot", "run", r.id, "worker", id, "second", mr.GetSecond(), "err", err)
			snap = nil
			r.mu.Lock()
			r.badSnaps++
			r.mu.Unlock()
		}
	}

	errs := errorCounts(mr.GetErrorCounts())

	r.mu.Lock()
	defer r.mu.Unlock()
	mb := r.members[id]
	if mb == nil || r.finishing {
		return
	}
	r.ackLocked(id)
	k := mr.GetSecond()
	if k <= r.emitted {
		r.late++
		// The snapshot is dropped, but error counts are kept exact.
		r.carryErrs = append(r.carryErrs, errs...)
		return
	}
	ps := r.pending[k]
	if ps == nil {
		ps = &pendingSecond{
			snap:      &metrics.Snapshot{Total: metrics.NewStats(), ByStep: map[string]*metrics.Stats{}},
			reporters: make(map[string]bool, len(r.members)),
			first:     time.Now(),
		}
		r.pending[k] = ps
	}
	if ps.reporters[id] {
		r.dupes++
		return
	}
	ps.reporters[id] = true
	ps.errs = append(ps.errs, errs...)
	ps.snap.Merge(snap)
	ps.target += mr.GetTarget()
	ps.active += mr.GetActive()
	ps.cpu = max(ps.cpu, mr.GetCpu())
	r.emitReadyLocked()
}

// errorSamples forwards a batch without blocking (dropped if the buffer
// is full).
func (r *remoteRun) errorSamples(in []*pb.ErrorSample) {
	if len(in) == 0 {
		return
	}
	batch := make([]model.ErrorSample, len(in))
	for i, s := range in {
		batch[i] = model.ErrorSample{
			T:       time.Unix(0, s.GetTUnixNano()),
			Step:    s.GetStep(),
			Status:  int(s.GetStatus()),
			Message: s.GetMessage(),
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finishing { // errs may already be closed
		return
	}
	select {
	case r.errs <- batch:
	default:
		r.droppedErr++
	}
}

// finished records a worker's RunFinished.
func (r *remoteRun) finished(id string, f *pb.RunFinished) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mb := r.members[id]
	if mb == nil || !mb.live() {
		return
	}
	mb.finished = true
	if msg := f.GetError(); msg != "" && r.result.Err == nil {
		r.result.Err = fmt.Errorf("worker %s: %s", id, msg)
	}
	if f.GetDataExhausted() {
		r.result.DataExhausted = true
	}
	r.memberEndedLocked(id)
}

// workerLost records that a worker's connection ended mid-run.
func (r *remoteRun) workerLost(id string, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mb := r.members[id]
	if mb == nil || !mb.live() {
		return
	}
	mb.lost = true
	r.result.LostWorkers = append(r.result.LostWorkers, id)
	r.log.Warn("cluster: worker lost mid-run", "run", r.id, "worker", id, "cause", cause)
	r.memberEndedLocked(id)
}

// memberEndedLocked re-evaluates the run after a member stopped reporting.
func (r *remoteRun) memberEndedLocked(id string) {
	r.ackLocked(id)
	for _, mb := range r.members {
		if mb.live() {
			// Seconds that only waited for this member may be complete now.
			r.emitReadyLocked()
			return
		}
	}
	r.finishing = true
	r.emitReadyLocked() // flushes everything still pending
	if len(r.carryErrs) > 0 {
		if n := len(r.queue); n > 0 {
			r.queue[n-1].Errors = append(r.queue[n-1].Errors, r.carryErrs...)
		} else {
			r.queue = append(r.queue, Second{Index: r.emitted, Errors: r.carryErrs})
		}
		r.carryErrs = nil
	}
	r.wake.Broadcast()
}

// emitReadyLocked emits pending seconds in increasing order while the
// lowest one is complete (every live member reported it), its straggler
// grace expired, or the run is finishing.
func (r *remoteRun) emitReadyLocked() {
	if len(r.pending) == 0 {
		return
	}
	keys := make([]int64, 0, len(r.pending))
	for k := range r.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	now := time.Now()
	for _, k := range keys {
		ps := r.pending[k]
		if !r.finishing && !r.completeLocked(ps) && now.Sub(ps.first) < r.grace {
			return
		}
		delete(r.pending, k)
		r.emitted = k
		if len(r.queue) >= maxQueuedSeconds {
			r.queue[0] = Second{}
			r.queue = r.queue[1:]
			r.droppedQ++
		}
		errs := ps.errs
		if len(r.carryErrs) > 0 {
			errs = append(errs, r.carryErrs...)
			r.carryErrs = nil
		}
		r.queue = append(r.queue, Second{
			Index:    k,
			Snapshot: ps.snap,
			Target:   ps.target,
			Active:   ps.active,
			CPU:      ps.cpu,
			Workers:  len(ps.reporters),
			Errors:   errs,
		})
		r.wake.Signal()
	}
}

func (r *remoteRun) completeLocked(ps *pendingSecond) bool {
	for id, mb := range r.members {
		if mb.live() && !ps.reporters[id] {
			return false
		}
	}
	return true
}

// ticker emits seconds whose straggler grace expired.
func (r *remoteRun) ticker() {
	t := time.NewTicker(max(r.grace/8, 5*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
			r.mu.Lock()
			if !r.finishing {
				r.emitReadyLocked()
			}
			r.mu.Unlock()
		}
	}
}

// pump forwards queued seconds to the Seconds channel. When the run is
// finishing and the queue is empty it closes Seconds and Errors, then Done.
func (r *remoteRun) pump() {
	for {
		r.mu.Lock()
		for len(r.queue) == 0 && !r.finishing {
			r.wake.Wait()
		}
		if len(r.queue) == 0 {
			// finishing: no sender can touch errs any more (they check
			// finishing under r.mu).
			r.mu.Unlock()
			close(r.seconds)
			close(r.errs)
			close(r.done)
			return
		}
		s := r.queue[0]
		r.queue[0] = Second{}
		r.queue = r.queue[1:]
		r.mu.Unlock()

		select {
		case r.seconds <- s:
		case <-r.abandoned:
		}
	}
}

// errorCounts converts a worker's per-second error groups.
func errorCounts(in []*pb.ErrorCount) []model.ErrorGroup {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.ErrorGroup, 0, len(in))
	for _, e := range in {
		if e.GetCount() <= 0 {
			continue
		}
		g := model.ErrorGroup{
			Step:    e.GetStep(),
			Status:  int(e.GetStatus()),
			Message: e.GetMessage(),
			Sample:  e.GetSample(),
			Count:   e.GetCount(),
		}
		if ns := e.GetFirstUnixNano(); ns != 0 {
			g.First = time.Unix(0, ns).UTC()
		}
		if ns := e.GetLastUnixNano(); ns != 0 {
			g.Last = time.Unix(0, ns).UTC()
		}
		out = append(out, g)
	}
	return out
}
