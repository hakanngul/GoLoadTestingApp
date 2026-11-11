package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/report"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// Reporting query limits.
const (
	defaultTrendLimit   = 30
	maxTrendLimit       = 200
	defaultOverviewDays = 30
	maxOverviewDays     = 365
)

// checkRunMeta validates run label/notes lengths (nil = not provided).
func checkRunMeta(label, notes *string) error {
	if label != nil && utf8.RuneCountInString(*label) > runner.MaxLabelLen {
		return badRequest("label must be at most %d characters", runner.MaxLabelLen)
	}
	if notes != nil && utf8.RuneCountInString(*notes) > runner.MaxNotesLen {
		return badRequest("notes must be at most %d characters", runner.MaxNotesLen)
	}
	return nil
}

// reportErr maps report argument errors to 400s.
func reportErr(err error) error {
	if errors.Is(err, report.ErrBadWindow) || errors.Is(err, report.ErrBadRequest) {
		msg := err.Error()
		for _, p := range []string{report.ErrBadWindow.Error() + ": ", report.ErrBadRequest.Error() + ": "} {
			msg = strings.TrimPrefix(msg, p)
		}
		return badRequest("%s", msg)
	}
	return err
}

// parseWindow reads window=full|steady|custom (default steady) and, for
// custom, from= and to= (seconds, 0 <= from < to).
func parseWindow(q url.Values) (report.WindowSpec, error) {
	kind := q.Get("window")
	switch model.WindowKind(kind) {
	case "", model.WindowSteady:
		return report.Steady, nil
	case model.WindowFull:
		return report.WindowSpec{Kind: model.WindowFull}, nil
	case model.WindowCustom:
		from, err := parseSeconds(q, "from")
		if err != nil {
			return report.WindowSpec{}, err
		}
		to, err := parseSeconds(q, "to")
		if err != nil {
			return report.WindowSpec{}, err
		}
		if to <= from {
			return report.WindowSpec{}, badRequest("to must be greater than from")
		}
		return report.WindowSpec{Kind: model.WindowCustom, From: from, To: to}, nil
	}
	return report.WindowSpec{}, badRequest("window must be full, steady or custom")
}

func parseSeconds(q url.Values, name string) (float64, error) {
	v := q.Get(name)
	if v == "" {
		return 0, badRequest("window=custom requires %s (seconds)", name)
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, badRequest("%s must be a non-negative number of seconds", name)
	}
	return f, nil
}

// parseTolerance reads tolerance= (default 0.10, 0..1).
func parseTolerance(q url.Values) (float64, error) {
	v := q.Get("tolerance")
	if v == "" {
		return report.DefaultTolerance, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < 0 || f > 1 {
		return 0, badRequest("tolerance must be a number between 0 and 1")
	}
	return f, nil
}

// parseIntParam reads a positive integer query parameter, clamped to max.
func parseIntParam(q url.Values, name string, def, max int) (int, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, badRequest("%s must be a positive integer", name)
	}
	return min(n, max), nil
}

func (s *server) handleUpdateRun(w http.ResponseWriter, r *http.Request) {
	var req model.UpdateRunRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Label != nil {
		l := strings.TrimSpace(*req.Label)
		req.Label = &l
	}
	if err := checkRunMeta(req.Label, req.Notes); err != nil {
		s.writeErr(w, r, err)
		return
	}
	run, err := s.store.UpdateRunMeta(r.Context(), r.PathValue("id"), req.Label, req.Notes, req.Pinned)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeRun(run))
}

// handleDeleteRun deletes a finished run (409 while it is active).
func (s *server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if slices.Contains(s.runner.Active(), id) {
		writeError(w, http.StatusConflict, "run is active; stop it first")
		return
	}
	if err := s.store.DeleteRun(r.Context(), id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.reporter.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleRunReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win, err := parseWindow(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	tol, err := parseTolerance(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	rep, err := s.reporter.RunReport(r.Context(), r.PathValue("id"), win, tol)
	if err != nil {
		s.writeErr(w, r, reportErr(err))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// exportName is the download file name of a run export:
// goload-<test>-<YYYY-MM-DD>-<runid8><ext>.
func exportName(run model.Run, ext string) string {
	var b strings.Builder
	dash := false
	for _, c := range run.TestName {
		ok := c < utf8.RuneSelf && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_')
		if ok {
			b.WriteRune(c)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if slug == "" {
		slug = "test"
	}
	id := run.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("goload-%s-%s-%s%s", slug, run.StartedAt.UTC().Format("2006-01-02"), id, ext)
}

func attachment(w http.ResponseWriter, contentType, name string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// csvHeader is the column list of export.csv.
var csvHeader = []string{"t", "elapsed", "target", "active", "requests", "errors", "dropped", "rps",
	"error_rate", "mean_ms", "p50_ms", "p95_ms", "p99_ms", "max_ms", "cpu", "workers"}

func (s *server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	pts, err := s.store.ListMetrics(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	attachment(w, "text/csv; charset=utf-8", exportName(run, ".csv"))
	cw := csv.NewWriter(w)
	_ = cw.Write(csvHeader)
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	for _, p := range pts {
		_ = cw.Write([]string{
			p.T.UTC().Format(time.RFC3339Nano), f(p.Elapsed), f(p.TargetRate), i(p.Active), i(p.Requests),
			i(p.Errors), i(p.Dropped), f(p.RPS), f(p.ErrorRate), f(p.MeanMs), f(p.P50Ms), f(p.P95Ms),
			f(p.P99Ms), f(p.MaxMs), f(p.CPU), strconv.Itoa(p.Workers),
		})
	}
	cw.Flush()
}

// runExport is the body of export.json.
type runExport struct {
	Run     model.Run           `json:"run"`
	Metrics []model.MetricPoint `json:"metrics"`
	Errors  []model.ErrorSample `json:"errors"`
}

func (s *server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	pts, err := s.store.ListMetrics(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	errs, err := s.store.ListErrorSamples(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	attachment(w, "application/json; charset=utf-8", exportName(run, ".json"))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(runExport{Run: normalizeRun(run), Metrics: nonNil(pts), Errors: nonNil(errs)})
}

func (s *server) handleCompare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var ids []string
	for _, v := range q["runs"] {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) < report.MinCompareRuns || len(ids) > report.MaxCompareRuns {
		s.writeErr(w, r, badRequest("runs must list %d to %d run IDs separated by commas", report.MinCompareRuns, report.MaxCompareRuns))
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			s.writeErr(w, r, badRequest("run %q is listed twice", id))
			return
		}
		seen[id] = true
	}
	win, err := parseWindow(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	tol, err := parseTolerance(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	res, err := s.reporter.CompareRuns(r.Context(), ids, win, tol)
	if err != nil {
		s.writeErr(w, r, reportErr(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleTrend(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := parseIntParam(q, "limit", defaultTrendLimit, maxTrendLimit)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	win, err := parseWindow(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	pts, err := s.reporter.Trend(r.Context(), r.PathValue("id"), win, limit)
	if err != nil {
		s.writeErr(w, r, reportErr(err))
		return
	}
	writeJSON(w, http.StatusOK, nonNil(pts))
}

// handleSetBaseline sets ({"run_id": "<id>"}) or clears ({"run_id": ""})
// a test's baseline: 404 unknown test or run, 400 run of another test,
// 409 run not finished.
func (s *server) handleSetBaseline(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	testID := r.PathValue("id")
	if _, err := s.store.GetTest(r.Context(), testID); err != nil {
		s.writeErr(w, r, err)
		return
	}
	req.RunID = strings.TrimSpace(req.RunID)
	if req.RunID != "" {
		run, err := s.store.GetRun(r.Context(), req.RunID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
			return
		}
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		if run.TestID != testID {
			writeError(w, http.StatusBadRequest, "run belongs to another test")
			return
		}
		if !run.Status.Finished() {
			writeError(w, http.StatusConflict, "baseline run must be finished")
			return
		}
	}
	t, err := s.store.SetBaseline(r.Context(), testID, req.RunID)
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusBadRequest, "run belongs to another test")
		return
	case err != nil:
		s.writeErr(w, r, err)
		return
	}
	t.Files = nonNil(t.Files)
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	days, err := parseIntParam(r.URL.Query(), "days", defaultOverviewDays, maxOverviewDays)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ov, err := s.reporter.Overview(r.Context(), days, time.Now())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}
