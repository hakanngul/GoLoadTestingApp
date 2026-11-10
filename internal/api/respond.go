package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a model.APIError body.
func writeError(w http.ResponseWriter, status int, msg string, details ...string) {
	writeJSON(w, status, model.APIError{Error: msg, Details: details})
}

// httpError is an error that already carries its HTTP status and body.
type httpError struct {
	status  int
	msg     string
	details []string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &httpError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

// validationDetailer may be implemented by errors (e.g. from the runner)
// that carry a list of human-readable validation problems.
type validationDetailer interface {
	ValidationDetails() []string
}

// writeErr maps err to an HTTP status and writes an APIError:
//
//	*httpError                         -> its status
//	store.ErrNotFound                  -> 404
//	store.ErrConflict                  -> 409 "test name already exists"
//	runner.ErrNotRunning               -> 409
//	runner.ErrTargetNotAllowed         -> 403
//	http.MaxBytesError                 -> 413
//	scenario.FieldError / ValidationDetails() / "invalid scenario..." -> 400
//	anything else                      -> 500 (message logged, not exposed)
func (s *server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	var mbe *http.MaxBytesError
	var vd validationDetailer
	switch {
	case errors.As(err, &he):
		writeError(w, he.status, he.msg, he.details...)
	case errors.As(err, &mbe):
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body too large (limit %d bytes)", mbe.Limit))
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "test name already exists")
	case errors.Is(err, runner.ErrNotRunning):
		writeError(w, http.StatusConflict, "run is not active")
	case errors.Is(err, runner.ErrTargetNotAllowed):
		writeError(w, http.StatusForbidden, "target not allowed", err.Error())
	case errors.Is(err, runner.ErrDistributedUnavailable):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, runner.ErrShuttingDown):
		writeError(w, http.StatusServiceUnavailable, "server is shutting down")
	case errors.As(err, &vd):
		writeError(w, http.StatusBadRequest, "invalid scenario", vd.ValidationDetails()...)
	case len(fieldErrors(err)) > 0:
		writeError(w, http.StatusBadRequest, "invalid scenario", validationMessages(err)...)
	case strings.HasPrefix(err.Error(), "invalid scenario"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.LogAttrs(r.Context(), slog.LevelError, "request failed",
			slog.String("method", r.Method), slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// decodeJSON decodes a JSON request body (at most 1 MiB) into dst,
// rejecting unknown fields, empty bodies and trailing data.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSONOptional(w, r, dst, false)
}

// decodeJSONOptional is decodeJSON that, when allowEmpty is set, accepts
// an empty body and leaves dst unchanged.
func decodeJSONOptional(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		var se *json.SyntaxError
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &mbe):
			return err
		case errors.Is(err, io.EOF):
			if allowEmpty {
				return nil
			}
			return badRequest("request body must not be empty")
		case errors.As(err, &se):
			return badRequest("malformed JSON at offset %d", se.Offset)
		case errors.Is(err, io.ErrUnexpectedEOF):
			return badRequest("malformed JSON")
		case errors.As(err, &te):
			return badRequest("invalid type for field %q", te.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return badRequest("unknown field %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
		default:
			return badRequest("invalid JSON: %v", err)
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return badRequest("request body must contain a single JSON object")
	}
	return nil
}

// fieldErrors returns every *scenario.FieldError in err's tree (following
// both single and multi-error Unwrap).
func fieldErrors(err error) []*scenario.FieldError {
	var out []*scenario.FieldError
	walkLeaves(err, func(e error) {
		if fe, ok := e.(*scenario.FieldError); ok {
			out = append(out, fe)
		}
	})
	return out
}

// validationMessages flattens a (possibly joined/wrapped) validation error
// into one message per problem. Field errors are formatted "path: msg";
// other leaves (e.g. YAML decode errors) use the outermost wrapper's text
// so that context such as "decode: " is preserved.
func validationMessages(err error) []string {
	if err == nil {
		return nil
	}
	if fes := fieldErrors(err); len(fes) > 0 {
		msgs := make([]string, 0, len(fes))
		for _, fe := range fes {
			msgs = append(msgs, fe.Path+": "+fe.Msg)
		}
		return msgs
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var msgs []string
		for _, e := range j.Unwrap() {
			msgs = append(msgs, validationMessages(e)...)
		}
		return msgs
	}
	return []string{err.Error()}
}

// walkLeaves calls fn for err and every error reachable through Unwrap,
// stopping descent at *scenario.FieldError.
func walkLeaves(err error, fn func(error)) {
	if err == nil {
		return
	}
	fn(err)
	if _, ok := err.(*scenario.FieldError); ok {
		return
	}
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			walkLeaves(e, fn)
		}
	case interface{ Unwrap() error }:
		walkLeaves(u.Unwrap(), fn)
	}
}

// nonNil returns s, or an empty slice when s is nil, so that lists encode
// as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
