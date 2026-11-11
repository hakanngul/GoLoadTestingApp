package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
)

// LoadOut describes the load a scenario generates.
type LoadOut struct {
	Model     string  `json:"model"`      // closed (virtual users) or open (iterations per second)
	Peak      int     `json:"peak"`       // highest stage target: users (closed) or it/s (open)
	DurationS float64 `json:"duration_s"` // all stages together
	BaseURL   string  `json:"base_url,omitempty"`
	Steps     int     `json:"steps"`
}

func loadOut(i loadInfo) LoadOut {
	return LoadOut{Model: string(i.Model), Peak: i.Peak, DurationS: i.Duration.Seconds(), BaseURL: i.BaseURL, Steps: i.Steps}
}

func (h *handlers) registerReadTools(s *mcp.Server) {
	h.regListTests(s)
	h.regGetTest(s)
	h.regValidateTest(s)
	h.regTryTest(s)
	h.regRuns(s)
	h.regReports(s)
	h.regSuites(s)
	h.regSchedulesAndInfo(s)
}

// ---- tests ----

type TestBrief struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Load          *LoadOut `json:"load,omitempty"`
	Files         []string `json:"files,omitempty"`
	BaselineRunID string   `json:"baseline_run_id,omitempty"`
	LastRun       *RunOut  `json:"last_run,omitempty"`
}

type listTestsIn struct{}
type listTestsOut struct {
	Tests []TestBrief `json:"tests"`
}

func (h *handlers) regListTests(s *mcp.Server) {
	addTool(s, "list_tests", "List tests",
		"List the saved load tests with their load shape (model, peak users or it/s, duration in seconds) and last run. Use first to find a test name or id.",
		kindRead, false,
		func(ctx context.Context, _ listTestsIn) (listTestsOut, string, error) {
			var ts []model.Test
			if err := h.get(ctx, "/api/tests", &ts); err != nil {
				return listTestsOut{}, "", h.apiErr("listing tests", err)
			}
			out := listTestsOut{Tests: []TestBrief{}}
			for _, t := range ts {
				b := TestBrief{ID: t.ID, Name: t.Name, BaselineRunID: t.BaselineRunID}
				if info, err := analyze(t.YAML); err == nil {
					l := loadOut(info)
					b.Load = &l
				}
				for _, f := range t.Files {
					b.Files = append(b.Files, f.Name)
				}
				if t.LastRun != nil {
					r := h.runOut(*t.LastRun, false)
					b.LastRun = &r
				}
				out.Tests = append(out.Tests, b)
			}
			return out, fmt.Sprintf("%d tests.", len(out.Tests)), nil
		})
}

type getTestIn struct {
	Test string `json:"test" jsonschema:"test name or id"`
}
type FileOut struct {
	Name string `json:"name"`
	Size int64  `json:"size_bytes"`
}
type getTestOut struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	YAML          string    `json:"yaml"`
	YAMLMasked    bool      `json:"yaml_masked"`
	Load          *LoadOut  `json:"load,omitempty"`
	Files         []FileOut `json:"files,omitempty"`
	BaselineRunID string    `json:"baseline_run_id,omitempty"`
	URL           string    `json:"url,omitempty"`
}

func (h *handlers) regGetTest(s *mcp.Server) {
	addTool(s, "get_test", "Get a test",
		"Get one saved test: its scenario YAML (secrets such as Authorization headers, tokens and passwords are replaced by \"***\"; {{var}} templates are shown), load shape, data files and baseline run.",
		kindRead, false,
		func(ctx context.Context, in getTestIn) (getTestOut, string, error) {
			t, err := h.getTest(ctx, in.Test)
			if err != nil {
				return getTestOut{}, "", err
			}
			masked, err := MaskYAML(t.YAML)
			if err != nil {
				return getTestOut{}, "", fmt.Errorf("test %q: %w", t.Name, err)
			}
			out := getTestOut{ID: t.ID, Name: t.Name, YAML: masked, YAMLMasked: masked != t.YAML && strings.Contains(masked, Mask),
				BaselineRunID: t.BaselineRunID, URL: h.url("/tests/" + t.ID)}
			if info, err := analyze(t.YAML); err == nil {
				l := loadOut(info)
				out.Load = &l
			}
			for _, f := range t.Files {
				out.Files = append(out.Files, FileOut{Name: f.Name, Size: f.Size})
			}
			return out, fmt.Sprintf("Test %q (%s).", t.Name, t.ID), nil
		})
}

type validateIn struct {
	YAML string `json:"yaml" jsonschema:"scenario YAML to check"`
	Test string `json:"test,omitempty" jsonschema:"optional saved test (name or id) whose uploaded data file supplies the CSV columns for {{var}} checks"`
}
type validateOut struct {
	Valid     bool     `json:"valid"`
	Errors    []string `json:"errors"`
	Warnings  []string `json:"warnings"`
	Load      *LoadOut `json:"load,omitempty"`
	RunNotice string   `json:"run_notice,omitempty"`
}

func (h *handlers) validate(ctx context.Context, yamlSrc, testID string) (model.ValidateResult, error) {
	var res model.ValidateResult
	err := h.client.Do(ctx, http.MethodPost, "/api/validate", model.ValidateRequest{YAML: yamlSrc, TestID: testID}, &res)
	if err != nil {
		return res, h.apiErr("validating", err)
	}
	return res, nil
}

func (h *handlers) regValidateTest(s *mcp.Server) {
	addTool(s, "validate_test", "Validate a test",
		"Check scenario YAML without saving or sending anything. Returns errors (\"path: message\"), warnings (e.g. {{var}} that no data source defines) and the load shape. Use before create_test or update_test.",
		kindRead, false,
		func(ctx context.Context, in validateIn) (validateOut, string, error) {
			if strings.TrimSpace(in.YAML) == "" {
				return validateOut{}, "", errors.New("yaml is required")
			}
			testID := ""
			if in.Test != "" {
				r, err := h.resolveTest(ctx, in.Test)
				if err != nil {
					return validateOut{}, "", err
				}
				testID = r.ID
			}
			res, err := h.validate(ctx, in.YAML, testID)
			if err != nil {
				return validateOut{}, "", err
			}
			out := validateOut{Valid: res.Valid, Errors: nonNil(res.Errors), Warnings: nonNil(res.Warnings)}
			if res.Valid {
				if info, err := analyze(in.YAML); err == nil {
					l := loadOut(info)
					out.Load = &l
					if err := h.caps.check("run", loadOf(info)); err != nil {
						out.RunNotice = "start_run would refuse this test as written: " + err.Error()
					}
				}
			}
			line := "Scenario is valid"
			if !res.Valid {
				line = fmt.Sprintf("Scenario is invalid (%d errors)", len(res.Errors))
			}
			return out, fmt.Sprintf("%s, %d warnings.", line, len(out.Warnings)), nil
		})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ---- try ----

type tryIn struct {
	YAML string `json:"yaml,omitempty" jsonschema:"scenario YAML to try (may be unsaved); defaults to the saved test's YAML"`
	Test string `json:"test,omitempty" jsonschema:"saved test (name or id): its YAML is used when yaml is empty and its uploaded data file supplies the first CSV row"`
}

type HeaderOut struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type ExtractOut struct {
	Var   string `json:"var"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
type AssertionOut struct {
	Assertion string `json:"assertion"`
	Passed    bool   `json:"passed"`
	Message   string `json:"message,omitempty"`
}
type TryStepOut struct {
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	State           string         `json:"state"` // passed, failed, not_run
	Method          string         `json:"method,omitempty"`
	URL             string         `json:"url,omitempty"`
	FinalURL        string         `json:"final_url,omitempty"`
	Status          int            `json:"status,omitempty"`
	DurationMs      float64        `json:"duration_ms,omitempty"`
	Redirects       int            `json:"redirects,omitempty"`
	Location        string         `json:"location,omitempty"`
	Extracted       []ExtractOut   `json:"extracted,omitempty"`
	Assertions      []AssertionOut `json:"assertions,omitempty"`
	ResponseHeaders []HeaderOut    `json:"response_headers,omitempty"`
	Body            string         `json:"body,omitempty"` // first 2 KiB, secrets masked
	BodyTruncated   bool           `json:"body_truncated,omitempty"`
	Error           string         `json:"error,omitempty"`
}
type HintOut struct {
	Level   string `json:"level"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}
type tryOut struct {
	OK         bool         `json:"ok"`
	DurationMs float64      `json:"duration_ms"`
	DataNote   string       `json:"data_note,omitempty"`
	Steps      []TryStepOut `json:"steps"`
	Hints      []HintOut    `json:"hints"`
}

const bodySnippetMax = 2048

func tryOutOf(r model.TryResult) tryOut {
	out := tryOut{OK: r.OK, DurationMs: r2(r.DurationMs), DataNote: r.DataNote, Steps: []TryStepOut{}, Hints: []HintOut{}}
	for _, st := range r.Steps {
		o := TryStepOut{
			Name: st.Name, Kind: st.Kind, State: st.State, Method: st.Method, URL: MaskURL(st.URL), Status: st.Status,
			DurationMs: r2(st.DurationMs), Redirects: len(st.Redirects), Location: MaskURL(st.Location), Error: st.Error,
		}
		if st.FinalURL != "" && st.FinalURL != st.URL {
			o.FinalURL = MaskURL(st.FinalURL)
		}
		for _, e := range st.Extracted {
			o.Extracted = append(o.Extracted, ExtractOut{Var: e.Var, OK: e.OK, Error: e.Error})
		}
		for _, a := range st.Assertions {
			o.Assertions = append(o.Assertions, AssertionOut{Assertion: a.Assertion, Passed: a.Passed, Message: a.Message})
		}
		for _, hd := range st.ResponseHeaders {
			o.ResponseHeaders = append(o.ResponseHeaders, HeaderOut{Name: hd.Name, Value: cut(MaskHeader(hd.Name, hd.Value), 200)})
		}
		switch {
		case st.BodyBinary:
			o.Body = "(binary body)"
		case st.Body != "":
			// Mask first: cutting could split a secret value and defeat the pattern.
			body := MaskBody(st.Body)
			o.Body = cut(body, bodySnippetMax)
			o.BodyTruncated = st.BodyTruncated || len(body) > bodySnippetMax
		}
		out.Steps = append(out.Steps, o)
	}
	for _, hn := range r.Hints {
		o := HintOut{Level: hn.Level, Step: hn.Step, Message: hn.Message}
		if hn.Fix != nil {
			o.Fix = hn.Fix.Label
		}
		out.Hints = append(out.Hints, o)
	}
	return out
}

func (h *handlers) regTryTest(s *mcp.Server) {
	addTool(s, "try_test", "Try a test once",
		"Send ONE iteration of a scenario (login + each step once, in order) to the real target and report each step: status, duration, final URL after redirects, extracted variables, assertion results, hints for fixing it, and a 2 KiB body snippet. Use it to debug a scenario before start_run. Pass yaml (unsaved draft) and/or test (saved test). Think times are skipped; max 20 steps, 30 s.",
		kindWrite, true,
		func(ctx context.Context, in tryIn) (tryOut, string, error) {
			req := model.TryRequest{YAML: in.YAML}
			if in.Test != "" {
				t, err := h.getTest(ctx, in.Test)
				if err != nil {
					return tryOut{}, "", err
				}
				req.TestID = t.ID
				if strings.TrimSpace(req.YAML) == "" {
					req.YAML = t.YAML
				}
			}
			if strings.TrimSpace(req.YAML) == "" {
				return tryOut{}, "", errors.New("give yaml or test")
			}
			var res model.TryResult
			if err := h.client.Do(ctx, http.MethodPost, "/api/tests/try", req, &res); err != nil {
				return tryOut{}, "", h.apiErr("trying the scenario", err)
			}
			out := tryOutOf(res)
			passed := 0
			for _, st := range out.Steps {
				if st.State == model.TryPassed {
					passed++
				}
			}
			line := fmt.Sprintf("Try %s: %d of %d steps passed in %.0f ms, %d hints.", map[bool]string{true: "OK", false: "FAILED"}[out.OK], passed, len(out.Steps), out.DurationMs, len(out.Hints))
			return out, line, nil
		})
}

// ---- runs ----

type listRunsIn struct {
	Test  string `json:"test,omitempty" jsonschema:"only runs of this test (name or id)"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many runs, newest first (default 10, max 50)"`
}
type listRunsOut struct {
	Runs []RunOut `json:"runs"`
}

type runIDIn struct {
	RunID string `json:"run_id" jsonschema:"run id"`
}

type waitRunIn struct {
	RunID    string `json:"run_id" jsonschema:"run id from start_run"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"seconds to wait for the run to finish (default 60, max 120)"`
}
type waitRunOut struct {
	RunOut
	StillRunning bool `json:"still_running"`
}

func (h *handlers) fetchRun(ctx context.Context, id string) (model.Run, error) {
	if strings.TrimSpace(id) == "" {
		return model.Run{}, errors.New("run_id is required (see list_runs)")
	}
	var r model.Run
	if err := h.get(ctx, "/api/runs/"+esc(strings.TrimSpace(id)), &r); err != nil {
		return r, h.apiErr("reading run "+id, err)
	}
	return r, nil
}

func (h *handlers) regRuns(s *mcp.Server) {
	addTool(s, "list_runs", "List runs",
		"List recent runs, newest first, with status and headline numbers (requests, rps, error_rate as a fraction, p95 in ms). Optionally for one test.",
		kindRead, false,
		func(ctx context.Context, in listRunsIn) (listRunsOut, string, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 10
			}
			limit = min(limit, 50)
			q := url.Values{"limit": {fmt.Sprint(limit)}}
			if in.Test != "" {
				r, err := h.resolveTest(ctx, in.Test)
				if err != nil {
					return listRunsOut{}, "", err
				}
				q.Set("test_id", r.ID)
			}
			var runs []model.Run
			if err := h.get(ctx, "/api/runs?"+q.Encode(), &runs); err != nil {
				return listRunsOut{}, "", h.apiErr("listing runs", err)
			}
			out := listRunsOut{Runs: []RunOut{}}
			for _, r := range runs {
				out.Runs = append(out.Runs, h.runOut(r, false))
			}
			return out, fmt.Sprintf("%d runs.", len(out.Runs)), nil
		})

	addTool(s, "get_run", "Get a run",
		"Get one run: status, headline numbers, thresholds, regression results, warnings and error. For the full analysis (error groups, slowest steps, baseline) use get_report.",
		kindRead, false,
		func(ctx context.Context, in runIDIn) (RunOut, string, error) {
			r, err := h.fetchRun(ctx, in.RunID)
			if err != nil {
				return RunOut{}, "", err
			}
			o := h.runOut(r, true)
			return o, runLine(o), nil
		})

	addTool(s, "wait_run", "Wait for a run",
		"Wait until a run finishes (polls every 2 s) and return its status and summary. If it is still running after timeout_s (default 60, max 120), returns still_running=true: call again. Use after start_run.",
		kindRead, false,
		func(ctx context.Context, in waitRunIn) (waitRunOut, string, error) {
			if strings.TrimSpace(in.RunID) == "" {
				return waitRunOut{}, "", errors.New("run_id is required")
			}
			r, done, err := waitFor(ctx, h.poll, clampTimeout(in.TimeoutS), func(ctx context.Context) (model.Run, bool, error) {
				r, err := h.fetchRun(ctx, in.RunID)
				return r, r.Status.Finished(), err
			})
			if err != nil {
				return waitRunOut{}, "", err
			}
			o := waitRunOut{RunOut: h.runOut(r, true), StillRunning: !done}
			line := runLine(o.RunOut)
			if !done {
				line = fmt.Sprintf("Run %s is still %s after %s; call wait_run again.", r.ID, r.Status, clampTimeout(in.TimeoutS))
			}
			return o, line, nil
		})
}

// ---- reports ----

type reportIn struct {
	RunID  string  `json:"run_id" jsonschema:"run id"`
	Window string  `json:"window,omitempty" jsonschema:"steady (default: without ramp-up and ramp-down) or full; with from_s and to_s the window is custom"`
	FromS  float64 `json:"from_s,omitempty" jsonschema:"custom window start, seconds since run start"`
	ToS    float64 `json:"to_s,omitempty" jsonschema:"custom window end, seconds since run start"`
}
type WindowOut struct {
	Kind  string  `json:"kind"`
	FromS float64 `json:"from_s"`
	ToS   float64 `json:"to_s"`
}
type BaselineOut struct {
	ReferenceRunID string     `json:"reference_run_id"`
	Verdict        string     `json:"verdict"` // worse (a metric regressed) or ok
	Metrics        []DeltaOut `json:"metrics"`
}
type reportOut struct {
	RunID        string          `json:"run_id"`
	Test         string          `json:"test"`
	Status       string          `json:"status"`
	Verdict      string          `json:"verdict"` // passed, failed (a threshold or regression rule failed), stopped, error, running
	Window       WindowOut       `json:"window"`
	Summary      SummaryOut      `json:"summary"`
	Thresholds   []ThresholdOut  `json:"thresholds"`
	Regression   []ThresholdOut  `json:"regression"`
	TopErrors    []ErrorGroupOut `json:"top_errors"`
	SlowestSteps []StepOut       `json:"slowest_steps"`
	Baseline     *BaselineOut    `json:"baseline,omitempty"`
	URL          string          `json:"url,omitempty"`
}

func windowQuery(w string, from, to float64) (url.Values, error) {
	q := url.Values{}
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "", "steady":
		if from != 0 || to != 0 {
			q.Set("window", "custom")
			q.Set("from", fmt.Sprint(from))
			q.Set("to", fmt.Sprint(to))
		}
	case "full":
		q.Set("window", "full")
	case "custom":
		q.Set("window", "custom")
		q.Set("from", fmt.Sprint(from))
		q.Set("to", fmt.Sprint(to))
	default:
		return nil, fmt.Errorf("window must be steady, full or custom, got %q", w)
	}
	return q, nil
}

type compareIn struct {
	RunIDs []string `json:"run_ids" jsonschema:"2 to 6 run ids; the first is the reference the others are compared to"`
	Window string   `json:"window,omitempty" jsonschema:"steady (default) or full"`
}
type CompareRunOut struct {
	RunID   string      `json:"run_id"`
	Test    string      `json:"test"`
	Label   string      `json:"label,omitempty"`
	Status  string      `json:"status"`
	Summary *SummaryOut `json:"summary,omitempty"`
}
type compareOut struct {
	Runs        []CompareRunOut `json:"runs"`
	Comparisons []ComparisonOut `json:"comparisons"`
}

func (h *handlers) regReports(s *mcp.Server) {
	addTool(s, "get_report", "Get a run report",
		"Analyse a finished run: verdict, summary (requests, rps, error_rate as a fraction, p50/p95/p99 in ms), threshold and regression results, top 5 error groups, the 10 slowest steps by p95, and the comparison with the test's baseline run. Default window is steady (ramp-up and ramp-down excluded).",
		kindRead, false,
		func(ctx context.Context, in reportIn) (reportOut, string, error) {
			id := strings.TrimSpace(in.RunID)
			if id == "" {
				return reportOut{}, "", errors.New("run_id is required")
			}
			q, err := windowQuery(in.Window, in.FromS, in.ToS)
			if err != nil {
				return reportOut{}, "", err
			}
			var rep model.RunReport
			if err := h.get(ctx, "/api/runs/"+esc(id)+"/report?"+q.Encode(), &rep); err != nil {
				return reportOut{}, "", h.apiErr("reading the report of run "+id, err)
			}
			var groups []model.ErrorGroup
			_ = h.get(ctx, "/api/runs/"+esc(id)+"/error-groups", &groups) // best effort
			th, reg := splitThresholds(rep.Thresholds)
			out := reportOut{
				RunID: rep.Run.ID, Test: rep.Run.TestName, Status: string(rep.Run.Status),
				Verdict: verdict(rep.Run.Status, rep.Thresholds),
				Window:  WindowOut{Kind: string(rep.Window.Kind), FromS: r2(rep.Window.From), ToS: r2(rep.Window.To)},
				Summary: *summaryOut(&rep.Summary), Thresholds: nonNil(th), Regression: nonNil(reg),
				TopErrors: nonNil(topErrorGroups(groups, 5)), SlowestSteps: nonNil(slowestSteps(rep.Summary.ByStep, 10)),
				URL: h.url("/runs/" + rep.Run.ID),
			}
			if rep.Baseline != nil {
				c := comparisonOut(*rep.Baseline)
				v := "ok"
				if c.Regression {
					v = "worse"
				}
				out.Baseline = &BaselineOut{ReferenceRunID: c.ReferenceRunID, Verdict: v, Metrics: c.Metrics}
			}
			ro := h.runOut(rep.Run, true)
			ro.Summary, ro.Thresholds, ro.Regression = &out.Summary, th, reg
			ro.Status = out.Verdict
			line := runLine(ro)
			if out.Baseline != nil {
				line += " Baseline: " + out.Baseline.Verdict + "."
			}
			return out, line, nil
		})

	addTool(s, "compare_runs", "Compare runs",
		"Compare 2 to 6 runs: the first is the reference, each other run gets per-metric deltas (relative; error_rate absolute) with a verdict better/same/worse, a regression flag and the steps that got worse.",
		kindRead, false,
		func(ctx context.Context, in compareIn) (compareOut, string, error) {
			if n := len(in.RunIDs); n < 2 || n > 6 {
				return compareOut{}, "", fmt.Errorf("run_ids needs 2 to 6 run ids, got %d", n)
			}
			q, err := windowQuery(in.Window, 0, 0)
			if err != nil {
				return compareOut{}, "", err
			}
			q.Set("runs", strings.Join(in.RunIDs, ","))
			var res model.CompareResult
			if err := h.get(ctx, "/api/compare?"+q.Encode(), &res); err != nil {
				return compareOut{}, "", h.apiErr("comparing runs", err)
			}
			out := compareOut{Runs: []CompareRunOut{}, Comparisons: []ComparisonOut{}}
			for _, r := range res.Runs {
				out.Runs = append(out.Runs, CompareRunOut{RunID: r.Run.ID, Test: r.Run.TestName, Label: r.Run.Label, Status: string(r.Run.Status), Summary: summaryOut(&r.Summary)})
			}
			regs := 0
			for _, c := range res.Comparisons {
				co := comparisonOut(c)
				if co.Regression {
					regs++
				}
				out.Comparisons = append(out.Comparisons, co)
			}
			return out, fmt.Sprintf("Compared %d runs against %s: %d with a regression.", len(out.Runs), in.RunIDs[0], regs), nil
		})
}

// ---- suites ----

type SuiteItemOut struct {
	Test    string `json:"test"`
	TestID  string `json:"test_id"`
	Workers int    `json:"workers,omitempty"`
	Load    string `json:"load,omitempty"` // load override, if any
}
type SuiteOut struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Mode          string         `json:"mode"`
	StopOnFailure bool           `json:"stop_on_failure,omitempty"`
	Tests         []SuiteItemOut `json:"tests"`
	LastRun       *SuiteRunOut   `json:"last_run,omitempty"`
}

func (h *handlers) suiteOut(s model.Suite) SuiteOut {
	o := SuiteOut{ID: s.ID, Name: s.Name, Description: s.Description, Mode: string(s.Mode), StopOnFailure: s.StopOnFailure, Tests: []SuiteItemOut{}}
	for _, it := range s.Items {
		name := it.TestName
		if name == "" {
			name = "(deleted test)"
		}
		o.Tests = append(o.Tests, SuiteItemOut{Test: name, TestID: it.TestID, Workers: it.Workers, Load: scheduler.DescribeLoad(it.Load)})
	}
	if s.LastRun != nil {
		r := h.suiteRunOut(*s.LastRun)
		r.Items = nil
		o.LastRun = &r
	}
	return o
}

type listSuitesIn struct{}
type listSuitesOut struct {
	Suites []SuiteOut `json:"suites"`
}
type getSuiteIn struct {
	Suite string `json:"suite" jsonschema:"suite name or id"`
}
type suiteRunIDIn struct {
	SuiteRunID string `json:"suite_run_id" jsonschema:"suite run id from run_suite"`
}
type waitSuiteRunIn struct {
	SuiteRunID string `json:"suite_run_id" jsonschema:"suite run id from run_suite"`
	TimeoutS   int    `json:"timeout_s,omitempty" jsonschema:"seconds to wait for the suite run to finish (default 60, max 120)"`
}
type waitSuiteRunOut struct {
	SuiteRunOut
	StillRunning bool `json:"still_running"`
}

func (h *handlers) fetchSuiteRun(ctx context.Context, id string) (model.SuiteRun, error) {
	if strings.TrimSpace(id) == "" {
		return model.SuiteRun{}, errors.New("suite_run_id is required")
	}
	var r model.SuiteRun
	if err := h.get(ctx, "/api/suite-runs/"+esc(strings.TrimSpace(id)), &r); err != nil {
		return r, h.apiErr("reading suite run "+id, err)
	}
	return r, nil
}

func (h *handlers) regSuites(s *mcp.Server) {
	addTool(s, "list_suites", "List suites",
		"List test suites (ordered groups of tests that run together) with their last run.",
		kindRead, false,
		func(ctx context.Context, _ listSuitesIn) (listSuitesOut, string, error) {
			var ss []model.Suite
			if err := h.get(ctx, "/api/suites", &ss); err != nil {
				return listSuitesOut{}, "", h.apiErr("listing suites", err)
			}
			out := listSuitesOut{Suites: []SuiteOut{}}
			for _, su := range ss {
				out.Suites = append(out.Suites, h.suiteOut(su))
			}
			return out, fmt.Sprintf("%d suites.", len(out.Suites)), nil
		})

	addTool(s, "get_suite", "Get a suite",
		"Get one suite: mode (sequential or parallel), its tests in order with any load override, and the last suite run.",
		kindRead, false,
		func(ctx context.Context, in getSuiteIn) (SuiteOut, string, error) {
			r, err := h.resolveSuite(ctx, in.Suite)
			if err != nil {
				return SuiteOut{}, "", err
			}
			var su model.Suite
			if err := h.get(ctx, "/api/suites/"+esc(r.ID), &su); err != nil {
				return SuiteOut{}, "", h.apiErr("reading suite", err)
			}
			return h.suiteOut(su), fmt.Sprintf("Suite %q: %s, %d tests.", su.Name, su.Mode, len(su.Items)), nil
		})

	addTool(s, "get_suite_run", "Get a suite run",
		"Get one suite run: overall status and, per test, status, run id and headline numbers.",
		kindRead, false,
		func(ctx context.Context, in suiteRunIDIn) (SuiteRunOut, string, error) {
			r, err := h.fetchSuiteRun(ctx, in.SuiteRunID)
			if err != nil {
				return SuiteRunOut{}, "", err
			}
			o := h.suiteRunOut(r)
			return o, suiteRunLine(o), nil
		})

	addTool(s, "wait_suite_run", "Wait for a suite run",
		"Wait until a suite run finishes (polls every 2 s). If it is still running after timeout_s (default 60, max 120), returns still_running=true: call again.",
		kindRead, false,
		func(ctx context.Context, in waitSuiteRunIn) (waitSuiteRunOut, string, error) {
			r, done, err := waitFor(ctx, h.poll, clampTimeout(in.TimeoutS), func(ctx context.Context) (model.SuiteRun, bool, error) {
				r, err := h.fetchSuiteRun(ctx, in.SuiteRunID)
				return r, r.Status.Finished(), err
			})
			if err != nil {
				return waitSuiteRunOut{}, "", err
			}
			o := waitSuiteRunOut{SuiteRunOut: h.suiteRunOut(r), StillRunning: !done}
			line := suiteRunLine(o.SuiteRunOut)
			if !done {
				line += " Still running; call wait_suite_run again."
			}
			return o, line, nil
		})
}

// ---- schedules and server info ----

type ScheduleOut struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Target     string     `json:"target"` // "test: <name>" or "suite: <name>"
	Kind       string     `json:"kind"`   // once or recurring
	Cron       string     `json:"cron,omitempty"`
	At         string     `json:"at,omitempty"`
	Timezone   string     `json:"timezone,omitempty"`
	Enabled    bool       `json:"enabled"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
	LastNote   string     `json:"last_message,omitempty"`
	LastRunID  string     `json:"last_run_id,omitempty"`
	Load       string     `json:"load,omitempty"`
}
type listSchedulesIn struct {
	Test string `json:"test,omitempty" jsonschema:"only schedules of this test (name or id)"`
}
type listSchedulesOut struct {
	Schedules []ScheduleOut `json:"schedules"`
}

type serverInfoIn struct{}
type MCPLimits struct {
	ReadOnly       bool    `json:"read_only"`
	MaxUsers       int     `json:"max_users"`
	MaxOpenRate    int     `json:"max_open_model_rate"` // iterations per second for open-model tests
	MaxDurationS   float64 `json:"max_duration_s"`
	StartedRunsTag string  `json:"started_runs_label"`
}
type serverInfoOut struct {
	ServerURL     string    `json:"server_url"`
	Reachable     bool      `json:"reachable"`
	Error         string    `json:"error,omitempty"`
	ServerVersion string    `json:"server_version,omitempty"`
	Retention     string    `json:"retention,omitempty"`
	MCPVersion    string    `json:"mcp_version"`
	Limits        MCPLimits `json:"limits"`
}

func (h *handlers) regSchedulesAndInfo(s *mcp.Server) {
	addTool(s, "list_schedules", "List schedules",
		"List schedules (planned runs of a test or suite): recurrence, next run, last outcome, load override. Read-only: schedules are managed in the GoLoad UI.",
		kindRead, false,
		func(ctx context.Context, in listSchedulesIn) (listSchedulesOut, string, error) {
			path := "/api/schedules"
			if in.Test != "" {
				r, err := h.resolveTest(ctx, in.Test)
				if err != nil {
					return listSchedulesOut{}, "", err
				}
				path += "?test_id=" + esc(r.ID)
			}
			var ss []model.Schedule
			if err := h.get(ctx, path, &ss); err != nil {
				return listSchedulesOut{}, "", h.apiErr("listing schedules", err)
			}
			out := listSchedulesOut{Schedules: []ScheduleOut{}}
			for _, sc := range ss {
				target := "test: " + sc.TestName
				if sc.SuiteID != "" {
					target = "suite: " + sc.SuiteName
				}
				out.Schedules = append(out.Schedules, ScheduleOut{
					ID: sc.ID, Name: sc.Name, Target: target, Kind: string(sc.Kind), Cron: sc.Cron, At: sc.At, Timezone: sc.Timezone,
					Enabled: sc.Enabled, NextRunAt: sc.NextRunAt, LastStatus: sc.LastStatus, LastNote: sc.LastMessage,
					LastRunID: sc.LastRunID, Load: scheduler.DescribeLoad(sc.Load),
				})
			}
			return out, fmt.Sprintf("%d schedules.", len(out.Schedules)), nil
		})

	addTool(s, "get_server_info", "Get server info",
		"Check the GoLoad server (reachable, version, how long runs are kept) and show this MCP server's limits: read-only mode, max users, max duration. Call it first when something fails.",
		kindRead, false,
		func(ctx context.Context, _ serverInfoIn) (serverInfoOut, string, error) {
			out := serverInfoOut{
				ServerURL: h.client.BaseURL, MCPVersion: h.opts.Version,
				Limits: MCPLimits{ReadOnly: h.opts.ReadOnly, MaxUsers: h.caps.maxUsers, MaxOpenRate: h.caps.maxRate(),
					MaxDurationS: h.caps.maxDuration.Seconds(), StartedRunsTag: runLabel("")},
			}
			var health struct {
				Status           string `json:"status"`
				Version          string `json:"version"`
				RetentionSeconds int64  `json:"retention_seconds"`
			}
			if err := h.get(ctx, "/api/health", &health); err != nil {
				out.Error = h.apiErr("health check", err).Error()
				return out, "GoLoad server is NOT reachable: " + out.Error, nil
			}
			out.Reachable = true
			out.ServerVersion = health.Version
			out.Retention = "forever"
			if health.RetentionSeconds > 0 {
				out.Retention = (time.Duration(health.RetentionSeconds) * time.Second).String()
			}
			return out, fmt.Sprintf("GoLoad server %s is reachable (runs kept %s); MCP limits: %d users, %s, read-only=%v.",
				health.Version, out.Retention, h.caps.maxUsers, h.caps.maxDuration, h.opts.ReadOnly), nil
		})
}
