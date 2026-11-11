package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/api"
	"github.com/hakanngul/GoLoadTestingApp/internal/apiclient"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
	"github.com/hakanngul/GoLoadTestingApp/internal/suite"
)

const apiToken = "s3cr3t-api-token"

// testEnv is a real GoLoad API (memory store, real runner, suites and
// scheduler) behind httptest, a target service to load, and an MCP client
// connected to the server under test through in-memory transports.
type testEnv struct {
	t      *testing.T
	api    *httptest.Server
	target *httptest.Server
	cs     *mcp.ClientSession
	client *apiclient.Client
}

func newEnv(t *testing.T, mutate ...func(*Options)) *testEnv {
	t.Helper()
	st := store.NewMemory()
	mgr := runner.NewManager(runner.Options{Store: st})
	suites := suite.New(suite.Options{Store: st, Runner: mgr, Poll: 20 * time.Millisecond})
	sched := scheduler.New(scheduler.Options{Store: st, Runner: mgr, Suites: suites})
	t.Cleanup(func() {
		_ = suites.Shutdown(context.Background())
		_ = mgr.Shutdown(context.Background())
	})
	apiSrv := httptest.NewServer(api.NewServer(api.Config{
		Store: st, Runner: mgr, Version: "vtest", AuthToken: apiToken, Suites: suites, Scheduler: sched,
	}))
	t.Cleanup(apiSrv.Close)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Token", "tok-from-target")
		fmt.Fprint(w, `{"ok":true,"user":"bob","password":"target-secret-pw","access_token":"jwt-abc"}`)
	}))
	t.Cleanup(target.Close)

	client := apiclient.New(apiSrv.URL, apiToken)
	client.Retries = 0
	opts := Options{Client: client, Version: "test", Guide: "# Guide\nhello", PollInterval: 20 * time.Millisecond}
	for _, m := range mutate {
		m(&opts)
	}
	srv := New(opts)

	ct, st2 := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	ss, err := srv.Connect(ctx, st2, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close(); cancel() })
	return &testEnv{t: t, api: apiSrv, target: target, cs: cs, client: client}
}

// call invokes a tool and returns its structured output and text. An
// error result (isError) fails the test unless wantErr is used.
func (e *testEnv) call(name string, args map[string]any) (map[string]any, string) {
	e.t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		e.t.Fatalf("%s: protocol error: %v", name, err)
	}
	text := textOf(res)
	if res.IsError {
		e.t.Fatalf("%s: tool error: %s", name, text)
	}
	m, _ := res.StructuredContent.(map[string]any)
	if m == nil {
		e.t.Fatalf("%s: no structured content (text %q)", name, text)
	}
	return m, text
}

// callErr invokes a tool that must fail with a tool error and returns its text.
func (e *testEnv) callErr(name string, args map[string]any) string {
	e.t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		e.t.Fatalf("%s: protocol error: %v", name, err)
	}
	if !res.IsError {
		e.t.Fatalf("%s: expected a tool error, got %s", name, textOf(res))
	}
	return textOf(res)
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func (e *testEnv) toolNames() []string {
	e.t.Helper()
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

// scenario returns a scenario for the target service.
func (e *testEnv) scenario(model string, stages string, extra string) string {
	return fmt.Sprintf(`name: sc
target:
  base_url: %s
load:
  model: %s
  stages: %s
steps:
  - name: ping
    path: /ping
    assert:
      - {type: status, value: "200"}
%s`, e.target.URL, model, stages, extra)
}

func (e *testEnv) createTest(name, yamlSrc string) string {
	e.t.Helper()
	out, _ := e.call("create_test", map[string]any{"name": name, "yaml": yamlSrc})
	return out["id"].(string)
}

const smallStages = "[{duration: 2s, target: 2}]"

func TestListToolsAndReadOnly(t *testing.T) {
	full := newEnv(t).toolNames()
	read := newEnv(t, func(o *Options) { o.ReadOnly = true }).toolNames()

	mutating := []string{"create_test", "update_test", "start_run", "stop_run", "set_baseline", "create_suite", "run_suite", "stop_suite_run"}
	reading := []string{"list_tests", "get_test", "validate_test", "try_test", "list_runs", "get_run", "wait_run", "get_report", "compare_runs",
		"list_suites", "get_suite", "get_suite_run", "wait_suite_run", "list_schedules", "get_server_info"}
	has := func(set []string, n string) bool {
		for _, s := range set {
			if s == n {
				return true
			}
		}
		return false
	}
	for _, n := range append(append([]string{}, mutating...), reading...) {
		if !has(full, n) {
			t.Errorf("tool %s missing", n)
		}
	}
	if len(full) != len(mutating)+len(reading) {
		t.Errorf("full tool set = %v", full)
	}
	for _, n := range mutating {
		if has(read, n) {
			t.Errorf("read-only server exposes %s", n)
		}
	}
	for _, n := range reading {
		if !has(read, n) {
			t.Errorf("read-only server lacks %s", n)
		}
	}
}

func TestHappyPath(t *testing.T) {
	e := newEnv(t)
	yamlSrc := e.scenario("closed", smallStages, "thresholds:\n  - {metric: error_rate, op: lt, value: 0.5}\n")

	// validate
	v, _ := e.call("validate_test", map[string]any{"yaml": yamlSrc})
	if v["valid"] != true {
		t.Fatalf("validate = %v", v)
	}
	bad, _ := e.call("validate_test", map[string]any{"yaml": "name: x\nbogus: 1\n"})
	if bad["valid"] != false || len(bad["errors"].([]any)) == 0 {
		t.Fatalf("invalid yaml reported as %v", bad)
	}

	// try
	tr, text := e.call("try_test", map[string]any{"yaml": yamlSrc})
	if tr["ok"] != true {
		t.Fatalf("try = %v", tr)
	}
	step := tr["steps"].([]any)[0].(map[string]any)
	if step["state"] != "passed" || step["status"] != float64(200) {
		t.Fatalf("try step = %v", step)
	}
	if strings.Contains(text, "target-secret-pw") || strings.Contains(text, "jwt-abc") || strings.Contains(text, "tok-from-target") {
		t.Errorf("try_test leaked a secret: %s", text)
	}
	if !strings.Contains(step["body"].(string), `"password":"***"`) {
		t.Errorf("body not masked: %v", step["body"])
	}

	// create, list, get
	id := e.createTest("shop", yamlSrc)
	lt, _ := e.call("list_tests", nil)
	tests := lt["tests"].([]any)
	if len(tests) != 1 || tests[0].(map[string]any)["name"] != "shop" {
		t.Fatalf("list_tests = %v", lt)
	}
	if load := tests[0].(map[string]any)["load"].(map[string]any); load["model"] != "closed" || load["peak"] != float64(2) {
		t.Errorf("load = %v", load)
	}
	gt, _ := e.call("get_test", map[string]any{"test": "shop"})
	if gt["id"] != id {
		t.Errorf("get_test by name returned %v", gt["id"])
	}
	e.call("try_test", map[string]any{"test": "shop"})

	// start (with an override), wait, report
	sr, text := e.call("start_run", map[string]any{"test": "shop", "label": "ci-42", "users": 1, "duration": "2s"})
	runID := sr["run_id"].(string)
	if sr["label"] != "mcp: ci-42" || sr["load"] != "1 VUs for 2s" {
		t.Fatalf("start_run = %v (%s)", sr, text)
	}
	wr, _ := e.call("wait_run", map[string]any{"run_id": runID, "timeout_s": 30})
	if wr["still_running"] != false || wr["status"] != "passed" {
		t.Fatalf("wait_run = %v", wr)
	}
	if wr["summary"].(map[string]any)["requests"].(float64) < 1 {
		t.Errorf("no requests: %v", wr["summary"])
	}
	rep, text := e.call("get_report", map[string]any{"run_id": runID, "window": "full"})
	if rep["verdict"] != "passed" || rep["test"] != "shop" {
		t.Fatalf("report = %v", rep)
	}
	if th := rep["thresholds"].([]any); len(th) != 1 || th[0].(map[string]any)["passed"] != true {
		t.Errorf("thresholds = %v", th)
	}
	if len(rep["slowest_steps"].([]any)) != 1 {
		t.Errorf("slowest_steps = %v", rep["slowest_steps"])
	}
	if !strings.Contains(text, "passed") {
		t.Errorf("summary line: %s", text)
	}

	lr, _ := e.call("list_runs", map[string]any{"test": "shop"})
	if runs := lr["runs"].([]any); len(runs) != 1 || runs[0].(map[string]any)["id"] != runID {
		t.Errorf("list_runs = %v", lr)
	}
	gr, _ := e.call("get_run", map[string]any{"run_id": runID})
	if gr["label"] != "mcp: ci-42" {
		t.Errorf("get_run = %v", gr)
	}

	// baseline, second run, compare
	b, _ := e.call("set_baseline", map[string]any{"test": "shop", "run_id": runID})
	if b["baseline_run_id"] != runID {
		t.Errorf("baseline = %v", b)
	}
	sr2, _ := e.call("start_run", map[string]any{"test": "shop"})
	if sr2["label"] != "mcp" {
		t.Errorf("default label = %v", sr2["label"])
	}
	id2 := sr2["run_id"].(string)
	e.call("wait_run", map[string]any{"run_id": id2, "timeout_s": 30})
	rep2, _ := e.call("get_report", map[string]any{"run_id": id2, "window": "full"})
	if rep2["baseline"] == nil {
		t.Errorf("no baseline comparison in %v", rep2)
	}
	cmp, _ := e.call("compare_runs", map[string]any{"run_ids": []string{runID, id2}})
	if len(cmp["comparisons"].([]any)) != 1 || len(cmp["runs"].([]any)) != 2 {
		t.Errorf("compare = %v", cmp)
	}
	if res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "compare_runs", Arguments: map[string]any{"run_ids": []string{runID}}}); err != nil || !res.IsError {
		t.Errorf("compare of one run should be a tool error, got %v %v", res, err)
	}

	cleared, _ := e.call("set_baseline", map[string]any{"test": "shop", "run_id": ""})
	if cleared["baseline_run_id"] != "" {
		t.Errorf("clear = %v", cleared)
	}
	info, _ := e.call("get_server_info", nil)
	if info["reachable"] != true || info["server_version"] != "vtest" {
		t.Errorf("info = %v", info)
	}
}

func TestStartRunStopAndStillRunning(t *testing.T) {
	e := newEnv(t)
	e.createTest("long", e.scenario("closed", "[{duration: 20s, target: 1}]", ""))
	sr, _ := e.call("start_run", map[string]any{"test": "long"})
	id := sr["run_id"].(string)
	wr, text := e.call("wait_run", map[string]any{"run_id": id, "timeout_s": 1})
	if wr["still_running"] != true || !strings.Contains(text, "still") {
		t.Fatalf("wait_run = %v / %s", wr, text)
	}
	e.call("stop_run", map[string]any{"run_id": id})
	wr, _ = e.call("wait_run", map[string]any{"run_id": id, "timeout_s": 30})
	if wr["status"] != "stopped" {
		t.Errorf("after stop: %v", wr["status"])
	}
}

func TestCapRefusals(t *testing.T) {
	e := newEnv(t)
	e.createTest("big", e.scenario("closed", "[{duration: 5s, target: 200}]", ""))
	e.createTest("long", e.scenario("closed", "[{duration: 1h, target: 2}]", ""))
	e.createTest("rate", e.scenario("open", "[{duration: 5s, target: 900}]", ""))
	e.createTest("ok", e.scenario("closed", smallStages, ""))

	msg := e.callErr("start_run", map[string]any{"test": "big"})
	for _, w := range []string{"200 users", "limit is 50", "GoLoad UI", "--max-users"} {
		if !strings.Contains(msg, w) {
			t.Errorf("refusal lacks %q: %s", w, msg)
		}
	}
	if msg := e.callErr("start_run", map[string]any{"test": "long"}); !strings.Contains(msg, "1h0m0s") || !strings.Contains(msg, "limit is 10m0s") {
		t.Errorf("duration refusal: %s", msg)
	}
	if msg := e.callErr("start_run", map[string]any{"test": "rate"}); !strings.Contains(msg, "900 iterations per second") || !strings.Contains(msg, "limit is 500") {
		t.Errorf("rate refusal: %s", msg)
	}
	// Overrides count: scaling a small test up, or stretching it, is refused too.
	if msg := e.callErr("start_run", map[string]any{"test": "ok", "users": 100}); !strings.Contains(msg, "100 users") {
		t.Errorf("override refusal: %s", msg)
	}
	if msg := e.callErr("start_run", map[string]any{"test": "ok", "duration": "30m"}); !strings.Contains(msg, "30m0s") {
		t.Errorf("duration override refusal: %s", msg)
	}
	// An override can bring a too-big test within the caps.
	sr, _ := e.call("start_run", map[string]any{"test": "big", "users": 3, "duration": "2s"})
	e.call("wait_run", map[string]any{"run_id": sr["run_id"], "timeout_s": 30})
	// Misfit overrides are explained.
	if msg := e.callErr("start_run", map[string]any{"test": "ok", "rate": 5}); !strings.Contains(msg, "closed model") {
		t.Errorf("misfit: %s", msg)
	}
	// Nothing was started by the refusals: only the one allowed run exists.
	lr, _ := e.call("list_runs", nil)
	if n := len(lr["runs"].([]any)); n != 1 {
		t.Errorf("%d runs exist, want 1", n)
	}
}

func TestCapsAreConfigurable(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxUsers = 5; o.MaxDuration = time.Minute })
	e.createTest("t10", e.scenario("closed", "[{duration: 2s, target: 10}]", ""))
	if msg := e.callErr("start_run", map[string]any{"test": "t10"}); !strings.Contains(msg, "limit is 5") {
		t.Errorf("custom cap: %s", msg)
	}
	info, _ := e.call("get_server_info", nil)
	l := info["limits"].(map[string]any)
	if l["max_users"] != float64(5) || l["max_duration_s"] != float64(60) || l["read_only"] != false {
		t.Errorf("limits = %v", l)
	}
}

func TestSuiteFlowAndCaps(t *testing.T) {
	e := newEnv(t)
	e.createTest("a", e.scenario("closed", "[{duration: 1s, target: 30}]", ""))
	e.createTest("b", e.scenario("closed", "[{duration: 1s, target: 30}]", ""))
	e.createTest("c", e.scenario("closed", "[{duration: 2s, target: 2}]", ""))

	// Parallel: 30 + 30 users exceed the cap even though each test alone is fine.
	e.call("create_suite", map[string]any{"name": "par", "tests": []string{"a", "b"}, "mode": "parallel"})
	if msg := e.callErr("run_suite", map[string]any{"suite": "par"}); !strings.Contains(msg, "60 users") || !strings.Contains(msg, "suite run") {
		t.Errorf("parallel refusal: %s", msg)
	}
	// Sequential: the largest test counts, so the same tests are fine.
	su, _ := e.call("create_suite", map[string]any{"name": "seq", "tests": []string{"c", "c"}, "description": "d"})
	if su["mode"] != "sequential" || len(su["tests"].([]any)) != 2 {
		t.Fatalf("suite = %v", su)
	}
	gs, _ := e.call("get_suite", map[string]any{"suite": "seq"})
	if gs["name"] != "seq" {
		t.Errorf("get_suite = %v", gs)
	}
	ls, _ := e.call("list_suites", nil)
	if len(ls["suites"].([]any)) != 2 {
		t.Errorf("list_suites = %v", ls)
	}
	rs, _ := e.call("run_suite", map[string]any{"suite": "seq", "label": "nightly"})
	if rs["label"] != "mcp: nightly" || rs["tests"] != float64(2) {
		t.Fatalf("run_suite = %v", rs)
	}
	wr, _ := e.call("wait_suite_run", map[string]any{"suite_run_id": rs["suite_run_id"], "timeout_s": 60})
	if wr["still_running"] != false || wr["status"] != "passed" {
		t.Fatalf("wait_suite_run = %v", wr)
	}
	g, _ := e.call("get_suite_run", map[string]any{"suite_run_id": rs["suite_run_id"]})
	if items := g["items"].([]any); len(items) != 2 || items[0].(map[string]any)["status"] != "passed" {
		t.Errorf("get_suite_run = %v", g)
	}
	// A suite too long in total is refused: 2 x 2s fits, so lower the cap.
	short := newEnv(t, func(o *Options) { o.MaxDuration = 3 * time.Second })
	short.createTest("c", short.scenario("closed", "[{duration: 2s, target: 2}]", ""))
	short.call("create_suite", map[string]any{"name": "seq", "tests": []string{"c", "c"}})
	if msg := short.callErr("run_suite", map[string]any{"suite": "seq"}); !strings.Contains(msg, "4s") {
		t.Errorf("suite duration refusal: %s", msg)
	}
	// Schedules are listed (none here).
	sc, _ := e.call("list_schedules", nil)
	if len(sc["schedules"].([]any)) != 0 {
		t.Errorf("schedules = %v", sc)
	}
}

func TestNameResolutionErrors(t *testing.T) {
	e := newEnv(t)
	e.createTest("checkout flow", e.scenario("closed", smallStages, ""))

	msg := e.callErr("get_test", map[string]any{"test": "checkout"})
	if !strings.Contains(msg, `no test named or with id "checkout"`) || !strings.Contains(msg, `did you mean: "checkout flow"`) {
		t.Errorf("suggestion missing: %s", msg)
	}
	if msg := e.callErr("start_run", map[string]any{"test": "nope"}); !strings.Contains(msg, "did you mean") {
		t.Errorf("start_run: %s", msg)
	}
	if msg := e.callErr("get_suite", map[string]any{"suite": "none"}); !strings.Contains(msg, `no suite named or with id "none"`) {
		t.Errorf("suite: %s", msg)
	}
	if msg := e.callErr("get_run", map[string]any{"run_id": "run-missing"}); !strings.Contains(msg, "404") {
		t.Errorf("run: %s", msg)
	}
}

func TestMaskingInTools(t *testing.T) {
	e := newEnv(t)
	yamlSrc := e.scenario("closed", smallStages, "")
	yamlSrc = strings.Replace(yamlSrc, "steps:\n", "steps:\n  - name: authed\n    path: /ping\n    headers:\n      Authorization: Bearer live-secret-token\n      X-Trace: \"{{trace}}\"\n    body: '{\"password\":\"hunter2\"}'\n", 1)
	id := e.createTest("secrets", yamlSrc)

	gt, text := e.call("get_test", map[string]any{"test": "secrets"})
	if strings.Contains(text, "live-secret-token") || strings.Contains(text, "hunter2") {
		t.Fatalf("get_test leaked: %s", text)
	}
	masked := gt["yaml"].(string)
	if !strings.Contains(masked, "{{trace}}") || !strings.Contains(masked, "***") || gt["yaml_masked"] != true {
		t.Fatalf("masked yaml: %s", masked)
	}

	// Round trip: change the name only via yaml edit; secrets must survive.
	edited := strings.Replace(masked, "target: 2", "target: 3", 1)
	e.call("update_test", map[string]any{"test": "secrets", "yaml": edited, "name": "secrets v2"})
	var stored struct{ YAML, Name string }
	if err := e.client.Do(context.Background(), http.MethodGet, "/api/tests/"+id, nil, &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.YAML, "live-secret-token") || !strings.Contains(stored.YAML, "hunter2") || strings.Contains(stored.YAML, "***") {
		t.Errorf("stored yaml lost its secrets:\n%s", stored.YAML)
	}
	if !strings.Contains(stored.YAML, "target: 3") || stored.Name != "secrets v2" {
		t.Errorf("update not applied: %q\n%s", stored.Name, stored.YAML)
	}

	// A mask with nothing to restore is refused, nothing is written.
	bad := strings.Replace(masked, "X-Trace:", "X-Api-Key: \"***\"\n      X-Trace:", 1)
	if msg := e.callErr("update_test", map[string]any{"test": "secrets v2", "yaml": bad}); !strings.Contains(msg, "X-Api-Key") {
		t.Errorf("unmatched mask accepted: %s", msg)
	}
}

func TestCreateTestWithDataFileAndValidation(t *testing.T) {
	e := newEnv(t)
	yamlSrc := e.scenario("closed", smallStages, "")
	yamlSrc = strings.Replace(yamlSrc, "load:", "data:\n  file: users.csv\nload:", 1)
	yamlSrc = strings.Replace(yamlSrc, "path: /ping", "path: /ping?u={{user}}", 1)
	out, _ := e.call("create_test", map[string]any{"name": "with data", "yaml": yamlSrc,
		"data_file": map[string]any{"name": "users.csv", "csv": "user,pw\nbob,x\nann,y\n"}})
	files := out["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["name"] != "users.csv" {
		t.Fatalf("create = %v", out)
	}
	gt, _ := e.call("get_test", map[string]any{"test": "with data"})
	if gf := gt["files"].([]any); len(gf) != 1 || gf[0].(map[string]any)["size_bytes"] != float64(len("user,pw\nbob,x\nann,y\n")) {
		t.Errorf("files = %v", gt["files"])
	}
	tr, _ := e.call("try_test", map[string]any{"test": "with data"})
	if tr["ok"] != true {
		t.Errorf("try with data = %v", tr)
	}
	// Invalid YAML is refused with the errors and nothing is saved.
	msg := e.callErr("create_test", map[string]any{"name": "broken", "yaml": "name: x\nbogus: 1\n"})
	if !strings.Contains(msg, "invalid") {
		t.Errorf("invalid create: %s", msg)
	}
	lt, _ := e.call("list_tests", nil)
	if n := len(lt["tests"].([]any)); n != 1 {
		t.Errorf("%d tests, want 1", n)
	}
	// Duplicate names are explained by the server.
	if msg := e.callErr("create_test", map[string]any{"name": "with data", "yaml": yamlSrc}); !strings.Contains(msg, "409") {
		t.Errorf("duplicate: %s", msg)
	}
}

func TestServerUnreachableAndAuth(t *testing.T) {
	e := newEnv(t)
	// Wrong token: a 401 with a hint.
	e.client.Token = "wrong"
	msg := e.callErr("list_tests", nil)
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "GOLOAD_TOKEN") || strings.Contains(msg, "Bearer") {
		t.Errorf("401 message: %s", msg)
	}
	e.client.Token = apiToken

	// Server gone: get_server_info reports it, other tools say how to fix it.
	e.api.Close()
	info, text := e.call("get_server_info", nil)
	if info["reachable"] != false || !strings.Contains(text, "NOT reachable") {
		t.Errorf("info = %v", info)
	}
	if msg := e.callErr("list_tests", nil); !strings.Contains(msg, "VPN") || strings.Contains(msg, apiToken) {
		t.Errorf("unreachable message: %s", msg)
	}
}

func TestPromptAndGuideResource(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.CreateTestPrompt = func(u string) string { return "custom prompt for " + u }
	})
	ctx := context.Background()
	pl, err := e.cs.ListPrompts(ctx, nil)
	if err != nil || len(pl.Prompts) != 1 || pl.Prompts[0].Name != "create_load_test" {
		t.Fatalf("prompts = %v %v", pl, err)
	}
	pr, err := e.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "create_load_test", Arguments: map[string]string{"url": "https://x.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if tc := pr.Messages[0].Content.(*mcp.TextContent); tc.Text != "custom prompt for https://x.example.test" {
		t.Errorf("prompt = %q", tc.Text)
	}
	rl, err := e.cs.ListResources(ctx, nil)
	if err != nil || len(rl.Resources) != 1 || rl.Resources[0].URI != "goload://guide" || rl.Resources[0].MIMEType != "text/markdown" {
		t.Fatalf("resources = %v %v", rl, err)
	}
	rr, err := e.cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "goload://guide"})
	if err != nil || rr.Contents[0].Text != "# Guide\nhello" {
		t.Errorf("read = %v %v", rr, err)
	}

	// Defaults: the built-in prompt, and no resource without a guide.
	d := newEnv(t, func(o *Options) { o.Guide = "" })
	pr, err = d.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "create_load_test", Arguments: map[string]string{"url": "https://y.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if tc := pr.Messages[0].Content.(*mcp.TextContent); !strings.Contains(tc.Text, "https://y.example.test") || !strings.Contains(tc.Text, "try_test") {
		t.Errorf("default prompt = %q", tc.Text)
	}
	if rl, err := d.cs.ListResources(ctx, nil); err != nil || len(rl.Resources) != 0 {
		t.Errorf("resources without guide = %v %v", rl, err)
	}
}

func TestOutputsAreCompact(t *testing.T) {
	e := newEnv(t)
	e.createTest("c", e.scenario("closed", smallStages, ""))
	sr, _ := e.call("start_run", map[string]any{"test": "c"})
	e.call("wait_run", map[string]any{"run_id": sr["run_id"], "timeout_s": 30})
	_, text := e.call("get_report", map[string]any{"run_id": sr["run_id"]})
	if len(text) > 4000 {
		t.Errorf("report text is %d bytes", len(text))
	}
	if strings.Contains(text, "histogram") || strings.Contains(text, "\"yaml\"") {
		t.Errorf("report carries bulk data: %s", text)
	}
}
