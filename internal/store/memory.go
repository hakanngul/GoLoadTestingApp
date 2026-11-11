package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// Memory is an in-process Store used by tests and `serve -db memory`.
//
// It mirrors the PostgreSQL implementation's semantics (ordering, error
// values, cascades, timestamp precision) and deep-copies every value that
// crosses its boundary, so callers can never alias internal state.
// The zero value is not usable; call NewMemory.
type Memory struct {
	mu      sync.RWMutex
	tests   map[string]*memTest
	names   map[string]string // test name -> test ID (unique index)
	runs    map[string]*model.Run
	metrics map[string][]memMetric                // run ID -> points, sorted by T
	errs    map[string][]model.ErrorSample        // run ID -> samples, sorted by T
	groups  map[string][]model.ErrorGroup         // run ID -> final error groups
	scheds  map[string]*model.Schedule            // schedule ID -> schedule (TestName empty)
	suites  map[string]*model.Suite               // suite ID -> suite (item names empty)
	sruns   map[string]*model.SuiteRun            // suite run ID -> suite run
	notifs  map[string]*model.NotificationChannel // channel ID -> channel
}

type memTest struct {
	test  model.Test        // Files and LastRun are always empty here
	files map[string][]byte // name -> content
}

type memMetric struct {
	point    model.MetricPoint
	snapshot []byte
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		tests:   make(map[string]*memTest),
		names:   make(map[string]string),
		runs:    make(map[string]*model.Run),
		metrics: make(map[string][]memMetric),
		errs:    make(map[string][]model.ErrorSample),
		groups:  make(map[string][]model.ErrorGroup),
		scheds:  make(map[string]*model.Schedule),
		suites:  make(map[string]*model.Suite),
		sruns:   make(map[string]*model.SuiteRun),
		notifs:  make(map[string]*model.NotificationChannel),
	}
}

// Close is a no-op; it exists to satisfy Store.
func (m *Memory) Close() {}

// ---- tests -----------------------------------------------------------------

// ListTests returns all tests, most recently updated first, each with its
// files and newest run.
func (m *Memory) ListTests(ctx context.Context) ([]model.Test, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Newest run per test in one pass over runs (mirrors the single query
	// used by Postgres).
	last := make(map[string]*model.Run, len(m.tests))
	for _, r := range m.runs {
		if cur, ok := last[r.TestID]; !ok || runLess(r, cur) {
			last[r.TestID] = r
		}
	}

	out := make([]model.Test, 0, len(m.tests))
	for _, mt := range m.tests {
		t := m.testLocked(mt)
		if r, ok := last[t.ID]; ok {
			c := cloneRun(*r)
			t.LastRun = &c
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b model.Test) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out, nil
}

// GetTest returns one test with its file list (LastRun is not filled).
func (m *Memory) GetTest(ctx context.Context, id string) (model.Test, error) {
	if err := ctx.Err(); err != nil {
		return model.Test{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	mt, ok := m.tests[id]
	if !ok {
		return model.Test{}, ErrNotFound
	}
	return m.testLocked(mt), nil
}

// CreateTest stores a new test with a fresh ID and timestamps. Files and
// LastRun on t are ignored (files are attached with PutFile).
func (m *Memory) CreateTest(ctx context.Context, t model.Test) (model.Test, error) {
	if err := ctx.Err(); err != nil {
		return model.Test{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.names[t.Name]; dup {
		return model.Test{}, ErrConflict
	}
	ts := now()
	stored := model.Test{ID: newID(), Name: t.Name, YAML: t.YAML, CreatedAt: ts, UpdatedAt: ts}
	m.tests[stored.ID] = &memTest{test: stored, files: make(map[string][]byte)}
	m.names[stored.Name] = stored.ID
	return m.testLocked(m.tests[stored.ID]), nil
}

// UpdateTest replaces the name and YAML of an existing test and bumps
// UpdatedAt.
func (m *Memory) UpdateTest(ctx context.Context, t model.Test) (model.Test, error) {
	if err := ctx.Err(); err != nil {
		return model.Test{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tests[t.ID]
	if !ok {
		return model.Test{}, ErrNotFound
	}
	if owner, dup := m.names[t.Name]; dup && owner != t.ID {
		return model.Test{}, ErrConflict
	}
	delete(m.names, mt.test.Name)
	m.names[t.Name] = t.ID
	mt.test.Name = t.Name
	mt.test.YAML = t.YAML
	mt.test.UpdatedAt = now()
	return m.testLocked(mt), nil
}

// DeleteTest removes a test together with its files, runs, metrics and
// error samples.
func (m *Memory) DeleteTest(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tests[id]
	if !ok {
		return ErrNotFound
	}
	delete(m.names, mt.test.Name)
	delete(m.tests, id)
	for rid, r := range m.runs {
		if r.TestID == id {
			m.deleteRunLocked(rid)
		}
	}
	for sid, sc := range m.scheds {
		if sc.TestID == id {
			delete(m.scheds, sid)
		}
	}
	return nil
}

// testLocked returns a deep copy of mt.test with Files filled (sorted by
// name). The caller must hold m.mu.
func (m *Memory) testLocked(mt *memTest) model.Test {
	t := mt.test
	t.Files = make([]model.FileInfo, 0, len(mt.files))
	for name, c := range mt.files {
		t.Files = append(t.Files, model.FileInfo{Name: name, Size: int64(len(c))})
	}
	slices.SortFunc(t.Files, func(a, b model.FileInfo) int { return cmp.Compare(a.Name, b.Name) })
	t.LastRun = nil
	return t
}

// ---- files -----------------------------------------------------------------

// PutFile creates or replaces a data file attached to a test.
func (m *Memory) PutFile(ctx context.Context, testID, name string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tests[testID]
	if !ok {
		return ErrNotFound
	}
	mt.files[name] = cloneBytes(content)
	return nil
}

// GetFile returns a copy of a file's content.
func (m *Memory) GetFile(ctx context.Context, testID, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	mt, ok := m.tests[testID]
	if !ok {
		return nil, ErrNotFound
	}
	c, ok := mt.files[name]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneBytes(c), nil
}

// DeleteFile removes a file from a test.
func (m *Memory) DeleteFile(ctx context.Context, testID, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tests[testID]
	if !ok {
		return ErrNotFound
	}
	if _, ok := mt.files[name]; !ok {
		return ErrNotFound
	}
	delete(mt.files, name)
	return nil
}

// ---- runs ------------------------------------------------------------------

// CreateRun stores a new run for an existing test. The store assigns the
// ID; StartedAt defaults to now, Status to queued and TestName to the
// test's current name.
func (m *Memory) CreateRun(ctx context.Context, r model.Run) (model.Run, error) {
	if err := ctx.Err(); err != nil {
		return model.Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tests[r.TestID]
	if !ok {
		return model.Run{}, ErrNotFound
	}
	stored := normRun(cloneRun(r))
	stored.ID = newID()
	if stored.TestName == "" {
		stored.TestName = mt.test.Name
	}
	if stored.Status == "" {
		stored.Status = model.RunQueued
	}
	if stored.StartedAt.IsZero() {
		stored.StartedAt = now()
	}
	m.runs[stored.ID] = &stored
	return cloneRun(stored), nil
}

// UpdateRun replaces every field of an existing run except ID, TestID,
// Label and Notes. A zero StartedAt keeps the stored value.
func (m *Memory) UpdateRun(ctx context.Context, r model.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[r.ID]
	if !ok {
		return ErrNotFound
	}
	next := normRun(cloneRun(r))
	next.ID, next.TestID = cur.ID, cur.TestID
	next.Label, next.Notes, next.ScheduleID, next.Pinned = cur.Label, cur.Notes, cur.ScheduleID, cur.Pinned
	if next.StartedAt.IsZero() {
		next.StartedAt = cur.StartedAt
	}
	*cur = next
	return nil
}

// GetRun returns one run.
func (m *Memory) GetRun(ctx context.Context, id string) (model.Run, error) {
	if err := ctx.Err(); err != nil {
		return model.Run{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runs[id]
	if !ok {
		return model.Run{}, ErrNotFound
	}
	return cloneRun(*r), nil
}

// ListRuns returns runs newest first, optionally restricted to one test.
// A non-positive limit means 50.
func (m *Memory) ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRunLimit
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	matched := make([]*model.Run, 0)
	for _, r := range m.runs {
		if f.TestID == "" || r.TestID == f.TestID {
			matched = append(matched, r)
		}
	}
	slices.SortFunc(matched, func(a, b *model.Run) int {
		if runLess(a, b) {
			return -1
		}
		if runLess(b, a) {
			return 1
		}
		return 0
	})
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]model.Run, len(matched))
	for i, r := range matched {
		out[i] = cloneRun(*r)
	}
	return out, nil
}

// MarkInterrupted moves every queued or running run to error with msg and
// sets FinishedAt to now. It returns the number of runs changed.
func (m *Memory) MarkInterrupted(ctx context.Context, msg string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ts := now()
	n := 0
	for _, r := range m.runs {
		if r.Status == model.RunQueued || r.Status == model.RunRunning {
			r.Status = model.RunError
			r.Error = msg
			fin := ts
			r.FinishedAt = &fin
			n++
		}
	}
	return n, nil
}

// deleteRunLocked removes a run and its children and clears a baseline
// pointing at it (ON DELETE SET NULL). Caller holds m.mu.
func (m *Memory) deleteRunLocked(id string) {
	if r, ok := m.runs[id]; ok {
		if mt, ok := m.tests[r.TestID]; ok && mt.test.BaselineRunID == id {
			mt.test.BaselineRunID = ""
		}
	}
	delete(m.runs, id)
	delete(m.metrics, id)
	delete(m.errs, id)
	delete(m.groups, id)
}

// runLess orders runs newest first (StartedAt desc, then ID desc), the
// same order Postgres uses.
func runLess(a, b *model.Run) bool {
	if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
		return c > 0
	}
	return a.ID > b.ID
}

// ---- metrics & error samples ------------------------------------------------

// AppendMetric stores one per-second point and its encoded snapshot. A
// point with the same T as an existing one replaces it.
func (m *Memory) AppendMetric(ctx context.Context, runID string, p model.MetricPoint, snapshot []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return ErrNotFound
	}
	p.T = normTime(p.T)
	mm := memMetric{point: p, snapshot: cloneBytes(snapshot)}
	pts := m.metrics[runID]
	i, found := slices.BinarySearchFunc(pts, p.T, func(e memMetric, t time.Time) int { return e.point.T.Compare(t) })
	if found {
		pts[i] = mm
	} else {
		pts = slices.Insert(pts, i, mm)
	}
	m.metrics[runID] = pts
	return nil
}

// ListMetrics returns a run's points ordered by T (empty for unknown runs).
func (m *Memory) ListMetrics(ctx context.Context, runID string) ([]model.MetricPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	pts := m.metrics[runID]
	out := make([]model.MetricPoint, len(pts))
	for i, p := range pts {
		out[i] = p.point
	}
	return out, nil
}

// ListSnapshots returns a run's encoded snapshots ordered by T (one per
// stored point, empty for unknown runs).
func (m *Memory) ListSnapshots(ctx context.Context, runID string) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	pts := m.metrics[runID]
	out := make([][]byte, len(pts))
	for i, p := range pts {
		out[i] = cloneBytes(p.snapshot)
	}
	return out, nil
}

// AppendErrorSamples stores error samples for a run. An empty slice is a
// no-op.
func (m *Memory) AppendErrorSamples(ctx context.Context, runID string, s []model.ErrorSample) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(s) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return ErrNotFound
	}
	cur := m.errs[runID]
	for _, e := range s {
		e.T = normTime(e.T)
		cur = append(cur, e)
	}
	// Stable: equal timestamps keep insertion order, like (t, id) in SQL.
	slices.SortStableFunc(cur, func(a, b model.ErrorSample) int { return a.T.Compare(b.T) })
	m.errs[runID] = cur
	return nil
}

// ListErrorSamples returns a run's error samples ordered by T, then
// insertion order (empty for unknown runs).
func (m *Memory) ListErrorSamples(ctx context.Context, runID string) ([]model.ErrorSample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	src := m.errs[runID]
	return append(make([]model.ErrorSample, 0, len(src)), src...), nil
}

// SetErrorGroups replaces a run's error groups.
func (m *Memory) SetErrorGroups(ctx context.Context, runID string, groups []model.ErrorGroup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return ErrNotFound
	}
	cp := make([]model.ErrorGroup, len(groups))
	for i, g := range groups {
		g.First, g.Last = normTime(g.First), normTime(g.Last)
		cp[i] = g
	}
	m.groups[runID] = cp
	return nil
}

// ListErrorGroups returns a run's stored error groups.
func (m *Memory) ListErrorGroups(ctx context.Context, runID string) ([]model.ErrorGroup, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, ErrNotFound
	}
	src := m.groups[runID]
	return append(make([]model.ErrorGroup, 0, len(src)), src...), nil
}

// ---- deep-copy helpers ------------------------------------------------------

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// cloneRun deep-copies r. Nil Thresholds/Warnings become empty slices so
// they always encode as JSON [] rather than null.
func cloneRun(r model.Run) model.Run {
	if r.FinishedAt != nil {
		f := *r.FinishedAt
		r.FinishedAt = &f
	}
	if r.Summary != nil {
		s := cloneSummary(*r.Summary)
		r.Summary = &s
	}
	r.Thresholds = append(make([]model.ThresholdResult, 0, len(r.Thresholds)), r.Thresholds...)
	r.Warnings = append(make([]string, 0, len(r.Warnings)), r.Warnings...)
	return r
}

func cloneSummary(s model.Summary) model.Summary {
	if len(s.ByStep) == 0 {
		s.ByStep = nil // omitempty: an empty map round-trips through JSON as nil
	} else {
		by := make(map[string]model.Summary, len(s.ByStep))
		for k, v := range s.ByStep {
			by[k] = cloneSummary(v)
		}
		s.ByStep = by
	}
	return s
}

// normRun normalises the run's timestamps to storage precision.
func normRun(r model.Run) model.Run {
	r.StartedAt = normTime(r.StartedAt)
	if r.FinishedAt != nil {
		f := normTime(*r.FinishedAt)
		r.FinishedAt = &f
	}
	return r
}

// ---- reporting ---------------------------------------------------------------

// UpdateRunMeta sets label, notes and/or pinned (nil = unchanged).
func (m *Memory) UpdateRunMeta(ctx context.Context, id string, label, notes *string, pinned *bool) (model.Run, error) {
	if err := ctx.Err(); err != nil {
		return model.Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return model.Run{}, ErrNotFound
	}
	if label != nil {
		r.Label = *label
	}
	if notes != nil {
		r.Notes = *notes
	}
	if pinned != nil {
		r.Pinned = *pinned
	}
	return cloneRun(*r), nil
}

// DeleteRun removes a run with its metrics and error samples and clears a
// test baseline pointing at it.
func (m *Memory) DeleteRun(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[id]; !ok {
		return ErrNotFound
	}
	m.deleteRunLocked(id)
	return nil
}

// ListMetricsWithSnapshots returns a run's points and snapshots, aligned.
func (m *Memory) ListMetricsWithSnapshots(ctx context.Context, runID string) ([]model.MetricPoint, [][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	src := m.metrics[runID]
	pts := make([]model.MetricPoint, len(src))
	snaps := make([][]byte, len(src))
	for i, p := range src {
		pts[i] = p.point
		snaps[i] = cloneBytes(p.snapshot)
	}
	return pts, snaps, nil
}

// SetBaseline sets or clears a test's baseline run.
func (m *Memory) SetBaseline(ctx context.Context, testID, runID string) (model.Test, error) {
	if err := ctx.Err(); err != nil {
		return model.Test{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tests[testID]
	if !ok {
		return model.Test{}, ErrNotFound
	}
	if runID != "" {
		r, ok := m.runs[runID]
		if !ok {
			return model.Test{}, ErrNotFound
		}
		if r.TestID != testID {
			return model.Test{}, fmt.Errorf("%w: run belongs to another test", ErrConflict)
		}
	}
	mt.test.BaselineRunID = runID
	return m.testLocked(mt), nil
}

// RunsSince returns runs started at or after since, newest first, with
// summaries stripped of by_step.
func (m *Memory) RunsSince(ctx context.Context, since time.Time) ([]model.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	since = normTime(since)
	m.mu.RLock()
	defer m.mu.RUnlock()
	matched := make([]*model.Run, 0)
	for _, r := range m.runs {
		if !r.StartedAt.Before(since) {
			matched = append(matched, r)
		}
	}
	slices.SortFunc(matched, func(a, b *model.Run) int {
		if runLess(a, b) {
			return -1
		}
		if runLess(b, a) {
			return 1
		}
		return 0
	})
	out := make([]model.Run, len(matched))
	for i, r := range matched {
		out[i] = cloneRun(*r)
		if out[i].Summary != nil {
			out[i].Summary.ByStep = nil
		}
	}
	return out, nil
}

var _ Pruner = (*Memory)(nil)

// PruneRuns deletes old finished runs except pinned runs, baselines and
// each test's latest finished run (see Pruner).
func (m *Memory) PruneRuns(ctx context.Context, olderThan time.Time, dryRun bool) ([]PrunedRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := normTime(olderThan)
	keep := make(map[string]bool)
	latest := make(map[string]*model.Run) // test ID -> latest finished run
	for _, mt := range m.tests {
		if id := mt.test.BaselineRunID; id != "" {
			keep[id] = true
		}
	}
	for _, r := range m.runs {
		if !prunableStatus(r.Status) {
			continue
		}
		if cur, ok := latest[r.TestID]; !ok || runLess(r, cur) {
			latest[r.TestID] = r
		}
	}
	for _, r := range latest {
		keep[r.ID] = true
	}

	var out []PrunedRun
	for _, r := range m.runs {
		if !prunableStatus(r.Status) || r.Pinned || keep[r.ID] {
			continue
		}
		at := r.StartedAt
		if r.FinishedAt != nil {
			at = *r.FinishedAt
		}
		if !at.Before(cutoff) {
			continue
		}
		p := PrunedRun{ID: r.ID, TestID: r.TestID, TestName: r.TestName, Status: r.Status, StartedAt: r.StartedAt}
		if r.FinishedAt != nil {
			p.FinishedAt = *r.FinishedAt
		}
		out = append(out, p)
	}
	slices.SortFunc(out, comparePruned)
	if !dryRun {
		for _, p := range out {
			m.deleteRunLocked(p.ID)
		}
	}
	return out, nil
}

// prunableStatus reports whether a run in status s may be pruned: every
// status except the active ones.
func prunableStatus(s model.RunStatus) bool {
	return s != model.RunQueued && s != model.RunRunning
}

// comparePruned orders pruned runs oldest first (StartedAt, then ID).
func comparePruned(a, b PrunedRun) int {
	if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}
