package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// fakeStore is a minimal in-memory store.Store for runner tests. Only the
// methods the runner uses do real work.
type fakeStore struct {
	mu        sync.Mutex
	nextID    int
	files     map[string][]byte // testID/name
	runs      map[string]model.Run
	metrics   map[string][]model.MetricPoint
	snapshots map[string][][]byte
	samples   map[string][]model.ErrorSample
	groups    map[string][]model.ErrorGroup

	tests map[string]model.Test // GetTest results (absent: ErrNotFound)

	failMetrics bool // AppendMetric / AppendErrorSamples return an error
}

var _ store.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		files:     map[string][]byte{},
		runs:      map[string]model.Run{},
		metrics:   map[string][]model.MetricPoint{},
		snapshots: map[string][][]byte{},
		samples:   map[string][]model.ErrorSample{},
		groups:    map[string][]model.ErrorGroup{},
	}
}

var errFake = errors.New("fake store failure")

func (s *fakeStore) ListTests(context.Context) ([]model.Test, error) { return nil, nil }
func (s *fakeStore) GetTest(_ context.Context, id string) (model.Test, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tests[id]; ok {
		return t, nil
	}
	return model.Test{}, store.ErrNotFound
}
func (s *fakeStore) CreateTest(_ context.Context, t model.Test) (model.Test, error) { return t, nil }
func (s *fakeStore) UpdateTest(_ context.Context, t model.Test) (model.Test, error) { return t, nil }
func (s *fakeStore) DeleteTest(context.Context, string) error                       { return nil }

func (s *fakeStore) PutFile(_ context.Context, testID, name string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[testID+"/"+name] = append([]byte(nil), content...)
	return nil
}

func (s *fakeStore) GetFile(_ context.Context, testID, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[testID+"/"+name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return b, nil
}

func (s *fakeStore) DeleteFile(context.Context, string, string) error { return nil }

func (s *fakeStore) CreateRun(_ context.Context, r model.Run) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	r.ID = fmt.Sprintf("run-%d", s.nextID)
	s.runs[r.ID] = r
	return r, nil
}

func (s *fakeStore) UpdateRun(_ context.Context, r model.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.runs[r.ID]
	if !ok {
		return store.ErrNotFound
	}
	r.Label, r.Notes = cur.Label, cur.Notes // like the real stores
	s.runs[r.ID] = r
	return nil
}

func (s *fakeStore) GetRun(_ context.Context, id string) (model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return model.Run{}, store.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) ListRuns(context.Context, store.RunFilter) ([]model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	return out, nil
}

func (s *fakeStore) MarkInterrupted(context.Context, string) (int, error) { return 0, nil }

func (s *fakeStore) AppendMetric(_ context.Context, runID string, p model.MetricPoint, snap []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failMetrics {
		return errFake
	}
	s.metrics[runID] = append(s.metrics[runID], p)
	s.snapshots[runID] = append(s.snapshots[runID], snap)
	return nil
}

func (s *fakeStore) ListMetrics(_ context.Context, runID string) ([]model.MetricPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.MetricPoint(nil), s.metrics[runID]...), nil
}

func (s *fakeStore) ListSnapshots(_ context.Context, runID string) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.snapshots[runID]...), nil
}

func (s *fakeStore) AppendErrorSamples(_ context.Context, runID string, smp []model.ErrorSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failMetrics {
		return errFake
	}
	s.samples[runID] = append(s.samples[runID], smp...)
	return nil
}

func (s *fakeStore) ListErrorSamples(_ context.Context, runID string) ([]model.ErrorSample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.ErrorSample(nil), s.samples[runID]...), nil
}

func (s *fakeStore) SetErrorGroups(_ context.Context, runID string, g []model.ErrorGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return store.ErrNotFound
	}
	s.groups[runID] = append([]model.ErrorGroup(nil), g...)
	return nil
}

func (s *fakeStore) ListErrorGroups(_ context.Context, runID string) ([]model.ErrorGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return nil, store.ErrNotFound
	}
	return append([]model.ErrorGroup{}, s.groups[runID]...), nil
}

func (s *fakeStore) Close() {}

func (s *fakeStore) UpdateRunMeta(context.Context, string, *string, *string, *bool) (model.Run, error) {
	return model.Run{}, store.ErrNotFound
}
func (s *fakeStore) DeleteRun(context.Context, string) error { return store.ErrNotFound }
func (s *fakeStore) ListMetricsWithSnapshots(context.Context, string) ([]model.MetricPoint, [][]byte, error) {
	return nil, nil, nil
}
func (s *fakeStore) SetBaseline(context.Context, string, string) (model.Test, error) {
	return model.Test{}, store.ErrNotFound
}
func (s *fakeStore) RunsSince(context.Context, time.Time) ([]model.Run, error) { return nil, nil }

func (s *fakeStore) runCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}
