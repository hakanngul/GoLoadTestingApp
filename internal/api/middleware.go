package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// statusWriter records the response status and size for logging. It keeps
// streaming working: Flush is forwarded and Unwrap exposes the underlying
// writer to http.ResponseController.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (sw *statusWriter) WriteHeader(code int) {
	if sw.status == 0 {
		sw.status = code
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	n, err := sw.ResponseWriter.Write(b)
	sw.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher.
func (sw *statusWriter) Flush() {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the wrapped writer (used by http.ResponseController).
func (sw *statusWriter) Unwrap() http.ResponseWriter { return sw.ResponseWriter }

// logRequests logs method, path, status and duration of every request.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			s.log.LogAttrs(r.Context(), levelFor(status), "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)),
			)
		}()
		next.ServeHTTP(sw, r)
	})
}

// recoverPanics turns a handler panic into a 500 response (when nothing
// has been written yet) and logs the stack. http.ErrAbortHandler is
// re-raised so net/http can abort the connection as intended.
func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			s.log.Error("panic in handler", "method", r.Method, "path", r.URL.Path,
				"panic", v, "stack", string(debug.Stack()))
			if sw.status == 0 {
				writeError(sw, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// cors allows the Vite dev server origin and answers preflight requests.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == devOrigin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", devOrigin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth authenticates /api/* requests (except /api/health) and
// enforces the scope the matched route needs. The master token
// (Config.AuthToken) is admin; any other bearer secret is looked up as an
// API token. With auth disabled every request runs as admin. The caller's
// model.Identity is put in the request context.
func (s *server) requireAuth(next http.Handler) http.Handler {
	if s.authToken == "" {
		anon := model.Identity{Kind: "none", Scope: model.ScopeAdmin}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(model.WithIdentity(r.Context(), anon)))
		})
	}
	want := []byte(s.authToken)
	master := model.Identity{Kind: "master", Name: "admin", Scope: model.ScopeAdmin}
	deny := func(w http.ResponseWriter) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
		writeError(w, http.StatusUnauthorized, "unauthorized")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		got := ""
		if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
			got = strings.TrimSpace(h[7:])
		} else if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/stream") {
			// EventSource cannot send headers.
			got = r.URL.Query().Get("access_token")
		}
		var id model.Identity
		switch {
		case got == "":
			deny(w)
			return
		case subtle.ConstantTimeCompare([]byte(got), want) == 1:
			id = master
		case s.tokens != nil && strings.HasPrefix(got, tokenSecretPrefix):
			tok, ok, err := s.tokens.lookup(r.Context(), got, time.Now())
			if err != nil {
				s.log.Error("api token lookup failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			if !ok {
				deny(w)
				return
			}
			id = model.Identity{Kind: "token", Name: tok.Name, Scope: tok.Scope}
		default:
			deny(w)
			return
		}
		// Routes the mux does not know answer 404/405 later; only matched
		// routes have a scope to enforce.
		if _, pattern := s.api.Handler(r); pattern != "" {
			if need := s.scopes[pattern]; !id.Scope.Includes(need) {
				writeError(w, http.StatusForbidden,
					fmt.Sprintf("this token has scope %q; %q is needed", id.Scope, need))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(model.WithIdentity(r.Context(), id)))
	})
}

// levelFor logs server errors at Error, client errors at Warn and the
// rest at Info.
func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
