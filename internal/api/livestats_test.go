package api

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
)

func TestStreamStatsFrame(t *testing.T) {
	e, ts, handlerDone := startStreamEnv(t, time.Hour)
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunRunning})
	events, cancelled := e.runner.activate(run.ID)

	resp := openStream(t, context.Background(), ts.URL+"/api/runs/"+run.ID+"/stream")
	br := bufio.NewReader(resp.Body)
	ls := &model.LiveStats{
		Elapsed: 3,
		Total: model.Summary{Requests: 30, Errors: 3, ByStep: map[string]model.Summary{
			"home": {Requests: 30, Errors: 3, P95Ms: 12.5},
		}},
		Current: model.Summary{Requests: 10, ByStep: map[string]model.Summary{"home": {Requests: 10}}},
		Errors:  []model.ErrorGroup{{Step: "home", Status: 503, Message: "status 503", Count: 3}},
	}
	events <- runner.Event{Type: runner.EventStats, Stats: ls}
	f, err := readFrame(br)
	if err != nil || f.event != "stats" {
		t.Fatalf("frame = %+v, %v", f, err)
	}
	var got model.LiveStats
	if err := json.Unmarshal([]byte(f.data), &got); err != nil {
		t.Fatal(err)
	}
	if got.Elapsed != 3 || got.Total.ByStep["home"].P95Ms != 12.5 || got.Current.Requests != 10 ||
		len(got.Errors) != 1 || got.Errors[0].Count != 3 || got.Errors[0].Status != 503 {
		t.Fatalf("stats data %s", f.data)
	}
	for _, key := range []string{`"total"`, `"current"`, `"errors"`, `"by_step"`, `"first"`, `"last"`, `"message"`} {
		if !strings.Contains(f.data, key) {
			t.Fatalf("stats JSON lacks %s: %s", key, f.data)
		}
	}
	close(events)
	waitClosed(t, handlerDone, "handler exit")
	waitClosed(t, cancelled, "subscription cancel")
}

func TestErrorGroupsEndpoint(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Active run: live groups from the runner.
	active, _ := e.store.CreateRun(ctx, model.Run{Status: model.RunRunning})
	e.runner.activate(active.ID)
	e.runner.mu.Lock()
	e.runner.groups = map[string][]model.ErrorGroup{active.ID: {{Step: "a", Status: 500, Message: "live", Count: 7}}}
	e.runner.mu.Unlock()
	rec := e.do(t, "GET", "/api/runs/"+active.ID+"/error-groups", nil)
	wantStatus(t, rec, 200)
	if got := decode[[]model.ErrorGroup](t, rec); len(got) != 1 || got[0].Message != "live" || got[0].Count != 7 {
		t.Fatalf("live groups = %+v", got)
	}

	// Finished run: the stored groups.
	fin, _ := e.store.CreateRun(ctx, model.Run{Status: model.RunFailed})
	stored := []model.ErrorGroup{
		{Step: "b", Status: 503, Message: "stored", Count: 5, First: time.Unix(10, 0).UTC(), Last: time.Unix(20, 0).UTC()},
		{Message: model.ErrorGroupOther, Count: 1},
	}
	if err := e.store.SetErrorGroups(ctx, fin.ID, stored); err != nil {
		t.Fatal(err)
	}
	rec = e.do(t, "GET", "/api/runs/"+fin.ID+"/error-groups", nil)
	wantStatus(t, rec, 200)
	if got := decode[[]model.ErrorGroup](t, rec); len(got) != 2 || got[0].Count != 5 || got[1].Message != model.ErrorGroupOther {
		t.Fatalf("stored groups = %+v", got)
	}

	// Finished run without failures: [] (not null).
	clean, _ := e.store.CreateRun(ctx, model.Run{Status: model.RunPassed})
	rec = e.do(t, "GET", "/api/runs/"+clean.ID+"/error-groups", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty groups = %s", rec.Body.String())
	}

	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/error-groups", nil), 404, "not found")
}
