package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/hakanngul/GoLoadTestingApp/internal/cluster/pb"
	"github.com/hakanngul/GoLoadTestingApp/internal/data"
	"github.com/hakanngul/GoLoadTestingApp/internal/engine"
	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/executor"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

const (
	defaultHeartbeat     = time.Second
	defaultMinBackoff    = time.Second
	defaultMaxBackoff    = 30 * time.Second
	defaultMaxErrSamples = 100
	// stableSession: a session that lasted this long resets the backoff.
	stableSession = 10 * time.Second
	// workerSendQueue buffers worker->master messages of one session.
	workerSendQueue = 256
	// maxSampleMessage caps the length of an error sample message.
	maxSampleMessage = 1024
	// MaxStartRunSize is the largest master message a worker accepts. A
	// StartRun carries the data files, so gRPC's 4MB default is too small.
	MaxStartRunSize = 256 << 20
)

// WorkerOptions configures RunWorker.
type WorkerOptions struct {
	// MasterAddr is the master's gRPC address (host:port). Required.
	MasterAddr string
	// Token is sent as "authorization: Bearer <token>" when set.
	Token string
	// Insecure dials without TLS (plaintext). Otherwise TLS with the
	// system roots is used.
	Insecure bool
	// ID identifies the worker; default hostname plus a random suffix.
	ID string
	// Version is reported to the master; default "dev".
	Version string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Allow vets redirect hops that leave the host of the request being
	// redirected (a target allowlist, `worker -allow-targets`). The
	// master's allowlist is not sent to workers, so with Allow nil a
	// worker follows redirects only to the scenario's own target hosts,
	// which the master vetted before starting the run.
	Allow func(rawURL string) error
	// DialOptions are appended after the defaults (tests: bufconn dialer,
	// or custom transport credentials, which override the defaults).
	DialOptions []grpc.DialOption

	// Test knobs (zero means default).
	heartbeat  time.Duration
	minBackoff time.Duration
	maxBackoff time.Duration
}

// RunWorker connects to the master and executes the runs it assigns until
// ctx is done, which returns nil. Broken streams are re-established with
// exponential backoff (1s..30s); a run in progress when the stream breaks
// is aborted, since the master has already counted the worker as lost.
// It returns an error only for permanent failures: invalid options or a
// master rejecting the worker (bad token, invalid hello).
func RunWorker(ctx context.Context, opts WorkerOptions) error {
	if opts.MasterAddr == "" {
		return errors.New("cluster: master address is required")
	}
	w := &worker{opts: opts, log: opts.Logger}
	if w.log == nil {
		w.log = slog.Default()
	}
	if w.opts.ID == "" {
		w.opts.ID = defaultWorkerID()
	}
	if w.opts.Version == "" {
		w.opts.Version = "dev"
	}
	if w.opts.heartbeat <= 0 {
		w.opts.heartbeat = defaultHeartbeat
	}
	if w.opts.minBackoff <= 0 {
		w.opts.minBackoff = defaultMinBackoff
	}
	if w.opts.maxBackoff <= 0 {
		w.opts.maxBackoff = defaultMaxBackoff
	}
	w.log = w.log.With("worker", w.opts.ID)

	dial := []grpc.DialOption{grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxStartRunSize))}
	if opts.Insecure {
		dial = append(dial, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		dial = append(dial, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}
	if opts.Token != "" {
		dial = append(dial, grpc.WithPerRPCCredentials(TokenCredentials(opts.Token, opts.Insecure)))
	}
	dial = append(dial, opts.DialOptions...)
	conn, err := grpc.NewClient(opts.MasterAddr, dial...)
	if err != nil {
		return fmt.Errorf("cluster: dial master: %w", err)
	}
	defer conn.Close()
	client := pb.NewCoordinatorClient(conn)

	backoff := w.opts.minBackoff
	for {
		began := time.Now()
		err := w.session(ctx, client)
		if ctx.Err() != nil {
			return nil
		}
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument:
			return fmt.Errorf("cluster: master rejected worker: %w", err)
		}
		if time.Since(began) >= stableSession {
			backoff = w.opts.minBackoff
		}
		w.log.Warn("cluster: connection to master lost; reconnecting", "err", err, "in", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		backoff = min(backoff*2, w.opts.maxBackoff)
	}
}

func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	var b [3]byte
	_, _ = rand.Read(b[:])
	return host + "-" + hex.EncodeToString(b[:])
}

type worker struct {
	opts WorkerOptions
	log  *slog.Logger
}

// session is one connected stream to the master.
type session struct {
	w   *worker
	ctx context.Context // the session's lifetime
	out chan *pb.WorkerMessage

	mu  sync.Mutex
	run *workerRun // nil when idle
	wg  sync.WaitGroup
}

// workerRun is the run currently executing on this worker.
type workerRun struct {
	id     string
	cancel context.CancelFunc
	eng    atomic.Pointer[engine.Engine]
}

// send queues msg for the sender goroutine. It reports false once the
// session ended. All stream sends go through this single queue.
func (s *session) send(msg *pb.WorkerMessage) bool {
	select {
	case s.out <- msg:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// session runs one stream until it fails or ctx is done.
func (w *worker) session(ctx context.Context, client pb.CoordinatorClient) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Connect(sctx)
	if err != nil {
		return err
	}
	hello := &pb.WorkerMessage{Msg: &pb.WorkerMessage_Hello{Hello: &pb.Hello{
		WorkerId: w.opts.ID,
		Version:  w.opts.Version,
		Cpus:     int32(runtime.NumCPU()),
	}}}
	if err := stream.Send(hello); err != nil {
		if errors.Is(err, io.EOF) { // the real status comes from Recv
			_, err = stream.Recv()
		}
		return err
	}

	s := &session{w: w, ctx: sctx, out: make(chan *pb.WorkerMessage, workerSendQueue)}
	var bg sync.WaitGroup
	bg.Add(2)
	go func() { // sole sender on the stream
		defer bg.Done()
		for {
			select {
			case msg := <-s.out:
				if err := stream.Send(msg); err != nil {
					cancel()
					return
				}
			case <-sctx.Done():
				return
			}
		}
	}()
	go func() {
		defer bg.Done()
		s.heartbeats()
	}()

	var recvErr error
	for {
		msg, err := stream.Recv()
		if err != nil {
			recvErr = err
			break
		}
		switch x := msg.GetMsg().(type) {
		case *pb.MasterMessage_Start:
			s.start(x.Start)
		case *pb.MasterMessage_Stop:
			s.stop(x.Stop.GetRunId())
		}
	}
	cancel() // aborts any active run
	s.wg.Wait()
	bg.Wait()
	if errors.Is(recvErr, io.EOF) {
		recvErr = errors.New("cluster: master closed the stream")
	}
	return recvErr
}

// heartbeats sends a Heartbeat every interval until the session ends.
func (s *session) heartbeats() {
	cpu := newCPUMeter()
	t := time.NewTicker(s.w.opts.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			if !s.send(s.heartbeat(cpu.sample())) {
				return
			}
		}
	}
}

func (s *session) heartbeat(cpu float64) *pb.WorkerMessage {
	hb := &pb.Heartbeat{Cpu: cpu}
	s.mu.Lock()
	if r := s.run; r != nil {
		hb.RunId = r.id
		if eng := r.eng.Load(); eng != nil {
			_, hb.Active = eng.Current()
		}
	}
	s.mu.Unlock()
	return &pb.WorkerMessage{Msg: &pb.WorkerMessage_Heartbeat{Heartbeat: hb}}
}

// start begins a run, or rejects it when one is already active.
func (s *session) start(msg *pb.StartRun) {
	s.mu.Lock()
	if s.run != nil {
		s.mu.Unlock()
		s.w.log.Warn("cluster: rejecting run while busy", "run", msg.GetRunId())
		s.send(finishedMsg(&pb.RunFinished{RunId: msg.GetRunId(), Error: "worker busy"}))
		return
	}
	rctx, rcancel := context.WithCancel(s.ctx)
	r := &workerRun{id: msg.GetRunId(), cancel: rcancel}
	s.run = r
	s.wg.Add(1)
	s.mu.Unlock()

	s.send(s.heartbeat(0)) // acknowledge the run right away
	go func() {
		defer s.wg.Done()
		s.w.log.Info("cluster: run started", "run", r.id, "index", msg.GetWorkerIndex(), "count", msg.GetWorkerCount())
		fin := s.execute(rctx, r, msg)
		rcancel()
		s.mu.Lock()
		s.run = nil // before RunFinished, so a following StartRun is accepted
		s.mu.Unlock()
		s.send(finishedMsg(fin))
		s.w.log.Info("cluster: run finished", "run", r.id, "error", fin.GetError(), "data_exhausted", fin.GetDataExhausted())
	}()
}

// stop cancels the active run if it is runID.
func (s *session) stop(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil && s.run.id == runID {
		s.run.cancel()
	}
}

func finishedMsg(f *pb.RunFinished) *pb.WorkerMessage {
	return &pb.WorkerMessage{Msg: &pb.WorkerMessage_Finished{Finished: f}}
}

// execute runs this worker's share of the scenario and returns its
// outcome. ctx is cancelled by StopRun or the end of the session.
func (s *session) execute(ctx context.Context, r *workerRun, msg *pb.StartRun) *pb.RunFinished {
	fin := &pb.RunFinished{RunId: r.id}
	fail := func(err error) *pb.RunFinished {
		fin.Error = err.Error()
		return fin
	}

	sc, err := scenario.Parse([]byte(msg.GetScenarioYaml()))
	if err != nil {
		return fail(fmt.Errorf("scenario: %w", err))
	}
	index, count := int(msg.GetWorkerIndex()), int(msg.GetWorkerCount())
	if count < 1 {
		index, count = 0, 1
	}
	sc.Load = ShareLoad(sc.Load, index, count)
	feeder, err := workerFeeder(sc, msg.GetFiles(), index, count)
	if err != nil {
		return fail(err)
	}
	exec, closeExec, err := executor.New(sc, executor.Options{HTTP: httpx.Options{
		CheckRedirect:     s.w.opts.Allow,
		RestrictRedirects: s.w.opts.Allow == nil,
	}})
	if err != nil {
		return fail(err)
	}
	defer closeExec()

	startAt := time.Unix(0, msg.GetStartAtUnixNano())
	if msg.GetStartAtUnixNano() == 0 {
		startAt = time.Now()
	}
	if d := time.Until(startAt); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return fin // stopped before the start: not an error
		case <-t.C:
		}
	}

	maxSamples := int64(msg.GetMaxErrorSamples())
	if maxSamples <= 0 {
		maxSamples = defaultMaxErrSamples
	}
	col := metrics.NewCollector(startAt)
	samples := &errorSampler{max: maxSamples}
	groups := metrics.NewErrorGrouper(model.MaxErrorGroups, nil)
	eng := engine.New(sc.Load, engine.Options{
		Executor: exec,
		Feeder:   feeder,
		Sink: func(res protocol.Result) {
			col.Record(res)
			if res.Err != nil {
				samples.offer(res)
				groups.Record(res.Step, res.Status, res.Err)
			}
		},
		OnDropped: col.RecordDropped,
	})
	r.eng.Store(eng)

	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()

	cpu := newCPUMeter()
	boundary := func(k int64) time.Time { return startAt.Add(time.Duration(k) * time.Second) }
	k := int64(1) // the second currently being collected
	report := func(k int64, end time.Time) {
		s.report(r.id, k, col.Flush(end), groups.Drain(), eng, cpu.sample())
		s.sendSamples(r.id, samples)
	}

	timer := time.NewTimer(time.Until(boundary(k)))
	var runErr error
loop:
	for {
		select {
		case <-timer.C:
			report(k, boundary(k))
			k++
			timer.Reset(time.Until(boundary(k)))
		case runErr = <-done:
			break loop
		}
	}
	timer.Stop()
	// Seconds whose boundary passed while the engine was finishing, then
	// the final partial second (always sent, even if empty, so every
	// worker reports the same last index).
	now := time.Now()
	for !boundary(k).After(now) {
		report(k, boundary(k))
		k++
	}
	if now.After(boundary(k - 1)) {
		report(k, now)
	}
	s.sendSamples(r.id, samples)

	switch {
	case runErr == nil, errors.Is(runErr, context.Canceled):
	case errors.Is(runErr, data.ErrExhausted):
		fin.DataExhausted = true
	default:
		fin.Error = runErr.Error()
	}
	return fin
}

// report sends one second's interval snapshot and error-group deltas.
func (s *session) report(runID string, k int64, snap *metrics.Snapshot, errs []model.ErrorGroup, eng *engine.Engine, cpu float64) {
	blob, err := snap.MarshalBinary()
	if err != nil {
		s.w.log.Error("cluster: encode snapshot", "run", runID, "second", k, "err", err)
		blob = nil
	}
	target, active := eng.Current()
	s.send(&pb.WorkerMessage{Msg: &pb.WorkerMessage_Metric{Metric: &pb.MetricReport{
		RunId:       runID,
		Second:      k,
		Target:      target,
		Active:      active,
		Cpu:         cpu,
		Snapshot:    blob,
		ErrorCounts: toErrorCounts(errs),
	}}})
}

// toErrorCounts converts error groups to their wire form.
func toErrorCounts(gs []model.ErrorGroup) []*pb.ErrorCount {
	if len(gs) == 0 {
		return nil
	}
	out := make([]*pb.ErrorCount, len(gs))
	for i, g := range gs {
		e := &pb.ErrorCount{Step: g.Step, Status: int32(g.Status), Message: g.Message, Sample: g.Sample, Count: g.Count}
		if !g.First.IsZero() {
			e.FirstUnixNano = g.First.UnixNano()
		}
		if !g.Last.IsZero() {
			e.LastUnixNano = g.Last.UnixNano()
		}
		out[i] = e
	}
	return out
}

// sendSamples sends error samples collected since the last call.
func (s *session) sendSamples(runID string, es *errorSampler) {
	batch := es.drain()
	if len(batch) == 0 {
		return
	}
	s.send(&pb.WorkerMessage{Msg: &pb.WorkerMessage_Errors{Errors: &pb.ErrorSamples{RunId: runID, Samples: batch}}})
}

// workerFeeder builds the data feeder for sc from the files sent by the
// master. In unique mode the worker keeps only its own partition of the
// rows (see PartitionCSV) so no row is used by two workers.
func workerFeeder(sc *scenario.Scenario, files map[string][]byte, index, count int) (data.Feeder, error) {
	if sc.Data == nil {
		return data.None{}, nil
	}
	name := filepath.Base(sc.Data.File)
	content, ok := files[name]
	if !ok {
		content, ok = files[sc.Data.File]
	}
	if !ok {
		return nil, fmt.Errorf("data.file: file %q was not sent to the worker", name)
	}
	if sc.Data.Mode == scenario.DataUnique && count > 1 {
		part, rows, err := PartitionCSV(content, index, count)
		if err != nil {
			return nil, fmt.Errorf("data.file: %w", err)
		}
		if rows == 0 {
			// Fewer rows than workers: this worker's share is empty.
			return exhaustedFeeder{}, nil
		}
		content = part
	}

	dir, err := os.MkdirTemp("", "goloadtest-worker-*")
	if err != nil {
		return nil, fmt.Errorf("cluster: temp dir: %w", err)
	}
	defer os.RemoveAll(dir) // NewCSV loads the file into memory
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("cluster: write data file: %w", err)
	}
	feeder, err := data.NewCSV(path, sc.Data.Mode)
	if err != nil {
		return nil, fmt.Errorf("data.file: %s", strings.ReplaceAll(err.Error(), path, name))
	}
	return feeder, nil
}

// exhaustedFeeder is a unique-mode partition with no rows.
type exhaustedFeeder struct{}

func (exhaustedFeeder) Next() (map[string]string, error) { return nil, data.ErrExhausted }

// errorSampler keeps the first max failed results of a run.
type errorSampler struct {
	max   int64
	taken atomic.Int64

	mu      sync.Mutex
	pending []*pb.ErrorSample
}

func (s *errorSampler) offer(r protocol.Result) {
	if s.taken.Load() >= s.max || s.taken.Add(1) > s.max {
		return
	}
	msg := ""
	if r.Err != nil {
		msg = truncate(r.Err.Error(), maxSampleMessage)
	}
	sample := &pb.ErrorSample{TUnixNano: time.Now().UnixNano(), Step: r.Step, Status: int32(r.Status), Message: msg}
	s.mu.Lock()
	s.pending = append(s.pending, sample)
	s.mu.Unlock()
}

func (s *errorSampler) drain() []*pb.ErrorSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending
	s.pending = nil
	return out
}

// truncate shortens s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s) + "…"
}
