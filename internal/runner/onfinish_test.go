package runner

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// finishRecorder collects OnFinish calls and checks, at call time, that the
// run was already persisted with the same final status.
type finishRecorder struct {
	st *fakeStore

	mu        sync.Mutex
	got       []model.Run
	persisted []model.RunStatus
}

func (f *finishRecorder) hook(r model.Run) {
	stored, _ := f.st.GetRun(context.Background(), r.ID)
	f.mu.Lock()
	f.got = append(f.got, r)
	f.persisted = append(f.persisted, stored.Status)
	f.mu.Unlock()
}

func (f *finishRecorder) runs() []model.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Run(nil), f.got...)
}

func TestOnFinishCalledOncePerLocalRun(t *testing.T) {
	st := newFakeStore()
	rec := &finishRecorder{st: st}
	m := newManager(t, st, func(o *Options) { o.OnFinish = rec.hook })
	srv := target(t, http.StatusOK)

	passed, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "1s", 10, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, st, passed.ID, 10*time.Second)

	stopped, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "30s", 10, "/", "")), model.StartRunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitMetrics(t, st, stopped.ID, 1, 5*time.Second)
	if err := m.Stop(stopped.ID); err != nil {
		t.Fatal(err)
	}
	waitFinished(t, st, stopped.ID, 5*time.Second)

	// Let any (wrongly) duplicated call land before counting.
	time.Sleep(200 * time.Millisecond)
	want := map[string]model.RunStatus{passed.ID: model.RunPassed, stopped.ID: model.RunStopped}
	got := rec.runs()
	if len(got) != 2 {
		t.Fatalf("OnFinish called %d times, want 2", len(got))
	}
	for i, r := range got {
		if want[r.ID] != r.Status {
			t.Errorf("run %s hook status = %s, want %s", r.ID, r.Status, want[r.ID])
		}
		if rec.persisted[i] != r.Status {
			t.Errorf("run %s was %s in the store when the hook ran", r.ID, rec.persisted[i])
		}
		if r.FinishedAt == nil {
			t.Errorf("run %s has no FinishedAt", r.ID)
		}
		delete(want, r.ID)
	}
	if len(want) != 0 {
		t.Errorf("no hook call for %v", want)
	}
}

func TestOnFinishCalledForRemoteRun(t *testing.T) {
	srv := target(t, http.StatusOK)
	c := newTestCluster(t)
	c.addWorker("w1")
	c.waitIdle(1)

	st := newFakeStore()
	rec := &finishRecorder{st: st}
	m := newManager(t, st, func(o *Options) { o.Dispatcher = c.master; o.OnFinish = rec.hook })
	run, err := m.Start(context.Background(), testFor(scenarioYAML(srv.URL, "1s", 20, "/", "")), model.StartRunRequest{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitFinished(t, st, run.ID, 20*time.Second)
	time.Sleep(200 * time.Millisecond)
	got := rec.runs()
	if len(got) != 1 || got[0].ID != run.ID || got[0].Status != model.RunPassed || rec.persisted[0] != model.RunPassed {
		t.Fatalf("hook calls = %+v (persisted %v)", got, rec.persisted)
	}
}
