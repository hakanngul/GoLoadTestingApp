package store

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ ScheduleStore = (*Memory)(nil)

// cloneSchedule deep-copies a schedule.
func cloneSchedule(s model.Schedule) model.Schedule {
	if s.Load != nil {
		l := *s.Load
		s.Load = &l
	}
	s.NextRunAt = cloneTimePtr(s.NextRunAt)
	s.LastRunAt = cloneTimePtr(s.LastRunAt)
	return s
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	n := normTime(*t)
	return &n
}

// scheduleLocked returns a copy of sc with TestName filled.
func (m *Memory) scheduleLocked(sc *model.Schedule) model.Schedule {
	out := cloneSchedule(*sc)
	if mt, ok := m.tests[sc.TestID]; ok {
		out.TestName = mt.test.Name
	}
	if su, ok := m.suites[sc.SuiteID]; ok {
		out.SuiteName = su.Name
	}
	return out
}

// ListSchedules returns schedules oldest created first (one test's when
// testID != "").
func (m *Memory) ListSchedules(ctx context.Context, testID string) ([]model.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []model.Schedule{}
	for _, sc := range m.scheds {
		if testID == "" || sc.TestID == testID {
			out = append(out, m.scheduleLocked(sc))
		}
	}
	slices.SortFunc(out, func(a, b model.Schedule) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nil
}

// GetSchedule returns one schedule.
func (m *Memory) GetSchedule(ctx context.Context, id string) (model.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return model.Schedule{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sc, ok := m.scheds[id]
	if !ok {
		return model.Schedule{}, ErrNotFound
	}
	return m.scheduleLocked(sc), nil
}

// scheduleTargetExistsLocked reports whether the test or suite s targets
// exists (exactly one of them is set).
func (m *Memory) scheduleTargetExistsLocked(s model.Schedule) bool {
	if s.SuiteID != "" {
		_, ok := m.suites[s.SuiteID]
		return ok && s.TestID == ""
	}
	_, ok := m.tests[s.TestID]
	return ok
}

// specOf copies the stored (user-editable) fields of s.
func specOf(dst *model.Schedule, s model.Schedule) {
	c := cloneSchedule(s)
	dst.ScheduleSpec = c.ScheduleSpec
	dst.Enabled = c.Enabled
	dst.NextRunAt = c.NextRunAt
}

// CreateSchedule stores a new schedule for an existing test or suite.
func (m *Memory) CreateSchedule(ctx context.Context, s model.Schedule) (model.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return model.Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.scheduleTargetExistsLocked(s) {
		return model.Schedule{}, ErrNotFound
	}
	ts := now()
	stored := &model.Schedule{ID: newID(), CreatedAt: ts, UpdatedAt: ts}
	specOf(stored, s)
	m.scheds[stored.ID] = stored
	return m.scheduleLocked(stored), nil
}

// UpdateSchedule replaces the spec, Enabled and NextRunAt of a schedule.
func (m *Memory) UpdateSchedule(ctx context.Context, s model.Schedule) (model.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return model.Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.scheds[s.ID]
	if !ok {
		return model.Schedule{}, ErrNotFound
	}
	if !m.scheduleTargetExistsLocked(s) {
		return model.Schedule{}, ErrNotFound
	}
	specOf(cur, s)
	cur.UpdatedAt = now()
	return m.scheduleLocked(cur), nil
}

// DeleteSchedule removes a schedule.
func (m *Memory) DeleteSchedule(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.scheds[id]; !ok {
		return ErrNotFound
	}
	delete(m.scheds, id)
	return nil
}

func setResult(sc *model.Schedule, r ScheduleResult) {
	if !r.At.IsZero() {
		sc.LastRunAt = cloneTimePtr(&r.At)
	}
	sc.LastRunID, sc.LastStatus, sc.LastMessage = r.RunID, r.Status, r.Message
}

// AdvanceSchedule records an occurrence if the schedule is still enabled
// and due at due (compare and set); see ScheduleStore.
func (m *Memory) AdvanceSchedule(ctx context.Context, id string, due time.Time, a ScheduleAdvance) (model.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return model.Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.scheds[id]
	if !ok {
		return model.Schedule{}, ErrNotFound
	}
	if !sc.Enabled || sc.NextRunAt == nil || !sc.NextRunAt.Equal(normTime(due)) {
		return model.Schedule{}, ErrConflict
	}
	sc.NextRunAt = cloneTimePtr(a.NextRunAt)
	sc.Enabled = a.Enabled
	setResult(sc, a.Result)
	return m.scheduleLocked(sc), nil
}

// SetScheduleResult replaces the Last* fields of a schedule.
func (m *Memory) SetScheduleResult(ctx context.Context, id string, r ScheduleResult) (model.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return model.Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.scheds[id]
	if !ok {
		return model.Schedule{}, ErrNotFound
	}
	setResult(sc, r)
	return m.scheduleLocked(sc), nil
}
