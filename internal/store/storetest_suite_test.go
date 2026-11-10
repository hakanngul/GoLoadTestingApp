package store

import (
	"errors"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func suiteStore(t *testing.T, s Store) SuiteStore {
	t.Helper()
	ss, ok := s.(SuiteStore)
	if !ok {
		t.Fatalf("%T does not implement SuiteStore", s)
	}
	return ss
}

func testSuiteCRUD(t *testing.T, s Store) {
	ss := suiteStore(t, s)
	c := ctx(t)
	a := mustTest(t, s, "suite-a")
	b := mustTest(t, s, "suite-b")

	in := model.Suite{ID: "ignored", Name: "smoke", Description: "d", Mode: model.SuiteSequential,
		StopOnFailure: true, GapMs: 5000,
		Items: []model.SuiteItem{{TestID: a.ID, TestName: "ignored", Workers: 2}, {TestID: b.ID}, {TestID: a.ID}}}
	got, err := ss.CreateSuite(c, in)
	if err != nil {
		t.Fatalf("CreateSuite: %v", err)
	}
	if !isUUID(got.ID) || !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("created = %+v", got)
	}
	want := []model.SuiteItem{{TestID: a.ID, Workers: 2}, {TestID: b.ID}, {TestID: a.ID}}
	jsonEq(t, "items", got.Items, want)
	if got.Name != "smoke" || got.Description != "d" || got.Mode != model.SuiteSequential ||
		!got.StopOnFailure || got.GapMs != 5000 {
		t.Fatalf("created = %+v", got)
	}
	read, err := ss.GetSuite(c, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "get", read, got)

	if _, err := ss.CreateSuite(c, model.Suite{Name: "smoke", Mode: model.SuiteParallel}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	other, err := ss.CreateSuite(c, model.Suite{Name: "another", Mode: model.SuiteParallel})
	if err != nil {
		t.Fatal(err)
	}
	if other.Items == nil {
		t.Fatal("items nil, want empty")
	}

	time.Sleep(2 * time.Millisecond)
	got.Name, got.Mode, got.StopOnFailure, got.GapMs = "smoke2", model.SuiteParallel, false, 0
	got.Items = []model.SuiteItem{{TestID: b.ID, Workers: 3}}
	up, err := ss.UpdateSuite(c, got)
	if err != nil {
		t.Fatalf("UpdateSuite: %v", err)
	}
	if up.Name != "smoke2" || up.Mode != model.SuiteParallel || up.StopOnFailure || len(up.Items) != 1 ||
		!up.CreatedAt.Equal(got.CreatedAt) || !up.UpdatedAt.After(got.UpdatedAt) {
		t.Fatalf("updated = %+v", up)
	}
	up.Name = "another"
	if _, err := ss.UpdateSuite(c, up); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename to taken name: %v", err)
	}
	if _, err := ss.UpdateSuite(c, model.Suite{ID: missingID, Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	list, err := ss.ListSuites(c)
	if err != nil || len(list) != 2 || list[0].Name != "another" || list[1].Name != "smoke2" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Deleting a test keeps the item (the caller marks it deleted).
	if err := s.DeleteTest(c, b.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := ss.GetSuite(c, up.ID); len(r.Items) != 1 || r.Items[0].TestID != b.ID {
		t.Fatalf("items after test delete = %+v", r.Items)
	}

	if err := ss.DeleteSuite(c, up.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.GetSuite(c, up.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	for _, id := range []string{up.ID, missingID, "nope"} {
		if err := ss.DeleteSuite(c, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete %q: %v", id, err)
		}
		if _, err := ss.GetSuite(c, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get %q: %v", id, err)
		}
	}
}

func testSuiteRuns(t *testing.T, s Store) {
	ss := suiteStore(t, s)
	c := ctx(t)
	a := mustTest(t, s, "suite-run-a")
	su, err := ss.CreateSuite(c, model.Suite{Name: "s1", Mode: model.SuiteSequential,
		Items: []model.SuiteItem{{TestID: a.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	su2, err := ss.CreateSuite(c, model.Suite{Name: "s2", Mode: model.SuiteParallel})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ss.CreateSuiteRun(c, model.SuiteRun{SuiteID: missingID, StartedAt: base}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("run of missing suite: %v", err)
	}

	r1, err := ss.CreateSuiteRun(c, model.SuiteRun{SuiteID: su.ID, SuiteName: "s1", Status: model.RunRunning,
		Mode: model.SuiteSequential, StopOnFailure: true, GapMs: 100, Label: "v1", StartedAt: base,
		Items: []model.SuiteRunItem{{TestID: a.ID, TestName: "suite-run-a", Status: model.SuiteItemPending}}})
	if err != nil {
		t.Fatalf("CreateSuiteRun: %v", err)
	}
	if !isUUID(r1.ID) || r1.Label != "v1" || !r1.StopOnFailure || r1.GapMs != 100 || len(r1.Items) != 1 ||
		!r1.StartedAt.Equal(base) || r1.FinishedAt != nil {
		t.Fatalf("created = %+v", r1)
	}
	r2, err := ss.CreateSuiteRun(c, model.SuiteRun{SuiteID: su.ID, SuiteName: "s1", Status: model.RunPassed,
		Mode: model.SuiteSequential, StartedAt: base.Add(time.Minute), FinishedAt: tp(base.Add(2 * time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Items == nil {
		t.Fatal("items nil, want empty")
	}
	r3, err := ss.CreateSuiteRun(c, model.SuiteRun{SuiteID: su2.ID, SuiteName: "s2", Status: model.RunQueued,
		Mode: model.SuiteParallel, StartedAt: base.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}

	fin := base.Add(time.Hour)
	r1.Status, r1.FinishedAt, r1.Error = model.RunFailed, &fin, "boom"
	r1.Items[0].Status = string(model.RunFailed)
	r1.Items[0].RunID = "run-1"
	r1.Items[0].Summary = &model.Summary{Requests: 10, P95Ms: 12.5}
	r1.Label = "ignored"
	if err := ss.UpdateSuiteRun(c, r1); err != nil {
		t.Fatalf("UpdateSuiteRun: %v", err)
	}
	got, err := ss.GetSuiteRun(c, r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	r1.Label = "v1"
	jsonEq(t, "updated run", got, r1)
	if err := ss.UpdateSuiteRun(c, model.SuiteRun{ID: missingID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	ids := func(l []model.SuiteRun) []string {
		out := []string{}
		for _, r := range l {
			out = append(out, r.ID)
		}
		return out
	}
	all, err := ss.ListSuiteRuns(c, SuiteRunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, "all", ids(all), []string{r3.ID, r2.ID, r1.ID})
	one, _ := ss.ListSuiteRuns(c, SuiteRunFilter{SuiteID: su.ID, Limit: 1})
	jsonEq(t, "suite limit 1", ids(one), []string{r2.ID})
	active, _ := ss.ListSuiteRuns(c, SuiteRunFilter{Active: true})
	jsonEq(t, "active", ids(active), []string{r3.ID})
	none, _ := ss.ListSuiteRuns(c, SuiteRunFilter{SuiteID: "nope"})
	if none == nil || len(none) != 0 {
		t.Fatalf("bad suite id = %+v", none)
	}

	if err := ss.DeleteSuite(c, su.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.GetSuiteRun(c, r1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("suite run after suite delete: %v", err)
	}
	if _, err := ss.GetSuiteRun(c, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get bad id: %v", err)
	}
}
