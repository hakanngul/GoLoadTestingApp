package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/metrics"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

const rampYAML = `# <script>alert("yaml")</script>
name: ramp
target:
  base_url: https://api.example.test
load:
  stages:
    - duration: 5s
      target: 10
    - duration: 10s
      target: 10
    - duration: 5s
      target: 0
steps:
  - name: home
    path: /
thresholds:
  - metric: latency.p95
    op: lt
    value: 500
`

// newMemEnv is an API environment backed by the real in-memory store.
func newMemEnv(t *testing.T) (*env, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	rn := newFakeRunner(st)
	s := newServer(Config{Store: st, Runner: rn, Version: "test"})
	return &env{h: s.handler(), srv: s, runner: rn}, st
}

var rt0 = time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

// seedRun stores a finished 20 s run: ramp seconds at 900 ms, steady
// seconds at 10..50 ms (times scale), one error sample.
func seedRun(t *testing.T, st store.Store, testID string, scale float64, startOffset time.Duration) model.Run {
	t.Helper()
	ctx := context.Background()
	start := rt0.Add(startOffset)
	run, err := st.CreateRun(ctx, model.Run{TestID: testID, Status: model.RunRunning, YAML: rampYAML, StartedAt: start,
		Notes: `<script>alert("notes")</script>`, Label: `<b>v1</b>`})
	if err != nil {
		t.Fatal(err)
	}
	col := metrics.NewCollector(start)
	for k := 1; k <= 20; k++ {
		for i := 1; i <= 5; i++ {
			lat := time.Duration(float64(i*10)*scale) * time.Millisecond
			if k <= 5 || k > 15 {
				lat = 900 * time.Millisecond
			}
			col.Record(protocol.Result{Step: "home", Status: 200, Latency: lat})
		}
		end := start.Add(time.Duration(k) * time.Second)
		snap := col.Flush(end)
		blob, _ := snap.MarshalBinary()
		s := metrics.Summarize(snap)
		p := model.MetricPoint{T: end, Elapsed: float64(k), TargetRate: 10, Requests: s.Requests, RPS: s.RPS,
			P50Ms: float64(s.P50) / 1e6, P95Ms: float64(s.P95) / 1e6, P99Ms: float64(s.P99) / 1e6, Workers: 1}
		if err := st.AppendMetric(ctx, run.ID, p, blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AppendErrorSamples(ctx, run.ID, []model.ErrorSample{{T: start, Step: "home", Status: 500,
		Message: `<script>alert("err")</script> & "quoted"`}}); err != nil {
		t.Fatal(err)
	}
	fin := start.Add(20 * time.Second)
	run.Status, run.FinishedAt = model.RunPassed, &fin
	run.Summary = &model.Summary{Requests: 100}
	run.Warnings = []string{`<i>warn</i>`}
	if err := st.UpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run, _ = st.GetRun(ctx, run.ID)
	return run
}

func memTest(t *testing.T, st store.Store, name string) model.Test {
	t.Helper()
	tt, err := st.CreateTest(context.Background(), model.Test{Name: name, YAML: rampYAML})
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestStartRunLabelNotes(t *testing.T) {
	e := newEnv(t)
	tt := e.createTest(t, "lbl")
	rec := e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", map[string]any{"label": "  v1.4.2 ", "notes": "deploy <x>"})
	wantStatus(t, rec, http.StatusCreated)
	run := decode[model.Run](t, rec)
	if run.Label != "v1.4.2" || run.Notes != "deploy <x>" {
		t.Fatalf("run = %+v", run)
	}
	if e.runner.lastReq.Label != "v1.4.2" {
		t.Fatalf("runner got label %q", e.runner.lastReq.Label)
	}
	wantAPIError(t, e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", map[string]any{"label": strings.Repeat("x", 101)}), 400, "label")
	wantAPIError(t, e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", map[string]any{"notes": strings.Repeat("x", 10001)}), 400, "notes")
	// Exactly at the limits is fine (runes, not bytes).
	rec = e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", map[string]any{"label": strings.Repeat("é", 100), "notes": strings.Repeat("x", 10000)})
	wantStatus(t, rec, http.StatusCreated)
}

func TestUpdateRunMetaAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "meta")
	run := seedRun(t, st, tt.ID, 1, 0)
	path := "/api/runs/" + run.ID

	rec := e.json(t, "PATCH", path, map[string]any{"label": "  rc-2 "})
	wantStatus(t, rec, 200)
	got := decode[model.Run](t, rec)
	if got.Label != "rc-2" || got.Notes != run.Notes || got.Thresholds == nil {
		t.Fatalf("PATCH label = %+v", got)
	}
	rec = e.json(t, "PATCH", path, map[string]any{"notes": ""})
	wantStatus(t, rec, 200)
	if got = decode[model.Run](t, rec); got.Label != "rc-2" || got.Notes != "" {
		t.Fatalf("PATCH notes = %+v", got)
	}
	rec = e.json(t, "PATCH", path, map[string]any{"pinned": true})
	wantStatus(t, rec, 200)
	if got = decode[model.Run](t, rec); !got.Pinned || got.Label != "rc-2" {
		t.Fatalf("PATCH pinned = %+v", got)
	}
	rec = e.json(t, "PATCH", path, map[string]any{"pinned": false})
	wantStatus(t, rec, 200)
	if got = decode[model.Run](t, rec); got.Pinned {
		t.Fatalf("PATCH unpin = %+v", got)
	}
	rec = e.do(t, "GET", path, nil)
	if got = decode[model.Run](t, rec); got.Label != "rc-2" {
		t.Fatalf("GET after PATCH = %+v", got)
	}

	wantAPIError(t, e.json(t, "PATCH", path, map[string]any{"label": strings.Repeat("x", 101)}), 400, "label")
	wantAPIError(t, e.json(t, "PATCH", path, map[string]any{"notes": strings.Repeat("x", 10001)}), 400, "notes")
	wantAPIError(t, e.json(t, "PATCH", path, map[string]any{"status": "passed"}), 400, "unknown field")
	wantAPIError(t, e.json(t, "PATCH", path, nil), 400, "empty")
	wantAPIError(t, e.json(t, "PATCH", "/api/runs/nope", map[string]any{"label": "x"}), 404, "not found")
}

func TestDeleteRunAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "del")
	run := seedRun(t, st, tt.ID, 1, 0)
	if _, err := st.SetBaseline(context.Background(), tt.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	// Active runs cannot be deleted.
	e.runner.mu.Lock()
	e.runner.active[run.ID] = true
	e.runner.mu.Unlock()
	wantAPIError(t, e.do(t, "DELETE", "/api/runs/"+run.ID, nil), 409, "active")
	e.runner.mu.Lock()
	delete(e.runner.active, run.ID)
	e.runner.mu.Unlock()

	rec := e.do(t, "DELETE", "/api/runs/"+run.ID, nil)
	wantStatus(t, rec, 204)
	wantAPIError(t, e.do(t, "GET", "/api/runs/"+run.ID, nil), 404, "not found")
	wantAPIError(t, e.do(t, "DELETE", "/api/runs/"+run.ID, nil), 404, "not found")
	got := decode[model.Test](t, e.do(t, "GET", "/api/tests/"+tt.ID, nil))
	if got.BaselineRunID != "" {
		t.Fatalf("baseline not cleared: %+v", got)
	}
}

func TestRunReportAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "rep")
	base := seedRun(t, st, tt.ID, 1, 0)
	run := seedRun(t, st, tt.ID, 2, time.Hour)
	path := "/api/runs/" + run.ID + "/report"

	rec := e.do(t, "GET", path, nil)
	wantStatus(t, rec, 200)
	rep := decode[model.RunReport](t, rec)
	if rep.Window != (model.Window{Kind: model.WindowSteady, From: 5, To: 15}) || rep.Summary.Requests != 50 ||
		rep.Summary.RPS != 5 || rep.Summary.MaxMs > 101 || rep.Baseline != nil || rep.Run.ID != run.ID {
		t.Fatalf("steady report = %+v", rep)
	}
	if len(rep.Thresholds) != 1 || !rep.Thresholds[0].Passed {
		t.Fatalf("thresholds = %+v", rep.Thresholds)
	}
	rep = decode[model.RunReport](t, e.do(t, "GET", path+"?window=full", nil))
	if rep.Window.Kind != model.WindowFull || rep.Summary.Requests != 100 || rep.Thresholds[0].Passed {
		t.Fatalf("full report = %+v", rep)
	}
	rep = decode[model.RunReport](t, e.do(t, "GET", path+"?window=custom&from=0&to=5", nil))
	if rep.Window != (model.Window{Kind: model.WindowCustom, From: 0, To: 5}) || rep.Summary.Requests != 25 || rep.Summary.P50Ms < 899 {
		t.Fatalf("custom report = %+v", rep)
	}

	// Baseline comparison once the test has one.
	wantStatus(t, e.json(t, "PUT", "/api/tests/"+tt.ID+"/baseline", map[string]string{"run_id": base.ID}), 200)
	rep = decode[model.RunReport](t, e.do(t, "GET", path+"?tolerance=0.5", nil))
	if rep.Baseline == nil || rep.Baseline.ReferenceRunID != base.ID || rep.Baseline.Tolerance != 0.5 || !rep.Baseline.Regression {
		t.Fatalf("baseline = %+v", rep.Baseline)
	}

	for _, q := range []string{"?window=bogus", "?window=custom", "?window=custom&from=1", "?window=custom&from=5&to=5",
		"?window=custom&from=-1&to=5", "?window=custom&from=a&to=5", "?window=custom&from=0&to=NaN", "?tolerance=2", "?tolerance=x", "?tolerance=-0.1"} {
		rec := e.do(t, "GET", path+q, nil)
		wantStatus(t, rec, 400)
	}
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/report", nil), 404, "not found")
}

func TestReportHTMLAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, `<script>alert("name")</script> Shop/API`)
	base := seedRun(t, st, tt.ID, 1, 0)
	run := seedRun(t, st, tt.ID, 2, time.Hour)
	if _, err := st.SetBaseline(context.Background(), tt.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, "GET", "/api/runs/"+run.ID+"/report.html?window=full", nil)
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	re := regexp.MustCompile(`^attachment; filename="goload-[A-Za-z0-9._-]+-2026-04-05-` + run.ID[:8] + `\.html"$`)
	if !re.MatchString(cd) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(cd, "script-alert-name-script-Shop-API") {
		t.Fatalf("file name slug = %q", cd)
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Fatalf("unescaped script tag in report:\n%s", body)
	}
	for _, want := range []string{
		`&lt;script&gt;alert(&#34;name&#34;)&lt;/script&gt;`,                       // test name
		`&lt;script&gt;alert(&#34;notes&#34;)&lt;/script&gt;`,                      // notes
		`&lt;script&gt;alert(&#34;err&#34;)&lt;/script&gt; &amp; &#34;quoted&#34;`, // error sample
		`# &lt;script&gt;alert(&#34;yaml&#34;)&lt;/script&gt;`,                     // scenario YAML
		`&lt;b&gt;v1&lt;/b&gt;`, `&lt;i&gt;warn&lt;/i&gt;`,
		"<svg", "<polyline", "Throughput (requests/s)", "Latency (ms)", "@media print",
		"Baseline comparison", base.ID, "Regression", "Per step", "home", "latency.p95",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report does not contain %q", want)
		}
	}
	// No external assets: the only URLs are the SVG namespace and the
	// scenario's base_url (inside the YAML <pre>).
	stripped := strings.NewReplacer("http://www.w3.org/2000/svg", "", "https://api.example.test", "").Replace(body)
	for _, bad := range []string{"http:", "https:", "<link", "<img", "@import", "src="} {
		if strings.Contains(stripped, bad) {
			t.Errorf("report references external asset %q", bad)
		}
	}
	wantStatus(t, e.do(t, "GET", "/api/runs/"+run.ID+"/report.html?window=bogus", nil), 400)
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/report.html", nil), 404, "not found")

	// A run without metric points still renders.
	empty, _ := st.CreateRun(context.Background(), model.Run{TestID: tt.ID, Status: model.RunError, Error: "<boom>"})
	rec = e.do(t, "GET", "/api/runs/"+empty.ID+"/report.html", nil)
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "No metric points recorded.") || !strings.Contains(rec.Body.String(), "&lt;boom&gt;") {
		t.Fatalf("empty report body:\n%s", rec.Body.String())
	}
}

func TestExportAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "exp")
	run := seedRun(t, st, tt.ID, 1, 0)

	rec := e.do(t, "GET", "/api/runs/"+run.ID+"/export.csv", nil)
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="goload-exp-2026-04-05-`+run.ID[:8]+`.csv"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rows[0], ","); got != "t,elapsed,target,active,requests,errors,dropped,rps,error_rate,mean_ms,p50_ms,p95_ms,p99_ms,max_ms,cpu,workers" {
		t.Fatalf("header = %q", got)
	}
	if len(rows) != 21 {
		t.Fatalf("rows = %d, want 1 header + 20 points", len(rows))
	}
	if rows[1][1] != "1" || rows[1][4] != "5" || rows[20][1] != "20" || rows[1][15] != "1" || rows[1][0] != "2026-04-05T06:07:09Z" {
		t.Fatalf("first/last rows = %v / %v", rows[1], rows[20])
	}
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/export.csv", nil), 404, "not found")

	rec = e.do(t, "GET", "/api/runs/"+run.ID+"/export.json", nil)
	wantStatus(t, rec, 200)
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.json"`) || !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	exp := decode[runExport](t, rec)
	if exp.Run.ID != run.ID || len(exp.Metrics) != 20 || len(exp.Errors) != 1 {
		t.Fatalf("export.json = run %s, %d metrics, %d errors", exp.Run.ID, len(exp.Metrics), len(exp.Errors))
	}
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/export.json", nil), 404, "not found")
}

func TestCompareAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "cmp")
	a := seedRun(t, st, tt.ID, 1, 0)
	b := seedRun(t, st, tt.ID, 2, time.Hour)
	c := seedRun(t, st, tt.ID, 1, 2*time.Hour)

	rec := e.do(t, "GET", fmt.Sprintf("/api/compare?runs=%s,%s,%s&window=steady&tolerance=0.2", a.ID, b.ID, c.ID), nil)
	wantStatus(t, rec, 200)
	res := decode[model.CompareResult](t, rec)
	if len(res.Runs) != 3 || len(res.Comparisons) != 2 || res.Runs[0].Run.ID != a.ID {
		t.Fatalf("compare = %+v", res)
	}
	if !res.Comparisons[0].Regression || res.Comparisons[0].CandidateRunID != b.ID || res.Comparisons[1].Regression ||
		res.Comparisons[0].Tolerance != 0.2 {
		t.Fatalf("comparisons = %+v", res.Comparisons)
	}

	ids := []string{a.ID, b.ID, c.ID, a.ID + "x", "x", "y", "z"}
	for _, q := range []string{
		"", "?runs=" + a.ID, "?runs=" + strings.Join(ids, ","), "?runs=" + a.ID + "," + a.ID,
		"?runs=" + a.ID + "," + b.ID + "&window=bad", "?runs=" + a.ID + "," + b.ID + "&tolerance=9",
	} {
		wantStatus(t, e.do(t, "GET", "/api/compare"+q, nil), 400)
	}
	wantAPIError(t, e.do(t, "GET", "/api/compare?runs="+a.ID+",missing", nil), 404, "not found")
}

func TestTrendAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "trend")
	a := seedRun(t, st, tt.ID, 1, 0)
	b := seedRun(t, st, tt.ID, 2, time.Hour)
	if _, err := st.CreateRun(context.Background(), model.Run{TestID: tt.ID, Status: model.RunRunning, StartedAt: rt0.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetBaseline(context.Background(), tt.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, "GET", "/api/tests/"+tt.ID+"/trend", nil)
	wantStatus(t, rec, 200)
	pts := decode[[]model.TrendPoint](t, rec)
	if len(pts) != 2 || pts[0].RunID != a.ID || pts[1].RunID != b.ID || !pts[0].IsBaseline || pts[1].IsBaseline ||
		pts[0].Summary.Requests != 50 || pts[0].Summary.ByStep != nil || pts[0].Label != "<b>v1</b>" {
		t.Fatalf("trend = %+v", pts)
	}
	pts = decode[[]model.TrendPoint](t, e.do(t, "GET", "/api/tests/"+tt.ID+"/trend?limit=1&window=full", nil))
	if len(pts) != 1 || pts[0].RunID != b.ID || pts[0].Summary.Requests != 100 {
		t.Fatalf("trend limit 1 = %+v", pts)
	}
	wantStatus(t, e.do(t, "GET", "/api/tests/"+tt.ID+"/trend?limit=100000", nil), 200) // clamped
	for _, q := range []string{"?limit=0", "?limit=x", "?window=nope"} {
		wantStatus(t, e.do(t, "GET", "/api/tests/"+tt.ID+"/trend"+q, nil), 400)
	}
	wantAPIError(t, e.do(t, "GET", "/api/tests/missing/trend", nil), 404, "not found")
	// A test without runs has an empty trend.
	empty := memTest(t, st, "empty")
	rec = e.do(t, "GET", "/api/tests/"+empty.ID+"/trend", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty trend = %s", rec.Body.String())
	}
}

func TestBaselineAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "bl")
	other := memTest(t, st, "other")
	run := seedRun(t, st, tt.ID, 1, 0)
	foreign := seedRun(t, st, other.ID, 1, 0)
	active, _ := st.CreateRun(context.Background(), model.Run{TestID: tt.ID, Status: model.RunRunning})
	path := "/api/tests/" + tt.ID + "/baseline"

	rec := e.json(t, "PUT", path, map[string]string{"run_id": run.ID})
	wantStatus(t, rec, 200)
	got := decode[model.Test](t, rec)
	if got.ID != tt.ID || got.BaselineRunID != run.ID || got.Files == nil {
		t.Fatalf("PUT baseline = %+v", got)
	}
	if got = decode[model.Test](t, e.do(t, "GET", "/api/tests/"+tt.ID, nil)); got.BaselineRunID != run.ID {
		t.Fatalf("GET test = %+v", got)
	}
	if !strings.Contains(e.do(t, "GET", "/api/tests", nil).Body.String(), `"baseline_run_id":"`+run.ID+`"`) {
		t.Fatal("ListTests lacks baseline_run_id")
	}

	wantAPIError(t, e.json(t, "PUT", path, map[string]string{"run_id": foreign.ID}), 400, "another test")
	wantAPIError(t, e.json(t, "PUT", path, map[string]string{"run_id": "00000000-0000-4000-8000-000000000000"}), 404, "run not found")
	wantAPIError(t, e.json(t, "PUT", path, map[string]string{"run_id": active.ID}), 409, "finished")
	wantAPIError(t, e.json(t, "PUT", "/api/tests/missing/baseline", map[string]string{"run_id": run.ID}), 404, "not found")
	wantAPIError(t, e.json(t, "PUT", path, `{"run":1}`), 400, "unknown field")
	wantAPIError(t, e.json(t, "PUT", path, nil), 400, "empty")

	rec = e.json(t, "PUT", path, map[string]string{"run_id": ""})
	wantStatus(t, rec, 200)
	if got = decode[model.Test](t, rec); got.BaselineRunID != "" {
		t.Fatalf("cleared baseline = %+v", got)
	}
	if strings.Contains(rec.Body.String(), "baseline_run_id") {
		t.Fatalf("empty baseline should be omitted: %s", rec.Body.String())
	}
}

func TestOverviewAPI(t *testing.T) {
	e, st := newMemEnv(t)
	tt := memTest(t, st, "ov")
	// Runs relative to now so they fall inside the default 30 days.
	now := time.Now().UTC()
	r1 := seedRun(t, st, tt.ID, 1, now.Sub(rt0)-2*time.Hour)
	seedRun(t, st, tt.ID, 2, now.Sub(rt0)-time.Hour)
	if _, err := st.SetBaseline(context.Background(), tt.ID, r1.ID); err != nil {
		t.Fatal(err)
	}
	seedRun(t, st, tt.ID, 1, -400*24*time.Hour+now.Sub(rt0)) // outside every period

	rec := e.do(t, "GET", "/api/reports/overview", nil)
	wantStatus(t, rec, 200)
	ov := decode[model.Overview](t, rec)
	if ov.Runs != 2 || ov.Passed != 2 || ov.Requests != 200 || len(ov.RunsPerDay) != 31 || len(ov.RecentRuns) != 2 {
		t.Fatalf("overview = %+v", ov)
	}
	if len(ov.Tests) != 1 || ov.Tests[0].RunCount != 2 || ov.Tests[0].PassRate != 1 || ov.Tests[0].Baseline == nil ||
		!ov.Tests[0].Baseline.Regression {
		t.Fatalf("tests = %+v", ov.Tests)
	}
	if time.Since(ov.Since) < 30*24*time.Hour-time.Minute || time.Since(ov.Since) > 30*24*time.Hour+time.Minute {
		t.Fatalf("since = %v", ov.Since)
	}
	ov = decode[model.Overview](t, e.do(t, "GET", "/api/reports/overview?days=1000", nil)) // clamped to 365
	if ov.Runs != 2 || len(ov.RunsPerDay) != 366 {
		t.Fatalf("days=1000: runs %d, %d days", ov.Runs, len(ov.RunsPerDay))
	}
	for _, q := range []string{"?days=0", "?days=-3", "?days=x"} {
		wantStatus(t, e.do(t, "GET", "/api/reports/overview"+q, nil), 400)
	}
}
