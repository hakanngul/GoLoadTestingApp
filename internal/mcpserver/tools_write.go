package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
)

// registerWriteTools adds the tools that change things on the server.
// They are not registered in read-only mode.
func (h *handlers) registerWriteTools(s *mcp.Server) {
	h.regCreateTest(s)
	h.regUpdateTest(s)
	h.regStartRun(s)
	h.regStopRun(s)
	h.regBaseline(s)
	h.regCreateSuite(s)
	h.regRunSuite(s)
	h.regStopSuiteRun(s)
}

// ---- create_test / update_test ----

type DataFileIn struct {
	Name string `json:"name" jsonschema:"file name referenced by data.file in the YAML, e.g. users.csv (letters, digits, . _ -)"`
	CSV  string `json:"csv" jsonschema:"the CSV text, first line is the header"`
}
type createTestIn struct {
	Name     string      `json:"name" jsonschema:"unique test name, at most 200 characters"`
	YAML     string      `json:"yaml" jsonschema:"scenario YAML"`
	DataFile *DataFileIn `json:"data_file,omitempty" jsonschema:"optional CSV to upload for {{var}} data (data.file in the YAML must use its name)"`
}
type testSavedOut struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Warnings []string  `json:"warnings"`
	Files    []FileOut `json:"files,omitempty"`
	Load     *LoadOut  `json:"load,omitempty"`
	URL      string    `json:"url,omitempty"`
}

func (h *handlers) regCreateTest(s *mcp.Server) {
	addTool(s, "create_test", "Create a test",
		"Save a new load test from scenario YAML (validated first; invalid YAML is rejected with the errors) and optionally upload a CSV data file. Does not run it: use try_test to check it, then start_run. Put secrets in data files or ask the user; never invent credentials.",
		kindWrite, false,
		func(ctx context.Context, in createTestIn) (testSavedOut, string, error) {
			if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.YAML) == "" {
				return testSavedOut{}, "", errors.New("name and yaml are required")
			}
			if in.DataFile != nil && (strings.TrimSpace(in.DataFile.Name) == "" || in.DataFile.CSV == "") {
				return testSavedOut{}, "", errors.New("data_file needs a name and csv")
			}
			res, err := h.validate(ctx, in.YAML, "")
			if err != nil {
				return testSavedOut{}, "", err
			}
			if !res.Valid {
				return testSavedOut{}, "", fmt.Errorf("the scenario is invalid, nothing was saved: %s", strings.Join(res.Errors, "; "))
			}
			var t model.Test
			body := map[string]string{"name": strings.TrimSpace(in.Name), "yaml": in.YAML}
			if err := h.client.Do(ctx, http.MethodPost, "/api/tests", body, &t); err != nil {
				return testSavedOut{}, "", h.apiErr("creating the test", err)
			}
			warnings := res.Warnings
			if in.DataFile != nil {
				path := "/api/tests/" + esc(t.ID) + "/files/" + esc(strings.TrimSpace(in.DataFile.Name))
				if err := h.client.DoRaw(ctx, http.MethodPut, path, "text/csv", []byte(in.DataFile.CSV), nil); err != nil {
					return testSavedOut{}, "", h.apiErr(fmt.Sprintf("the test was created (id %s) but uploading the data file failed", t.ID), err)
				}
				// Re-validate: the CSV header now supplies variables.
				if r2, err := h.validate(ctx, in.YAML, t.ID); err == nil {
					warnings = r2.Warnings
				}
			}
			if strings.Contains(in.YAML, Mask) {
				warnings = append(warnings, fmt.Sprintf("the YAML contains %q: if that is a masked secret copied from get_test, replace it with the real value or a {{variable}}", Mask))
			}
			out := testSavedOut{ID: t.ID, Name: t.Name, Warnings: nonNil(warnings), URL: h.url("/tests/" + t.ID)}
			if in.DataFile != nil {
				out.Files = []FileOut{{Name: strings.TrimSpace(in.DataFile.Name), Size: int64(len(in.DataFile.CSV))}}
			}
			if info, err := analyze(in.YAML); err == nil {
				l := loadOut(info)
				out.Load = &l
			}
			return out, fmt.Sprintf("Created test %q (%s) with %d warnings.", t.Name, t.ID, len(out.Warnings)), nil
		})
}

type updateTestIn struct {
	Test string `json:"test" jsonschema:"test name or id"`
	Name string `json:"name,omitempty" jsonschema:"new name (omit to keep)"`
	YAML string `json:"yaml,omitempty" jsonschema:"new scenario YAML (omit to keep). Values shown as *** by get_test are restored from the stored test when left as is"`
}

func (h *handlers) regUpdateTest(s *mcp.Server) {
	addTool(s, "update_test", "Update a test",
		"Rename a test and/or replace its scenario YAML (validated first). Send the complete YAML, not a diff. Secrets that get_test showed as \"***\" and that you left untouched keep their stored value; a remaining \"***\" that cannot be matched is refused. Existing runs and baselines are kept.",
		kindOverride, false,
		func(ctx context.Context, in updateTestIn) (testSavedOut, string, error) {
			if strings.TrimSpace(in.Name) == "" && strings.TrimSpace(in.YAML) == "" {
				return testSavedOut{}, "", errors.New("give name and/or yaml to change")
			}
			t, err := h.getTest(ctx, in.Test)
			if err != nil {
				return testSavedOut{}, "", err
			}
			name, yamlSrc := t.Name, t.YAML
			if n := strings.TrimSpace(in.Name); n != "" {
				name = n
			}
			if strings.TrimSpace(in.YAML) != "" {
				if yamlSrc, err = UnmaskYAML(in.YAML, t.YAML); err != nil {
					return testSavedOut{}, "", err
				}
			}
			res, err := h.validate(ctx, yamlSrc, t.ID)
			if err != nil {
				return testSavedOut{}, "", err
			}
			if !res.Valid {
				return testSavedOut{}, "", fmt.Errorf("the scenario is invalid, nothing was changed: %s", strings.Join(res.Errors, "; "))
			}
			var upd model.Test
			if err := h.client.Do(ctx, http.MethodPut, "/api/tests/"+esc(t.ID), map[string]string{"name": name, "yaml": yamlSrc}, &upd); err != nil {
				return testSavedOut{}, "", h.apiErr("updating the test", err)
			}
			out := testSavedOut{ID: upd.ID, Name: upd.Name, Warnings: nonNil(res.Warnings), URL: h.url("/tests/" + upd.ID)}
			if info, err := analyze(yamlSrc); err == nil {
				l := loadOut(info)
				out.Load = &l
			}
			return out, fmt.Sprintf("Updated test %q (%s).", upd.Name, upd.ID), nil
		})
}

// ---- runs ----

type startRunIn struct {
	Test     string `json:"test" jsonschema:"test name or id"`
	Label    string `json:"label,omitempty" jsonschema:"label for the run, e.g. a version or commit; MCP runs are always prefixed with \"mcp\""`
	Users    int    `json:"users,omitempty" jsonschema:"override the peak virtual users (closed-model tests only); without duration the test's stages are scaled to this peak"`
	Rate     int    `json:"rate,omitempty" jsonschema:"override the peak iterations per second (open-model tests only)"`
	Duration string `json:"duration,omitempty" jsonschema:"override the total duration, e.g. 30s or 2m (ramp-up 10% of it, 1s to 30s, then hold). Applies to this run only; the saved test is unchanged"`
}
type startRunOut struct {
	RunID    string   `json:"run_id"`
	Test     string   `json:"test"`
	Status   string   `json:"status"`
	Label    string   `json:"label"`
	Load     string   `json:"load"` // what will run, e.g. "20 VUs for 30s"
	Override string   `json:"override,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	URL      string   `json:"url,omitempty"`
}

func (h *handlers) regStartRun(s *mcp.Server) {
	addTool(s, "start_run", "Start a run",
		fmt.Sprintf("Start a load run of a saved test and return its run id; then call wait_run. Sends real traffic to the test's target: only use targets the user owns or may test, and try_test first. Limits: at most %d users (open-model tests: %d iterations/s) and %s per run; larger runs are refused and must be started from the GoLoad UI. users/rate/duration override the load for this run only.",
			h.caps.maxUsers, h.caps.maxRate(), h.caps.maxDuration),
		kindWrite, true,
		func(ctx context.Context, in startRunIn) (startRunOut, string, error) {
			t, err := h.getTest(ctx, in.Test)
			if err != nil {
				return startRunOut{}, "", err
			}
			base, err := analyze(t.YAML)
			if err != nil {
				return startRunOut{}, "", fmt.Errorf("test %q: %w", t.Name, err)
			}
			ov, err := buildOverride(base, in.Users, in.Rate, in.Duration)
			if err != nil {
				return startRunOut{}, "", err
			}
			eff, err := effective(t.YAML, ov)
			if err != nil {
				return startRunOut{}, "", err
			}
			if err := h.caps.check("run", loadOf(eff)); err != nil {
				return startRunOut{}, "", err
			}
			label := runLabel(in.Label)
			var run model.Run
			req := model.StartRunRequest{Label: label, Load: ov}
			if err := h.client.Do(ctx, http.MethodPost, "/api/tests/"+esc(t.ID)+"/runs", req, &run); err != nil {
				return startRunOut{}, "", h.apiErr(fmt.Sprintf("starting test %q", t.Name), err)
			}
			out := startRunOut{RunID: run.ID, Test: t.Name, Status: string(run.Status), Label: run.Label, Load: eff.describe(), Warnings: run.Warnings, URL: h.url("/runs/" + run.ID)}
			if ov != nil {
				out.Override = scheduler.DescribeLoad(ov)
			}
			return out, fmt.Sprintf("Started run %s of %q (%s). Call wait_run to follow it.", run.ID, t.Name, out.Load), nil
		})
}

type stopRunOut struct {
	RunID         string `json:"run_id"`
	StopRequested bool   `json:"stop_requested"`
}

func (h *handlers) regStopRun(s *mcp.Server) {
	addTool(s, "stop_run", "Stop a run",
		"Stop an active run. The run ends as stopped with partial results. Fails if the run is not active.",
		kindOverride, false,
		func(ctx context.Context, in runIDIn) (stopRunOut, string, error) {
			id := strings.TrimSpace(in.RunID)
			if id == "" {
				return stopRunOut{}, "", errors.New("run_id is required")
			}
			if err := h.client.Do(ctx, http.MethodPost, "/api/runs/"+esc(id)+"/stop", nil, nil); err != nil {
				return stopRunOut{}, "", h.apiErr("stopping run "+id, err)
			}
			return stopRunOut{RunID: id, StopRequested: true}, "Stop requested for run " + id + ".", nil
		})
}

type baselineIn struct {
	Test  string `json:"test" jsonschema:"test name or id"`
	RunID string `json:"run_id" jsonschema:"finished run of this test to use as the baseline; empty string clears the baseline"`
}
type baselineOut struct {
	Test          string `json:"test"`
	BaselineRunID string `json:"baseline_run_id"` // "" = none
}

func (h *handlers) regBaseline(s *mcp.Server) {
	addTool(s, "set_baseline", "Set the baseline run",
		"Set the run that new runs of a test are compared against (regression rules, get_report baseline), or clear it with an empty run_id.",
		kindOverride, false,
		func(ctx context.Context, in baselineIn) (baselineOut, string, error) {
			r, err := h.resolveTest(ctx, in.Test)
			if err != nil {
				return baselineOut{}, "", err
			}
			var t model.Test
			body := map[string]string{"run_id": strings.TrimSpace(in.RunID)}
			if err := h.client.Do(ctx, http.MethodPut, "/api/tests/"+esc(r.ID)+"/baseline", body, &t); err != nil {
				return baselineOut{}, "", h.apiErr("setting the baseline", err)
			}
			line := fmt.Sprintf("Baseline of %q is now run %s.", r.Name, t.BaselineRunID)
			if t.BaselineRunID == "" {
				line = fmt.Sprintf("Cleared the baseline of %q.", r.Name)
			}
			return baselineOut{Test: r.Name, BaselineRunID: t.BaselineRunID}, line, nil
		})
}

// ---- suites ----

type createSuiteIn struct {
	Name          string   `json:"name" jsonschema:"unique suite name"`
	Tests         []string `json:"tests" jsonschema:"test names or ids, in run order"`
	Mode          string   `json:"mode,omitempty" jsonschema:"sequential (default) or parallel"`
	Description   string   `json:"description,omitempty"`
	StopOnFailure bool     `json:"stop_on_failure,omitempty" jsonschema:"sequential only: skip the remaining tests when one fails"`
}

func (h *handlers) regCreateSuite(s *mcp.Server) {
	addTool(s, "create_suite", "Create a suite",
		"Create a suite: an ordered group of saved tests that run together, one after another (sequential) or all at once (parallel). Run it with run_suite.",
		kindWrite, false,
		func(ctx context.Context, in createSuiteIn) (SuiteOut, string, error) {
			if strings.TrimSpace(in.Name) == "" || len(in.Tests) == 0 {
				return SuiteOut{}, "", errors.New("name and at least one test are required")
			}
			mode := model.SuiteMode(strings.ToLower(strings.TrimSpace(in.Mode)))
			switch mode {
			case "", model.SuiteSequential, model.SuiteParallel:
			default:
				return SuiteOut{}, "", fmt.Errorf("mode must be sequential or parallel, got %q", in.Mode)
			}
			req := model.SuiteRequest{Name: strings.TrimSpace(in.Name), Description: in.Description, Mode: mode, StopOnFailure: in.StopOnFailure}
			for _, ref := range in.Tests {
				r, err := h.resolveTest(ctx, ref)
				if err != nil {
					return SuiteOut{}, "", err
				}
				req.Items = append(req.Items, model.SuiteItem{TestID: r.ID})
			}
			var su model.Suite
			if err := h.client.Do(ctx, http.MethodPost, "/api/suites", req, &su); err != nil {
				return SuiteOut{}, "", h.apiErr("creating the suite", err)
			}
			return h.suiteOut(su), fmt.Sprintf("Created suite %q (%s) with %d tests.", su.Name, su.ID, len(su.Items)), nil
		})
}

type runSuiteIn struct {
	Suite string `json:"suite" jsonschema:"suite name or id"`
	Label string `json:"label,omitempty" jsonschema:"label for the suite run and its test runs; always prefixed with \"mcp\""`
}
type runSuiteOut struct {
	SuiteRunID string `json:"suite_run_id"`
	Suite      string `json:"suite"`
	Status     string `json:"status"`
	Label      string `json:"label"`
	Mode       string `json:"mode"`
	Tests      int    `json:"tests"`
	Load       string `json:"load"` // planned peak and duration of the whole suite
	URL        string `json:"url,omitempty"`
}

// suitePlan computes the load of a whole suite from its tests' YAML and
// per-item load overrides.
func (h *handlers) suitePlan(ctx context.Context, su model.Suite) (load, error) {
	var items []load
	for _, it := range su.Items {
		if it.TestName == "" {
			continue // deleted test: skipped by the server
		}
		var t model.Test
		if err := h.get(ctx, "/api/tests/"+esc(it.TestID), &t); err != nil {
			return load{}, h.apiErr(fmt.Sprintf("reading test %q of the suite", it.TestName), err)
		}
		eff, err := effective(t.YAML, it.Load)
		if err != nil {
			return load{}, fmt.Errorf("test %q of the suite: %w", it.TestName, err)
		}
		items = append(items, loadOf(eff))
	}
	return suiteLoad(su.Mode, time.Duration(su.GapMs)*time.Millisecond, items), nil
}

func (h *handlers) regRunSuite(s *mcp.Server) {
	addTool(s, "run_suite", "Run a suite",
		fmt.Sprintf("Start a suite run (all tests of a suite) and return the suite run id; then call wait_suite_run. Sends real traffic. Same limits as start_run, applied to the whole suite: sequential tests count by their largest load and add up in time, parallel tests add up in load (max %d users, %d it/s, %s).",
			h.caps.maxUsers, h.caps.maxRate(), h.caps.maxDuration),
		kindWrite, true,
		func(ctx context.Context, in runSuiteIn) (runSuiteOut, string, error) {
			r, err := h.resolveSuite(ctx, in.Suite)
			if err != nil {
				return runSuiteOut{}, "", err
			}
			var su model.Suite
			if err := h.get(ctx, "/api/suites/"+esc(r.ID), &su); err != nil {
				return runSuiteOut{}, "", h.apiErr("reading suite", err)
			}
			plan, err := h.suitePlan(ctx, su)
			if err != nil {
				return runSuiteOut{}, "", err
			}
			if err := h.caps.check("suite run", plan); err != nil {
				return runSuiteOut{}, "", err
			}
			var sr model.SuiteRun
			req := model.StartSuiteRequest{Label: runLabel(in.Label)}
			if err := h.client.Do(ctx, http.MethodPost, "/api/suites/"+esc(r.ID)+"/runs", req, &sr); err != nil {
				return runSuiteOut{}, "", h.apiErr(fmt.Sprintf("starting suite %q", su.Name), err)
			}
			desc := fmt.Sprintf("peak %d VUs and %d it/s, up to %s", plan.users, plan.rate, plan.duration.Round(time.Second))
			out := runSuiteOut{SuiteRunID: sr.ID, Suite: su.Name, Status: string(sr.Status), Label: sr.Label, Mode: string(su.Mode), Tests: len(sr.Items), Load: desc, URL: h.url("/suite-runs/" + sr.ID)}
			return out, fmt.Sprintf("Started suite run %s of %q (%d tests, %s). Call wait_suite_run to follow it.", sr.ID, su.Name, len(sr.Items), desc), nil
		})
}

type stopSuiteRunOut struct {
	SuiteRunID    string `json:"suite_run_id"`
	StopRequested bool   `json:"stop_requested"`
}

func (h *handlers) regStopSuiteRun(s *mcp.Server) {
	addTool(s, "stop_suite_run", "Stop a suite run",
		"Stop an active suite run: its running tests are stopped and the rest are skipped. Fails if it is not active.",
		kindOverride, false,
		func(ctx context.Context, in suiteRunIDIn) (stopSuiteRunOut, string, error) {
			id := strings.TrimSpace(in.SuiteRunID)
			if id == "" {
				return stopSuiteRunOut{}, "", errors.New("suite_run_id is required")
			}
			if err := h.client.Do(ctx, http.MethodPost, "/api/suite-runs/"+esc(id)+"/stop", nil, nil); err != nil {
				return stopSuiteRunOut{}, "", h.apiErr("stopping suite run "+id, err)
			}
			return stopSuiteRunOut{SuiteRunID: id, StopRequested: true}, "Stop requested for suite run " + id + ".", nil
		})
}
