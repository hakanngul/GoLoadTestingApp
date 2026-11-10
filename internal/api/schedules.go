package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
)

// Scheduler manages schedules (implemented by *scheduler.Scheduler).
type Scheduler interface {
	List(ctx context.Context, testID string) ([]model.Schedule, error)
	Get(ctx context.Context, id string) (model.Schedule, error)
	Create(ctx context.Context, req model.ScheduleRequest) (model.Schedule, error)
	Update(ctx context.Context, id string, req model.ScheduleRequest) (model.Schedule, error)
	Delete(ctx context.Context, id string) error
	RunNow(ctx context.Context, id string) (model.Run, error)
	Preview(req model.SchedulePreviewRequest) model.SchedulePreview
}

var _ Scheduler = (*scheduler.Scheduler)(nil)

// errNoScheduler answers schedule routes when no Scheduler is configured.
var errNoScheduler = &httpError{status: http.StatusServiceUnavailable, msg: "scheduling is not available on this server"}

// scheduleErr maps scheduler validation errors to 400 "invalid schedule";
// everything else goes through writeErr.
func (s *server) scheduleErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *scheduler.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, "invalid schedule", ve.Details...)
		return
	}
	s.writeErr(w, r, err)
}

func (s *server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	list, err := s.scheduler.List(r.Context(), r.URL.Query().Get("test_id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (s *server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	sc, err := s.scheduler.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	var req model.ScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	sc, err := s.scheduler.Create(r.Context(), req)
	if err != nil {
		s.scheduleErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	var req model.ScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	sc, err := s.scheduler.Update(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.scheduleErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	if err := s.scheduler.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	run, err := s.scheduler.RunNow(r.Context(), r.PathValue("id"))
	if err != nil {
		s.scheduleErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, normalizeRun(run))
}

func (s *server) handlePreviewSchedule(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		s.writeErr(w, r, errNoScheduler)
		return
	}
	var req model.SchedulePreviewRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.scheduler.Preview(req))
}
