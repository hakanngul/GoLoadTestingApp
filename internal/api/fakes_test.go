package api

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// fakeStore is a minimal in-memory store.Store for handler tests.
type fakeStore struct {
	mu      sync.Mutex
	seq     int
	tests   map[string]model.Test
	files   map[string]map[string][]byte
	runs    map[string]model.Run
	metrics map[string][]model.MetricPoint
	errs    map[string][]model.ErrorSample
	groups  map[string][]model.ErrorGroup
	lastRF  store.RunFilter
}

var _ store.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		tests:   map[string]model.Test{},
		files:   map[string]map[string][]byte{},
		runs:    map[string]model.Run{},
		metrics: map[string][]model.MetricPoint{},
		errs:    map[string][]model.ErrorSample{},
		groups:  map[string][]model.ErrorGroup{},
	}
}

func (f *fakeStore) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *fakeStore) withFiles(t model.Test) model.Test {
	t.Files = nil
	for name, b := range f.files[t.ID] {
		t.Files = append(t.Files, model.FileInfo{Name: name, Size: int64(len(b))})
	}
	return t
}

func (f *fakeStore) ListTests(context.Context) ([]model.Test, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Test
	for _, t := range f.tests {
		out = append(out, f.withFiles(t))
	}
	return out, nil
}

func (f *fakeStore) GetTest(_ context.Context, id string) (model.Test, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tests[id]
	if !ok {
		return model.Test{}, store.ErrNotFound
	}
	return f.withFiles(t), nil
}

func (f *fakeStore) nameTaken(name, except string) bool {
	for _, t := range f.tests {
		if t.Name == name && t.ID != except {
			return true
		}
	}
	return false
}

func (f *fakeStore) CreateTest(_ context.Context, t model.Test) (model.Test, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nameTaken(t.Name, "") {
		return model.Test{}, store.ErrConflict
	}
	t.ID = f.nextID("test")
	t.CreatedAt, t.UpdatedAt = time.Now(), time.Now()
	f.tests[t.ID] = t
	return t, nil
}

func (f *fakeStore) UpdateTest(_ context.Context, t model.Test) (model.Test, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.tests[t.ID]
	if !ok {
		return model.Test{}, store.ErrNotFound
	}
	if f.nameTaken(t.Name, t.ID) {
		return model.Test{}, store.ErrConflict
	}
	old.Name, old.YAML, old.UpdatedAt = t.Name, t.YAML, time.Now()
	f.tests[t.ID] = old
	return f.withFiles(old), nil
}

func (f *fakeStore) DeleteTest(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tests[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.tests, id)
	delete(f.files, id)
	return nil
}

func (f *fakeStore) PutFile(_ context.Context, testID, name string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tests[testID]; !ok {
		return store.ErrNotFound
	}
	if f.files[testID] == nil {
		f.files[testID] = map[string][]byte{}
	}
	f.files[testID][name] = content
	return nil
}

func (f *fakeStore) GetFile(_ context.Context, testID, name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[testID][name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) DeleteFile(_ context.Context, testID, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[testID][name]; !ok {
		return store.ErrNotFound
	}
	delete(f.files[testID], name)
	return nil
}

func (f *fakeStore) CreateRun(_ context.Context, r model.Run) (model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ID == "" {
		r.ID = f.nextID("run")
	}
	f.runs[r.ID] = r
	return r, nil
}

func (f *fakeStore) UpdateRun(_ context.Context, r model.Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[r.ID]; !ok {
		return store.ErrNotFound
	}
	f.runs[r.ID] = r
	return nil
}

func (f *fakeStore) GetRun(_ context.Context, id string) (model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return model.Run{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) ListRuns(_ context.Context, rf store.RunFilter) ([]model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRF = rf
	var out []model.Run
	for _, r := range f.runs {
		if rf.TestID == "" || r.TestID == rf.TestID {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b model.Run) int { return b.StartedAt.Compare(a.StartedAt) })
	if rf.Limit > 0 && len(out) > rf.Limit {
		out = out[:rf.Limit]
	}
	return out, nil
}

func (f *fakeStore) MarkInterrupted(context.Context, string) (int, error) { return 0, nil }

func (f *fakeStore) AppendMetric(_ context.Context, runID string, p model.MetricPoint, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics[runID] = append(f.metrics[runID], p)
	return nil
}

func (f *fakeStore) ListMetrics(_ context.Context, runID string) ([]model.MetricPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metrics[runID], nil
}

func (f *fakeStore) ListSnapshots(context.Context, string) ([][]byte, error) { return nil, nil }

func (f *fakeStore) AppendErrorSamples(_ context.Context, runID string, s []model.ErrorSample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[runID] = append(f.errs[runID], s...)
	return nil
}

func (f *fakeStore) ListErrorSamples(_ context.Context, runID string) ([]model.ErrorSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.errs[runID], nil
}

func (f *fakeStore) Close() {}

func (f *fakeStore) UpdateRunMeta(_ context.Context, id string, label, notes *string) (model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return model.Run{}, store.ErrNotFound
	}
	if label != nil {
		r.Label = *label
	}
	if notes != nil {
		r.Notes = *notes
	}
	f.runs[id] = r
	return r, nil
}

func (f *fakeStore) DeleteRun(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.runs, id)
	return nil
}

func (f *fakeStore) ListMetricsWithSnapshots(_ context.Context, runID string) ([]model.MetricPoint, [][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pts := f.metrics[runID]
	return pts, make([][]byte, len(pts)), nil
}

func (f *fakeStore) SetBaseline(_ context.Context, testID, runID string) (model.Test, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tests[testID]
	if !ok {
		return model.Test{}, store.ErrNotFound
	}
	t.BaselineRunID = runID
	f.tests[testID] = t
	return f.withFiles(t), nil
}

func (f *fakeStore) RunsSince(context.Context, time.Time) ([]model.Run, error) { return nil, nil }

func (f *fakeStore) SetErrorGroups(_ context.Context, runID string, g []model.ErrorGroup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[runID]; !ok {
		return store.ErrNotFound
	}
	f.groups[runID] = append([]model.ErrorGroup(nil), g...)
	return nil
}

func (f *fakeStore) ListErrorGroups(_ context.Context, runID string) ([]model.ErrorGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.runs[runID]; !ok {
		return nil, store.ErrNotFound
	}
	return append([]model.ErrorGroup{}, f.groups[runID]...), nil
}

// fakeRunner is a scriptable runner.Runner.
type fakeRunner struct {
	mu       sync.Mutex
	store    store.Store
	startErr error
	stopErr  error
	active   map[string]bool
	// subs holds the event channel handed out per run; tests push events
	// into it. cancelled is closed when the subscription is cancelled.
	subs      map[string]chan runner.Event
	cancelled map[string]chan struct{}
	workers   []model.Worker
	lastReq   model.StartRunRequest
	// groups are the live error groups returned for active runs.
	groups map[string][]model.ErrorGroup
}

var _ runner.Runner = (*fakeRunner)(nil)

func newFakeRunner(s store.Store) *fakeRunner {
	return &fakeRunner{
		store:     s,
		active:    map[string]bool{},
		subs:      map[string]chan runner.Event{},
		cancelled: map[string]chan struct{}{},
	}
}

func (r *fakeRunner) Start(ctx context.Context, t model.Test, req model.StartRunRequest) (model.Run, error) {
	r.mu.Lock()
	r.lastReq = req
	err := r.startErr
	r.mu.Unlock()
	if err != nil {
		return model.Run{}, err
	}
	run, err := r.store.CreateRun(ctx, model.Run{
		TestID: t.ID, TestName: t.Name, Status: model.RunRunning, YAML: t.YAML,
		Workers: max(req.Workers, 1), StartedAt: time.Now(), Label: req.Label, Notes: req.Notes,
		ScheduleID: req.ScheduleID,
	})
	if err != nil {
		return model.Run{}, err
	}
	r.mu.Lock()
	r.active[run.ID] = true
	r.mu.Unlock()
	return run, nil
}

func (r *fakeRunner) Stop(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopErr != nil {
		return r.stopErr
	}
	if !r.active[id] {
		return runner.ErrNotRunning
	}
	return nil
}

// activate makes id streamable and returns the channel the test feeds and
// a channel closed when the handler cancels its subscription.
func (r *fakeRunner) activate(id string) (chan runner.Event, chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan runner.Event, 16)
	done := make(chan struct{})
	r.active[id] = true
	r.subs[id] = ch
	r.cancelled[id] = done
	return ch, done
}

func (r *fakeRunner) Subscribe(id string) (<-chan runner.Event, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.subs[id]
	if !ok {
		return nil, nil, runner.ErrNotRunning
	}
	done := r.cancelled[id]
	var once sync.Once
	return ch, func() { once.Do(func() { close(done) }) }, nil
}

func (r *fakeRunner) ErrorGroups(id string) ([]model.ErrorGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active[id] {
		return nil, runner.ErrNotRunning
	}
	return append([]model.ErrorGroup{}, r.groups[id]...), nil
}

func (r *fakeRunner) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id := range r.active {
		ids = append(ids, id)
	}
	return ids
}

func (r *fakeRunner) Workers() []model.Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.workers
}
