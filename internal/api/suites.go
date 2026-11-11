package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
	"github.com/hakanngul/GoLoadTestingApp/internal/suite"
)

// Suites manages test suites (implemented by *suite.Service).
type Suites interface {
	List(ctx context.Context) ([]model.Suite, error)
	Get(ctx context.Context, id string) (model.Suite, error)
	Create(ctx context.Context, req model.SuiteRequest) (model.Suite, error)
	Update(ctx context.Context, id string, req model.SuiteRequest) (model.Suite, error)
	Delete(ctx context.Context, id string) error
	Start(ctx context.Context, id string, req model.StartSuiteRequest) (model.SuiteRun, error)
	Stop(id string) error
	GetRun(ctx context.Context, id string) (model.SuiteRun, error)
	ListRuns(ctx context.Context, suiteID string, limit int) ([]model.SuiteRun, error)
}

var _ Suites = (*suite.Service)(nil)

// errNoSuites answers suite routes when no Suites is configured.
var errNoSuites = &httpError{status: http.StatusServiceUnavailable, msg: "test suites are not available on this server"}

// suiteErr maps suite errors to responses; everything else goes through
// writeErr.
func (s *server) suiteErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *suite.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid suite", ve.Details...)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "suite name already exists")
	case errors.Is(err, suite.ErrActive):
		writeError(w, http.StatusConflict, "suite is running")
	case errors.Is(err, suite.ErrNotRunning):
		writeError(w, http.StatusConflict, "suite run is not active")
	case errors.Is(err, suite.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, "server is shutting down")
	default:
		s.writeErr(w, r, err)
	}
}

// withSuites answers 503 when suites are not configured.
func (s *server) withSuites(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.suites == nil {
			s.writeErr(w, r, errNoSuites)
			return
		}
		h(w, r)
	}
}

func (s *server) handleListSuites(w http.ResponseWriter, r *http.Request) {
	list, err := s.suites.List(r.Context())
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (s *server) handleGetSuite(w http.ResponseWriter, r *http.Request) {
	su, err := s.suites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, su)
}

func (s *server) handleCreateSuite(w http.ResponseWriter, r *http.Request) {
	var req model.SuiteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	su, err := s.suites.Create(r.Context(), req)
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, su)
}

func (s *server) handleUpdateSuite(w http.ResponseWriter, r *http.Request) {
	var req model.SuiteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	su, err := s.suites.Update(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, su)
}

func (s *server) handleDeleteSuite(w http.ResponseWriter, r *http.Request) {
	if err := s.suites.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.suiteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleStartSuite(w http.ResponseWriter, r *http.Request) {
	var req model.StartSuiteRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	req.StartedBy = model.StartedByFrom(r.Context())
	run, err := s.suites.Start(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *server) handleListSuiteRuns(w http.ResponseWriter, r *http.Request) {
	limit := defaultRunLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			s.writeErr(w, r, badRequest("limit must be a positive integer"))
			return
		}
		limit = min(n, maxRunLimit)
	}
	suiteID := r.PathValue("id")
	if suiteID == "" {
		suiteID = r.URL.Query().Get("suite_id")
	} else if _, err := s.suites.Get(r.Context(), suiteID); err != nil {
		s.suiteErr(w, r, err)
		return
	}
	list, err := s.suites.ListRuns(r.Context(), suiteID, limit)
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (s *server) handleGetSuiteRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.suites.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		s.suiteErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *server) handleStopSuiteRun(w http.ResponseWriter, r *http.Request) {
	if err := s.suites.Stop(r.PathValue("id")); err != nil {
		s.suiteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
