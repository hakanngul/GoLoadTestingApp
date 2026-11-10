package cluster

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster/pb"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// ErrNotEnoughWorkers is returned by Master.Start when fewer idle workers
// are connected than requested.
var ErrNotEnoughWorkers = errors.New("cluster: not enough idle workers")

// Worker states reported in model.Worker.State.
const (
	StateIdle = "idle"
	StateBusy = "busy"
	StateLost = "lost"
)

const (
	// DefaultHeartbeatTimeout is MasterOptions.HeartbeatTimeout's default.
	DefaultHeartbeatTimeout = 5 * time.Second
	// DefaultStartDelay is how far in the future a run starts when
	// RunSpec.StartAt is zero, so every worker can prepare in time.
	DefaultStartDelay = 2 * time.Second

	// lostRetention is how long a lost worker stays listed by Workers.
	lostRetention = 5 * time.Minute
	// stragglerGrace is how long a second waits for missing workers after
	// its first report before it is emitted without them.
	stragglerGrace = 2 * time.Second
	// sendQueueSize buffers master->worker messages per connection.
	sendQueueSize = 64
)

var (
	errHeartbeatTimeout = errors.New("cluster: worker heartbeat timeout")
	errReplaced         = errors.New("cluster: worker reconnected with the same id")
	errWorkerClosed     = errors.New("cluster: worker closed the stream")
)

// MasterOptions configures a Master.
type MasterOptions struct {
	// Token, when set, is required from workers as the gRPC metadata
	// "authorization: Bearer <token>" (see TokenCredentials).
	Token string
	// HeartbeatTimeout marks a worker lost when nothing was received from
	// it for this long. Default DefaultHeartbeatTimeout.
	HeartbeatTimeout time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now is the clock used for timestamps shown to users (LastSeen).
	// Default time.Now. Liveness and straggler timing always use the real
	// clock.
	Now func() time.Time
}

// Master is the coordinator side of the cluster. It implements Dispatcher
// for the runner and pb.CoordinatorServer for workers; attach it to a gRPC
// server with Register.
type Master struct {
	pb.UnimplementedCoordinatorServer

	token     string
	hbTimeout time.Duration
	log       *slog.Logger
	now       func() time.Time
	grace     time.Duration // straggler grace; tests shorten it

	mu      sync.Mutex
	workers map[string]*workerConn
}

var (
	_ Dispatcher                    = (*Master)(nil)
	_ pb.CoordinatorServer          = (*Master)(nil)
	_ credentials.PerRPCCredentials = tokenCredentials{}
)

// NewMaster returns a Master with no connected workers.
func NewMaster(opts MasterOptions) *Master {
	m := &Master{
		token:     opts.Token,
		hbTimeout: opts.HeartbeatTimeout,
		log:       opts.Logger,
		now:       opts.Now,
		grace:     stragglerGrace,
		workers:   make(map[string]*workerConn),
	}
	if m.hbTimeout <= 0 {
		m.hbTimeout = DefaultHeartbeatTimeout
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m
}

// Register attaches the Coordinator service to s.
func (m *Master) Register(s *grpc.Server) { pb.RegisterCoordinatorServer(s, m) }

// workerConn is one connected worker stream.
type workerConn struct {
	id       string
	addr     string
	version  string
	send     chan *pb.MasterMessage // drained by the Connect handler
	gone     chan struct{}          // closed once the connection is over
	cancel   context.CancelCauseFunc
	lastRecv atomic.Int64 // UnixNano of the last message (real clock)

	// Guarded by Master.mu.
	state    string
	runID    string
	run      *remoteRun
	cpu      float64
	active   int64
	lastSeen time.Time
	lostAt   time.Time
}

// enqueue queues msg for the worker. It reports false if the connection
// ended or ctx is done first.
func (wc *workerConn) enqueue(ctx context.Context, msg *pb.MasterMessage) bool {
	select {
	case wc.send <- msg:
		return true
	case <-wc.gone:
		return false
	case <-ctx.Done():
		return false
	}
}

// Connect implements pb.CoordinatorServer. The first message must be a
// Hello. Master->worker messages are sent from this goroutine only;
// worker messages are read by a separate goroutine, and a watchdog fails
// the stream when the worker goes silent for HeartbeatTimeout.
func (m *Master) Connect(stream pb.Coordinator_ConnectServer) error {
	if err := m.authorize(stream.Context()); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.GetWorkerId() == "" {
		return status.Error(codes.InvalidArgument, "cluster: first message must be a Hello with a worker_id")
	}

	ctx, cancel := context.WithCancelCause(stream.Context())
	defer cancel(nil)
	wc := m.register(hello, stream.Context(), cancel)
	m.log.Info("cluster: worker connected", "worker", wc.id, "addr", wc.addr, "version", wc.version)

	go m.readLoop(ctx, cancel, wc, stream)
	go m.watchdog(ctx, cancel, wc)

loop:
	for {
		select {
		case msg := <-wc.send:
			if err := stream.Send(msg); err != nil {
				cancel(err)
				break loop
			}
		case <-ctx.Done():
			break loop
		}
	}

	cause := context.Cause(ctx)
	m.markLost(wc, cause)
	m.log.Warn("cluster: worker disconnected", "worker", wc.id, "cause", cause)
	switch {
	case errors.Is(cause, errHeartbeatTimeout):
		return status.Error(codes.DeadlineExceeded, cause.Error())
	case errors.Is(cause, errReplaced):
		return status.Error(codes.Aborted, cause.Error())
	case errors.Is(cause, errWorkerClosed):
		return nil
	}
	if s, ok := status.FromError(cause); ok && s.Code() != codes.OK && s.Code() != codes.Unknown {
		return s.Err()
	}
	return status.Error(codes.Canceled, "cluster: stream ended")
}

// authorize checks the bearer token, if one is configured.
func (m *Master) authorize(ctx context.Context) error {
	if m.token == "" {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	want := []byte("Bearer " + m.token)
	for _, got := range md.Get("authorization") {
		if subtle.ConstantTimeCompare([]byte(got), want) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "cluster: invalid or missing worker token")
}

// register adds a new connection, replacing (and disconnecting) any live
// connection that uses the same worker id.
func (m *Master) register(h *pb.Hello, ctx context.Context, cancel context.CancelCauseFunc) *workerConn {
	addr := h.GetAddr()
	if addr == "" {
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			addr = p.Addr.String()
		}
	}
	wc := &workerConn{
		id:      h.GetWorkerId(),
		addr:    addr,
		version: h.GetVersion(),
		send:    make(chan *pb.MasterMessage, sendQueueSize),
		gone:    make(chan struct{}),
		cancel:  cancel,
		state:   StateIdle,
	}
	wc.lastRecv.Store(time.Now().UnixNano())

	m.mu.Lock()
	defer m.mu.Unlock()
	wc.lastSeen = m.now()
	if old, ok := m.workers[h.GetWorkerId()]; ok && old.state != StateLost {
		old.cancel(errReplaced)
	}
	m.workers[wc.id] = wc
	return wc
}

// readLoop receives worker messages until the stream fails.
func (m *Master) readLoop(ctx context.Context, cancel context.CancelCauseFunc, wc *workerConn, stream pb.Coordinator_ConnectServer) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errWorkerClosed
			}
			cancel(err)
			return
		}
		if ctx.Err() != nil {
			return
		}
		wc.lastRecv.Store(time.Now().UnixNano())
		m.handle(wc, msg)
	}
}

// watchdog fails the stream once the worker has been silent too long.
func (m *Master) watchdog(ctx context.Context, cancel context.CancelCauseFunc, wc *workerConn) {
	t := time.NewTicker(max(m.hbTimeout/5, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if now.Sub(time.Unix(0, wc.lastRecv.Load())) > m.hbTimeout {
				cancel(errHeartbeatTimeout)
				return
			}
		}
	}
}

// handle applies one worker message. It never blocks on run consumers.
func (m *Master) handle(wc *workerConn, msg *pb.WorkerMessage) {
	m.mu.Lock()
	if wc.state == StateLost {
		m.mu.Unlock()
		return
	}
	wc.lastSeen = m.now()
	run := wc.run
	var finished *pb.RunFinished
	switch x := msg.GetMsg().(type) {
	case *pb.WorkerMessage_Heartbeat:
		wc.cpu = x.Heartbeat.GetCpu()
		wc.active = x.Heartbeat.GetActive()
	case *pb.WorkerMessage_Metric:
		wc.cpu = x.Metric.GetCpu()
		wc.active = x.Metric.GetActive()
	case *pb.WorkerMessage_Finished:
		if run != nil && x.Finished.GetRunId() == run.id {
			finished = x.Finished
			wc.run, wc.runID, wc.state, wc.active = nil, "", StateIdle, 0
		}
	}
	m.mu.Unlock()

	if run == nil {
		return
	}
	switch x := msg.GetMsg().(type) {
	case *pb.WorkerMessage_Heartbeat:
		if x.Heartbeat.GetRunId() == run.id {
			run.ack(wc.id)
		}
	case *pb.WorkerMessage_Metric:
		if x.Metric.GetRunId() == run.id {
			run.report(wc.id, x.Metric)
		}
	case *pb.WorkerMessage_Errors:
		if x.Errors.GetRunId() == run.id {
			run.errorSamples(x.Errors.GetSamples())
		}
	}
	if finished != nil {
		run.finished(wc.id, finished)
	}
}

// markLost records that wc's connection ended and tells its run, if any.
func (m *Master) markLost(wc *workerConn, cause error) {
	m.mu.Lock()
	if wc.state == StateLost {
		m.mu.Unlock()
		return
	}
	run := wc.run
	wc.state, wc.run, wc.runID, wc.active = StateLost, nil, "", 0
	wc.lostAt = m.now()
	close(wc.gone)
	m.mu.Unlock()
	if run != nil {
		run.workerLost(wc.id, cause)
	}
}

// Workers implements Dispatcher: connected workers plus those lost within
// the last five minutes, sorted by id.
func (m *Master) Workers() []model.Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]model.Worker, 0, len(m.workers))
	for id, wc := range m.workers {
		if wc.state == StateLost && now.Sub(wc.lostAt) > lostRetention {
			delete(m.workers, id)
			continue
		}
		out = append(out, model.Worker{
			ID:       wc.id,
			Addr:     wc.addr,
			State:    wc.state,
			RunID:    wc.runID,
			CPU:      wc.cpu,
			LastSeen: wc.lastSeen,
			Version:  wc.version,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Idle implements Dispatcher.
func (m *Master) Idle() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, wc := range m.workers {
		if wc.state == StateIdle {
			n++
		}
	}
	return n
}

// Start implements Dispatcher. It assigns the n idle workers with the
// lowest ids, sends each a StartRun with its index, and waits until every
// one of them acknowledged the run (a heartbeat or any message carrying
// the run id), finished or was lost. If ctx ends first the run is stopped
// and ctx's error returned. A zero spec.StartAt means now + DefaultStartDelay.
func (m *Master) Start(ctx context.Context, spec RunSpec, n int) (RemoteRun, error) {
	if n < 1 {
		return nil, fmt.Errorf("cluster: worker count must be at least 1, got %d", n)
	}
	if spec.RunID == "" {
		return nil, errors.New("cluster: run id is required")
	}

	m.mu.Lock()
	idle := make([]*workerConn, 0, len(m.workers))
	for _, wc := range m.workers {
		if wc.state == StateIdle {
			idle = append(idle, wc)
		}
	}
	if len(idle) < n {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %d requested, %d idle", ErrNotEnoughWorkers, n, len(idle))
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].id < idle[j].id })
	picked := idle[:n]
	run := newRemoteRun(spec.RunID, picked, m.grace, m.log)
	for _, wc := range picked {
		wc.state, wc.run, wc.runID = StateBusy, run, spec.RunID
	}
	m.mu.Unlock()

	startAt := spec.StartAt
	if startAt.IsZero() {
		startAt = m.now().Add(DefaultStartDelay)
	}
	for i, wc := range picked {
		msg := &pb.MasterMessage{Msg: &pb.MasterMessage_Start{Start: &pb.StartRun{
			RunId:           spec.RunID,
			ScenarioYaml:    spec.YAML,
			Files:           spec.Files,
			WorkerIndex:     int32(i),
			WorkerCount:     int32(n),
			StartAtUnixNano: startAt.UnixNano(),
			MaxErrorSamples: int32(spec.MaxErrorSamples),
		}}}
		run.setSent(wc.id, true) // before queueing: the reply may be immediate
		if wc.enqueue(ctx, msg) {
			continue
		}
		if ctx.Err() != nil {
			run.setSent(wc.id, false)
			m.abort(run, picked)
			return nil, ctx.Err()
		}
		// The connection ended: markLost has told (or will tell) the run.
	}

	select {
	case <-run.acked:
		return run, nil
	case <-ctx.Done():
		m.abort(run, picked)
		return nil, ctx.Err()
	}
}

// abort abandons a run whose Start did not complete: workers that got the
// StartRun are told to stop (they become idle when they report
// RunFinished); the others are released immediately.
func (m *Master) abort(run *remoteRun, picked []*workerConn) {
	run.abandon()
	run.Stop()
	var unsent []string
	m.mu.Lock()
	for _, wc := range picked {
		if wc.run == run && !run.wasSent(wc.id) {
			wc.state, wc.run, wc.runID = StateIdle, nil, ""
			unsent = append(unsent, wc.id)
		}
	}
	m.mu.Unlock()
	run.release(unsent)
}

// TokenCredentials returns per-RPC credentials that send token as
// "authorization: Bearer <token>", for workers dialing a master started
// with MasterOptions.Token. With insecure false the token is only sent
// over TLS connections.
func TokenCredentials(token string, insecure bool) credentials.PerRPCCredentials {
	return tokenCredentials{token: token, insecure: insecure}
}

type tokenCredentials struct {
	token    string
	insecure bool
}

func (c tokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + c.token}, nil
}

func (c tokenCredentials) RequireTransportSecurity() bool { return !c.insecure }
