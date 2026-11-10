package store

import (
	"cmp"
	"context"
	"slices"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ SuiteStore = (*Memory)(nil)

func cloneSuite(s model.Suite) model.Suite {
	items := make([]model.SuiteItem, 0, len(s.Items))
	for _, it := range s.Items {
		c := model.SuiteItem{TestID: it.TestID, Workers: it.Workers}
		if it.Load != nil {
			l := *it.Load
			c.Load = &l
		}
		items = append(items, c)
	}
	s.Items = items
	s.LastRun = nil
	return s
}

func (m *Memory) suiteNameTakenLocked(name, exceptID string) bool {
	for id, su := range m.suites {
		if su.Name == name && id != exceptID {
			return true
		}
	}
	return false
}

// ListSuites returns every suite in name order.
func (m *Memory) ListSuites(ctx context.Context) ([]model.Suite, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Suite, 0, len(m.suites))
	for _, su := range m.suites {
		out = append(out, cloneSuite(*su))
	}
	slices.SortFunc(out, func(a, b model.Suite) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// GetSuite returns one suite.
func (m *Memory) GetSuite(ctx context.Context, id string) (model.Suite, error) {
	if err := ctx.Err(); err != nil {
		return model.Suite{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	su, ok := m.suites[id]
	if !ok {
		return model.Suite{}, ErrNotFound
	}
	return cloneSuite(*su), nil
}

// CreateSuite stores a new suite.
func (m *Memory) CreateSuite(ctx context.Context, s model.Suite) (model.Suite, error) {
	if err := ctx.Err(); err != nil {
		return model.Suite{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.suiteNameTakenLocked(s.Name, "") {
		return model.Suite{}, ErrConflict
	}
	st := cloneSuite(s)
	st.ID = newID()
	st.CreatedAt = now()
	st.UpdatedAt = st.CreatedAt
	m.suites[st.ID] = &st
	return cloneSuite(st), nil
}

// UpdateSuite replaces a suite's definition.
func (m *Memory) UpdateSuite(ctx context.Context, s model.Suite) (model.Suite, error) {
	if err := ctx.Err(); err != nil {
		return model.Suite{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.suites[s.ID]
	if !ok {
		return model.Suite{}, ErrNotFound
	}
	if m.suiteNameTakenLocked(s.Name, s.ID) {
		return model.Suite{}, ErrConflict
	}
	st := cloneSuite(s)
	st.CreatedAt = old.CreatedAt
	st.UpdatedAt = now()
	m.suites[st.ID] = &st
	return cloneSuite(st), nil
}

// DeleteSuite removes a suite and its suite runs.
func (m *Memory) DeleteSuite(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.suites[id]; !ok {
		return ErrNotFound
	}
	delete(m.suites, id)
	for rid, r := range m.sruns {
		if r.SuiteID == id {
			delete(m.sruns, rid)
		}
	}
	return nil
}

// CreateSuiteRun stores a new suite run.
func (m *Memory) CreateSuiteRun(ctx context.Context, r model.SuiteRun) (model.SuiteRun, error) {
	if err := ctx.Err(); err != nil {
		return model.SuiteRun{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.suites[r.SuiteID]; !ok {
		return model.SuiteRun{}, ErrNotFound
	}
	st := cloneSuiteRun(r)
	st.ID = newID()
	m.sruns[st.ID] = &st
	return cloneSuiteRun(st), nil
}

// UpdateSuiteRun replaces the mutable fields of a suite run.
func (m *Memory) UpdateSuiteRun(ctx context.Context, r model.SuiteRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.sruns[r.ID]
	if !ok {
		return ErrNotFound
	}
	c := cloneSuiteRun(r)
	st.Status, st.FinishedAt, st.Items, st.Error = c.Status, c.FinishedAt, c.Items, c.Error
	return nil
}

// GetSuiteRun returns one suite run.
func (m *Memory) GetSuiteRun(ctx context.Context, id string) (model.SuiteRun, error) {
	if err := ctx.Err(); err != nil {
		return model.SuiteRun{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.sruns[id]
	if !ok {
		return model.SuiteRun{}, ErrNotFound
	}
	return cloneSuiteRun(*r), nil
}

// ListSuiteRuns returns suite runs newest first.
func (m *Memory) ListSuiteRuns(ctx context.Context, f SuiteRunFilter) ([]model.SuiteRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []model.SuiteRun{}
	for _, r := range m.sruns {
		if f.SuiteID != "" && r.SuiteID != f.SuiteID {
			continue
		}
		if f.Active && r.Status.Finished() {
			continue
		}
		out = append(out, cloneSuiteRun(*r))
	}
	slices.SortFunc(out, func(a, b model.SuiteRun) int {
		return cmp.Or(b.StartedAt.Compare(a.StartedAt), cmp.Compare(b.ID, a.ID))
	})
	if l := suiteRunLimit(f); len(out) > l {
		out = out[:l]
	}
	return out, nil
}
