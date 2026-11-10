package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

func (s *server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var req model.StartRunRequest
	if err := decodeJSONOptional(w, r, &req, true); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.Workers < 0 {
		s.writeErr(w, r, badRequest("workers must not be negative"))
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if err := checkRunMeta(&req.Label, &req.Notes); err != nil {
		s.writeErr(w, r, err)
		return
	}
	t, err := s.store.GetTest(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	run, err := s.runner.Start(r.Context(), t, req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, normalizeRun(run))
}

func (s *server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultRunLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.writeErr(w, r, badRequest("limit must be a positive integer"))
			return
		}
		limit = min(n, maxRunLimit)
	}
	runs, err := s.store.ListRuns(r.Context(), store.RunFilter{TestID: q.Get("test_id"), Limit: limit})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	for i := range runs {
		runs[i] = normalizeRun(runs[i])
	}
	writeJSON(w, http.StatusOK, nonNil(runs))
}

func (s *server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, normalizeRun(run))
}

// handleStopRun answers 202 when a stop was requested, 404 for unknown
// runs and 409 for runs that exist but are not active.
func (s *server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runner.Stop(id); err != nil {
		if errors.Is(err, runner.ErrNotRunning) {
			if _, gerr := s.store.GetRun(r.Context(), id); gerr != nil {
				s.writeErr(w, r, gerr)
				return
			}
		}
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) handleRunMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetRun(r.Context(), id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	pts, err := s.store.ListMetrics(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(pts))
}

func (s *server) handleRunErrors(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetRun(r.Context(), id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	samples, err := s.store.ListErrorSamples(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(samples))
}

// handleRunErrorGroups serves GET /api/runs/{id}/error-groups: the live
// groups of an active run, otherwise the groups stored when it ended.
func (s *server) handleRunErrorGroups(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	groups, err := s.runner.ErrorGroups(id)
	if err == nil {
		writeJSON(w, http.StatusOK, nonNil(groups))
		return
	}
	if !errors.Is(err, runner.ErrNotRunning) {
		s.writeErr(w, r, err)
		return
	}
	// The runner stores the final groups before it forgets the run, so a
	// run that is no longer active has them in the store.
	if _, err := s.store.GetRun(r.Context(), id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	groups, err = s.store.ListErrorGroups(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(groups))
}

// normalizeRun makes list fields encode as [] instead of null.
func normalizeRun(r model.Run) model.Run {
	r.Thresholds = nonNil(r.Thresholds)
	r.Warnings = nonNil(r.Warnings)
	return r
}
