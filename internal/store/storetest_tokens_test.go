package store

import (
	"errors"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func testAPITokens(t *testing.T, s Store) {
	ts, ok := s.(TokenStore)
	if !ok {
		t.Fatalf("%T does not implement TokenStore", s)
	}
	c := ctx(t)

	list, err := ts.ListTokens(c)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty list = %v, %v; want empty non-nil", list, err)
	}
	if _, err := ts.TokenByHash(c, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TokenByHash unknown = %v", err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	exp := base.Add(24 * time.Hour)
	a, err := ts.CreateToken(c, model.APIToken{Name: "ci", Scope: model.ScopeRun, Prefix: "glt_abcd",
		CreatedAt: base, ExpiresAt: &exp}, "hash-a")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !isUUID(a.ID) || a.Name != "ci" || a.Scope != model.ScopeRun || a.Prefix != "glt_abcd" ||
		!a.CreatedAt.Equal(base) || a.LastUsedAt != nil || a.ExpiresAt == nil || !a.ExpiresAt.Equal(exp) {
		t.Fatalf("created = %+v", a)
	}
	b, err := ts.CreateToken(c, model.APIToken{Name: "agent", Scope: model.ScopeRead, Prefix: "glt_wxyz",
		CreatedAt: base.Add(time.Minute)}, "hash-b")
	if err != nil {
		t.Fatalf("CreateToken b: %v", err)
	}
	if b.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil", b.ExpiresAt)
	}

	if _, err := ts.CreateToken(c, model.APIToken{Name: "ci", Scope: model.ScopeRead, Prefix: "x"}, "hash-c"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name = %v, want ErrConflict", err)
	}

	list, err = ts.ListTokens(c)
	if err != nil || len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("list = %+v, %v; want newest first", list, err)
	}

	got, err := ts.TokenByHash(c, "hash-a")
	if err != nil || got.ID != a.ID || got.Name != "ci" {
		t.Fatalf("TokenByHash = %+v, %v", got, err)
	}

	at := base.Add(time.Hour)
	if err := ts.TouchToken(c, a.ID, at); err != nil {
		t.Fatalf("TouchToken: %v", err)
	}
	got, _ = ts.TokenByHash(c, "hash-a")
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
		t.Fatalf("last_used_at = %v, want %v", got.LastUsedAt, at)
	}
	if err := ts.TouchToken(c, missingID, at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Touch missing = %v", err)
	}

	if err := ts.DeleteToken(c, a.ID); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if _, err := ts.TokenByHash(c, "hash-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
	if err := ts.DeleteToken(c, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if err := ts.DeleteToken(c, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete bad id = %v", err)
	}
	// The freed name can be reused.
	if _, err := ts.CreateToken(c, model.APIToken{Name: "ci", Scope: model.ScopeAdmin, Prefix: "glt_new1"}, "hash-d"); err != nil {
		t.Fatalf("reuse name: %v", err)
	}
}

func testStartedBy(t *testing.T, s Store) {
	c := ctx(t)
	tst := mustTest(t, s, "started-by")
	run, err := s.CreateRun(c, model.Run{TestID: tst.ID, Status: model.RunRunning, StartedBy: "token:ci"})
	if err != nil || run.StartedBy != "token:ci" {
		t.Fatalf("CreateRun = %+v, %v", run, err)
	}
	// UpdateRun must keep started_by.
	run.Status = model.RunPassed
	run.StartedBy = "other"
	if err := s.UpdateRun(c, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(c, run.ID)
	if err != nil || got.StartedBy != "token:ci" {
		t.Fatalf("GetRun = %+v, %v; want started_by kept", got, err)
	}
	list, _ := s.ListRuns(c, RunFilter{})
	if len(list) != 1 || list[0].StartedBy != "token:ci" {
		t.Fatalf("ListRuns = %+v", list)
	}
	tests, _ := s.ListTests(c)
	if len(tests) != 1 || tests[0].LastRun == nil || tests[0].LastRun.StartedBy != "token:ci" {
		t.Fatalf("ListTests last run = %+v", tests)
	}

	ss := s.(SuiteStore)
	su, err := ss.CreateSuite(c, model.Suite{Name: "sb", Mode: model.SuiteSequential,
		Items: []model.SuiteItem{{TestID: tst.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	sr, err := ss.CreateSuiteRun(c, model.SuiteRun{SuiteID: su.ID, SuiteName: "sb", Status: model.RunRunning,
		Mode: model.SuiteSequential, StartedAt: time.Now(), StartedBy: "schedule:nightly"})
	if err != nil || sr.StartedBy != "schedule:nightly" {
		t.Fatalf("CreateSuiteRun = %+v, %v", sr, err)
	}
	got2, err := ss.GetSuiteRun(c, sr.ID)
	if err != nil || got2.StartedBy != "schedule:nightly" {
		t.Fatalf("GetSuiteRun = %+v, %v", got2, err)
	}
}
