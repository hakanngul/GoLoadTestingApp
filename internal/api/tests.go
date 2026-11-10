package api

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// testRequest is the body of POST /api/tests and PUT /api/tests/{id}.
type testRequest struct {
	Name string `json:"name"`
	YAML string `json:"yaml"`
}

// validFileName restricts data file names to a safe, flat character set.
var validFileName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// checkFileName reports whether name is an acceptable data file name.
func checkFileName(name string) error {
	if !validFileName.MatchString(name) || strings.Contains(name, "..") || name == "." {
		return badRequest("invalid file name %q: use 1-128 characters from A-Z a-z 0-9 . _ - (no \"..\")", name)
	}
	return nil
}

// validateTest normalises and validates a test create/update request.
func validateTest(req *testRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(req.Name); n == 0 || n > maxTestNameLen {
		return badRequest("name must be between 1 and %d characters", maxTestNameLen)
	}
	if _, err := scenario.Parse([]byte(req.YAML)); err != nil {
		return &httpError{status: http.StatusBadRequest, msg: "invalid scenario", details: validationMessages(err)}
	}
	return nil
}

func (s *server) handleListTests(w http.ResponseWriter, r *http.Request) {
	tests, err := s.store.ListTests(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	for i := range tests {
		tests[i].Files = nonNil(tests[i].Files)
	}
	writeJSON(w, http.StatusOK, nonNil(tests))
}

func (s *server) handleCreateTest(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := validateTest(&req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	t, err := s.store.CreateTest(r.Context(), model.Test{Name: req.Name, YAML: req.YAML})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	t.Files = nonNil(t.Files)
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) handleGetTest(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTest(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	t.Files = nonNil(t.Files)
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleUpdateTest(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := validateTest(&req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	t, err := s.store.UpdateTest(r.Context(), model.Test{ID: r.PathValue("id"), Name: req.Name, YAML: req.YAML})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	t.Files = nonNil(t.Files)
	writeJSON(w, http.StatusOK, t)
}

func (s *server) handleDeleteTest(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTest(r.Context(), r.PathValue("id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := checkFileName(name); err != nil {
		s.writeErr(w, r, err)
		return
	}
	// Check existence first so a missing test is a 404 regardless of how
	// the store treats files of unknown tests.
	if _, err := s.store.GetTest(r.Context(), r.PathValue("id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBody))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.store.PutFile(r.Context(), r.PathValue("id"), name, content); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := checkFileName(name); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.store.DeleteFile(r.Context(), r.PathValue("id"), name); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleValidate parses a scenario and reports problems. It answers 200
// for both valid and invalid scenarios; only a malformed request body is
// a 400.
func (s *server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	sc, err := scenario.Parse([]byte(req.YAML))
	if err != nil {
		writeJSON(w, http.StatusOK, model.ValidateResult{Valid: false, Errors: nonNil(validationMessages(err))})
		return
	}
	writeJSON(w, http.StatusOK, model.ValidateResult{
		Valid:    true,
		Errors:   []string{},
		Model:    string(sc.Load.Model),
		Duration: sc.Load.TotalDuration().Seconds(),
		Steps:    len(sc.Steps),
	})
}
