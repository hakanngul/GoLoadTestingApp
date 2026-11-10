package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/data"
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
// a 400. Warnings list {{var}} references no source defines; with test_id
// the header of the test's uploaded data file is used for CSV columns.
func (s *server) handleValidate(w http.ResponseWriter, r *http.Request) {
	var req model.ValidateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	sc, err := scenario.Parse([]byte(req.YAML))
	if err != nil {
		writeJSON(w, http.StatusOK, model.ValidateResult{Valid: false, Errors: nonNil(validationMessages(err)), Warnings: []string{}})
		return
	}
	var columns []string
	known := sc.Data == nil
	if sc.Data != nil && req.TestID != "" {
		b, err := s.store.GetFile(r.Context(), req.TestID, filepath.Base(sc.Data.File))
		if err == nil {
			if h, err := data.Header(bytes.NewReader(b)); err == nil {
				columns, known = h, true
			}
		}
	}
	warnings := []string{}
	for _, is := range sc.CheckVars(columns, known) {
		warnings = append(warnings, is.String())
	}
	writeJSON(w, http.StatusOK, model.ValidateResult{
		Valid:    true,
		Errors:   []string{},
		Model:    string(sc.Load.Model),
		Duration: sc.Load.TotalDuration().Seconds(),
		Steps:    len(sc.Steps),
		Warnings: warnings,
	})
}

// tryRunner is implemented by runners that can run trial iterations
// (*runner.Manager).
type tryRunner interface {
	Try(ctx context.Context, test model.Test) (model.TryResult, error)
}

// handleTry runs one iteration of a (possibly unsaved) scenario and
// reports every step with hints. At most maxConcurrentTries run at once.
func (s *server) handleTry(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.runner.(tryRunner)
	if !ok {
		writeError(w, http.StatusNotImplemented, "test requests are not supported by this server")
		return
	}
	var req model.TryRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		s.writeErr(w, r, badRequest("yaml must not be empty"))
		return
	}
	select {
	case s.trySem <- struct{}{}:
		defer func() { <-s.trySem }()
	default:
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusTooManyRequests, "too many test requests in progress; try again in a moment")
		return
	}
	test := model.Test{ID: req.TestID, YAML: req.YAML}
	if req.TestID != "" {
		if _, err := s.store.GetTest(r.Context(), req.TestID); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	res, err := tr.Try(r.Context(), test)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
