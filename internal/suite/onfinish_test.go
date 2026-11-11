package suite

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// hookFixture is a fixture whose Service reports finished suite runs to rec.
func hookFixture(t *testing.T, hold time.Duration, rec *hookRecorder, names ...string) *fixture {
	t.Helper()
	f := newFixture(t, hold, names...)
	_ = f.svc.Shutdown(context.Background())
	f.svc = New(Options{Store: f.st, Runner: f.fr, Poll: 5 * time.Millisecond, OnFinish: rec.hook(f.st)})
	t.Cleanup(func() { _ = f.svc.Shutdown(context.Background()) })
	return f
}

type hookRecorder struct {
	mu        sync.Mutex
	got       []model.SuiteRun
	persisted []model.RunStatus
}

func (h *hookRecorder) hook(st interface {
	GetSuiteRun(context.Context, string) (model.SuiteRun, error)
}) func(model.SuiteRun) {
	return func(r model.SuiteRun) {
		stored, _ := st.GetSuiteRun(context.Background(), r.ID)
		h.mu.Lock()
		h.got = append(h.got, r)
		h.persisted = append(h.persisted, stored.Status)
		h.mu.Unlock()
	}
}

func (h *hookRecorder) runs() []model.SuiteRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.SuiteRun(nil), h.got...)
}

func TestOnFinishCalledOncePerSuiteRun(t *testing.T) {
	var rec hookRecorder
	f := hookFixture(t, 5*time.Millisecond, &rec, "a", "b")
	f.fr.outcome["b"] = model.RunFailed
	su := f.suite(t, model.SuiteRequest{}, "a", "b")

	r, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.waitDone(t, r.ID)
	time.Sleep(100 * time.Millisecond) // a duplicate call would land by now

	got := rec.runs()
	if len(got) != 1 {
		t.Fatalf("OnFinish called %d times, want 1", len(got))
	}
	if got[0].ID != r.ID || got[0].Status != model.RunFailed || got[0].FinishedAt == nil || len(got[0].Items) != 2 {
		t.Fatalf("hook run = %+v", got[0])
	}
	if rec.persisted[0] != model.RunFailed {
		t.Fatalf("stored status when hook ran = %s, want failed", rec.persisted[0])
	}
}

func TestOnFinishCalledForStoppedSuiteRun(t *testing.T) {
	var rec hookRecorder
	f := hookFixture(t, 5*time.Second, &rec, "a", "b")
	su := f.suite(t, model.SuiteRequest{}, "a", "b")
	r, err := f.svc.Start(context.Background(), su.ID, model.StartSuiteRequest{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.fr.mu.Lock()
		n := len(f.fr.started)
		f.fr.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := f.svc.Stop(r.ID); err != nil {
		t.Fatal(err)
	}
	f.waitDone(t, r.ID)
	time.Sleep(100 * time.Millisecond)
	got := rec.runs()
	if len(got) != 1 || got[0].Status != model.RunStopped {
		t.Fatalf("hook calls = %+v", got)
	}
}
