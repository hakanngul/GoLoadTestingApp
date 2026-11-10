package store

import (
	"errors"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func schedStore(t *testing.T, s Store) ScheduleStore {
	t.Helper()
	ss, ok := s.(ScheduleStore)
	if !ok {
		t.Fatalf("%T does not implement ScheduleStore", s)
	}
	return ss
}

func tp(t time.Time) *time.Time { return &t }

func sampleSchedule(testID string) model.Schedule {
	return model.Schedule{
		ScheduleSpec: model.ScheduleSpec{
			TestID:   testID,
			Name:     "nightly",
			Kind:     model.ScheduleRecurring,
			Cron:     "0 3 * * 1-5",
			Timezone: "Europe/Istanbul",
			Load: &model.LoadOverride{Mode: model.LoadProfile, Users: 20,
				RampUpMs: 30000, HoldMs: 600000, RampDownMs: 10000},
			Workers:        2,
			Label:          "nightly run",
			Notes:          "notes",
			Overlap:        model.OverlapQueue,
			QueueTimeoutMs: 1800000,
		},
		Enabled:   true,
		NextRunAt: tp(base.Add(time.Hour)),
	}
}

func testScheduleCRUD(t *testing.T, s Store) {
	ss := schedStore(t, s)
	c := ctx(t)
	tt := mustTest(t, s, "sched-test")
	other := mustTest(t, s, "other-test")

	in := sampleSchedule(tt.ID)
	in.ID = "ignored"
	in.LastStatus = "ignored"
	before := time.Now().Add(-time.Second)
	got, err := ss.CreateSchedule(c, in)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if !isUUID(got.ID) {
		t.Fatalf("ID %q not a UUID", got.ID)
	}
	if got.CreatedAt.Before(before) || !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("timestamps: %v %v", got.CreatedAt, got.UpdatedAt)
	}
	want := in
	want.ID, want.TestName, want.LastStatus = got.ID, "sched-test", ""
	want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
	jsonEq(t, "created", got, want)

	g, err := ss.GetSchedule(c, got.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	jsonEq(t, "get", g, want)

	// Once schedule without load, disabled, no next run.
	once, err := ss.CreateSchedule(c, model.Schedule{ScheduleSpec: model.ScheduleSpec{
		TestID: other.ID, Name: "once", Kind: model.ScheduleOnce, At: "2026-05-01T10:00",
		Timezone: "UTC", Overlap: model.OverlapSkip}})
	if err != nil {
		t.Fatalf("CreateSchedule once: %v", err)
	}
	if once.Load != nil || once.NextRunAt != nil || once.Enabled || once.LastRunAt != nil {
		t.Fatalf("once = %+v", once)
	}

	all, err := ss.ListSchedules(c, "")
	if err != nil || len(all) != 2 || all[0].ID != got.ID || all[1].ID != once.ID {
		t.Fatalf("ListSchedules all = %+v, %v", all, err)
	}
	one, err := ss.ListSchedules(c, other.ID)
	if err != nil || len(one) != 1 || one[0].ID != once.ID || one[0].TestName != "other-test" {
		t.Fatalf("ListSchedules(other) = %+v, %v", one, err)
	}
	none, err := ss.ListSchedules(c, "not-a-uuid")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("ListSchedules(bad) = %v, %v", none, err)
	}

	// Update replaces the spec and keeps Last*.
	if _, err := ss.SetScheduleResult(c, got.ID, ScheduleResult{At: base, RunID: "r1", Status: model.ScheduleStarted}); err != nil {
		t.Fatalf("SetScheduleResult: %v", err)
	}
	upd := got
	upd.Name = "renamed"
	upd.TestID = other.ID
	upd.Load = nil
	upd.Enabled = false
	upd.NextRunAt = nil
	upd.Cron = "*/5 * * * *"
	time.Sleep(2 * time.Millisecond)
	u, err := ss.UpdateSchedule(c, upd)
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if u.Name != "renamed" || u.TestName != "other-test" || u.Load != nil || u.Enabled || u.NextRunAt != nil ||
		u.Cron != "*/5 * * * *" || !u.CreatedAt.Equal(got.CreatedAt) || !u.UpdatedAt.After(got.UpdatedAt) {
		t.Fatalf("updated = %+v", u)
	}
	if u.LastRunID != "r1" || u.LastStatus != model.ScheduleStarted || u.LastRunAt == nil || !u.LastRunAt.Equal(base) {
		t.Fatalf("Last* not kept: %+v", u)
	}

	// Not found.
	_, err = ss.GetSchedule(c, missingID)
	wantErr(t, "GetSchedule missing", err, ErrNotFound)
	_, err = ss.GetSchedule(c, "bogus")
	wantErr(t, "GetSchedule bogus", err, ErrNotFound)
	_, err = ss.CreateSchedule(c, model.Schedule{ScheduleSpec: model.ScheduleSpec{TestID: missingID, Name: "x", Kind: model.ScheduleOnce}})
	wantErr(t, "CreateSchedule missing test", err, ErrNotFound)
	upd.ID = missingID
	_, err = ss.UpdateSchedule(c, upd)
	wantErr(t, "UpdateSchedule missing", err, ErrNotFound)
	upd.ID = got.ID
	upd.TestID = missingID
	_, err = ss.UpdateSchedule(c, upd)
	wantErr(t, "UpdateSchedule missing test", err, ErrNotFound)
	_, err = ss.SetScheduleResult(c, missingID, ScheduleResult{})
	wantErr(t, "SetScheduleResult missing", err, ErrNotFound)

	if err := ss.DeleteSchedule(c, got.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	wantErr(t, "DeleteSchedule twice", ss.DeleteSchedule(c, got.ID), ErrNotFound)
	_, err = ss.GetSchedule(c, got.ID)
	wantErr(t, "GetSchedule deleted", err, ErrNotFound)
}

func testScheduleAdvance(t *testing.T, s Store) {
	ss := schedStore(t, s)
	c := ctx(t)
	tt := mustTest(t, s, "adv")
	due := base.Add(time.Hour)
	sc, err := ss.CreateSchedule(c, sampleSchedule(tt.ID))
	if err != nil {
		t.Fatal(err)
	}
	next := due.Add(24 * time.Hour)
	a := ScheduleAdvance{NextRunAt: &next, Enabled: true,
		Result: ScheduleResult{At: due, Status: model.ScheduleQueued, Message: "waiting"}}

	// Wrong due time: conflict, nothing changes.
	_, err = ss.AdvanceSchedule(c, sc.ID, due.Add(time.Microsecond), a)
	wantErr(t, "Advance wrong due", err, ErrConflict)

	got, err := ss.AdvanceSchedule(c, sc.ID, due, a)
	if err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(next) || !got.Enabled || got.LastStatus != model.ScheduleQueued ||
		got.LastMessage != "waiting" || got.LastRunAt == nil || !got.LastRunAt.Equal(due) || !got.UpdatedAt.Equal(sc.UpdatedAt) {
		t.Fatalf("advanced = %+v", got)
	}
	// The same occurrence cannot be claimed twice (exactly-once).
	_, err = ss.AdvanceSchedule(c, sc.ID, due, a)
	wantErr(t, "Advance twice", err, ErrConflict)

	// Result update.
	r, err := ss.SetScheduleResult(c, sc.ID, ScheduleResult{RunID: "run-1", Status: model.ScheduleStarted})
	if err != nil {
		t.Fatal(err)
	}
	if r.LastRunID != "run-1" || r.LastStatus != model.ScheduleStarted || r.LastMessage != "" || !r.LastRunAt.Equal(due) {
		t.Fatalf("result = %+v", r)
	}

	// Last occurrence of a once schedule: disable, no next.
	fin, err := ss.AdvanceSchedule(c, sc.ID, next, ScheduleAdvance{Result: ScheduleResult{At: next, Status: model.ScheduleMissed}})
	if err != nil {
		t.Fatal(err)
	}
	if fin.Enabled || fin.NextRunAt != nil || fin.LastStatus != model.ScheduleMissed || fin.LastRunID != "" {
		t.Fatalf("final = %+v", fin)
	}
	// Disabled schedules cannot be advanced.
	_, err = ss.AdvanceSchedule(c, sc.ID, next, a)
	wantErr(t, "Advance disabled", err, ErrConflict)
	_, err = ss.AdvanceSchedule(c, missingID, next, a)
	wantErr(t, "Advance missing", err, ErrNotFound)
}

func testScheduleCascade(t *testing.T, s Store) {
	ss := schedStore(t, s)
	c := ctx(t)
	tt := mustTest(t, s, "casc")
	keep := mustTest(t, s, "keep")
	sc, err := ss.CreateSchedule(c, sampleSchedule(tt.ID))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := ss.CreateSchedule(c, sampleSchedule(keep.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTest(c, tt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.GetSchedule(c, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("schedule survived its test: %v", err)
	}
	all, err := ss.ListSchedules(c, "")
	if err != nil || len(all) != 1 || all[0].ID != kept.ID {
		t.Fatalf("ListSchedules = %+v, %v", all, err)
	}
}

func testRunScheduleID(t *testing.T, s Store) {
	c := ctx(t)
	tt := mustTest(t, s, "runsched")
	r := fullRun(tt.ID)
	r.ScheduleID = "11111111-2222-4333-8444-555555555555"
	got := mustRun(t, s, r)
	if got.ScheduleID != r.ScheduleID {
		t.Fatalf("CreateRun ScheduleID = %q", got.ScheduleID)
	}
	// UpdateRun never changes it.
	got.ScheduleID = ""
	got.Status = model.RunPassed
	if err := s.UpdateRun(c, got); err != nil {
		t.Fatal(err)
	}
	back, err := s.GetRun(c, got.ID)
	if err != nil || back.ScheduleID != r.ScheduleID {
		t.Fatalf("GetRun ScheduleID = %q, %v", back.ScheduleID, err)
	}
	list, err := s.ListRuns(c, RunFilter{TestID: tt.ID})
	if err != nil || len(list) != 1 || list[0].ScheduleID != r.ScheduleID {
		t.Fatalf("ListRuns = %+v, %v", list, err)
	}
	tests, err := s.ListTests(c)
	if err != nil || len(tests) != 1 || tests[0].LastRun == nil || tests[0].LastRun.ScheduleID != r.ScheduleID {
		t.Fatalf("ListTests LastRun = %+v, %v", tests, err)
	}
	plain := mustRun(t, s, fullRun(tt.ID))
	if plain.ScheduleID != "" {
		t.Fatalf("plain run ScheduleID = %q", plain.ScheduleID)
	}
}
