package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/report"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// Config configures the HTTP server returned by NewServer.
type Config struct {
	// Store persists tests, runs and metrics. Required.
	Store store.Store
	// Runner starts, stops and streams runs. Required.
	Runner runner.Runner
	// UI is the built web UI (web/dist). When nil, every non-/api path
	// answers 404.
	UI fs.FS
	// Version is reported by GET /api/health.
	Version string
	// Logger receives request logs and internal errors. Nil discards logs.
	Logger *slog.Logger
	// AuthToken, when non-empty, requires "Authorization: Bearer <token>"
	// on every /api/* route except /api/health. Because browsers cannot set
	// headers on EventSource, the SSE stream route additionally accepts the
	// token in the "access_token" query parameter.
	AuthToken string
	// DevCORS allows cross-origin requests from the Vite dev server
	// (http://localhost:5173). Leave false in production.
	DevCORS bool
}

// Limits on request bodies.
const (
	maxJSONBody = 1 << 20  // 1 MiB
	maxFileBody = 50 << 20 // 50 MiB

	defaultRunLimit = 50
	maxRunLimit     = 500

	maxTestNameLen = 200

	defaultHeartbeat = 15 * time.Second

	devOrigin = "http://localhost:5173"
)

// server holds the dependencies shared by all handlers.
type server struct {
	store     store.Store
	runner    runner.Runner
	ui        fs.FS
	version   string
	log       *slog.Logger
	authToken string
	devCORS   bool
	reporter  *report.Reporter

	// heartbeat is the SSE keep-alive interval (overridable in tests).
	heartbeat time.Duration

	api *http.ServeMux
}

// NewServer returns the HTTP handler serving the REST API under /api, the
// SSE live stream and the embedded UI for every other path. The route
// contract is documented in the package comment (doc.go).
func NewServer(cfg Config) http.Handler {
	return newServer(cfg).handler()
}

func newServer(cfg Config) *server {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		store:     cfg.Store,
		runner:    cfg.Runner,
		ui:        cfg.UI,
		version:   cfg.Version,
		log:       log,
		authToken: cfg.AuthToken,
		devCORS:   cfg.DevCORS,
		reporter:  report.New(cfg.Store),
		heartbeat: defaultHeartbeat,
		api:       http.NewServeMux(),
	}
	s.routes()
	return s
}

// routes registers every /api route on s.api.
func (s *server) routes() {
	m := s.api
	m.HandleFunc("GET /api/health", s.handleHealth)

	m.HandleFunc("GET /api/tests", s.handleListTests)
	m.HandleFunc("POST /api/tests", s.handleCreateTest)
	m.HandleFunc("GET /api/tests/{id}", s.handleGetTest)
	m.HandleFunc("PUT /api/tests/{id}", s.handleUpdateTest)
	m.HandleFunc("DELETE /api/tests/{id}", s.handleDeleteTest)
	m.HandleFunc("PUT /api/tests/{id}/files/{name}", s.handlePutFile)
	m.HandleFunc("DELETE /api/tests/{id}/files/{name}", s.handleDeleteFile)
	m.HandleFunc("POST /api/validate", s.handleValidate)

	m.HandleFunc("POST /api/tests/{id}/runs", s.handleStartRun)
	m.HandleFunc("GET /api/runs", s.handleListRuns)
	m.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	m.HandleFunc("POST /api/runs/{id}/stop", s.handleStopRun)
	m.HandleFunc("GET /api/runs/{id}/metrics", s.handleRunMetrics)
	m.HandleFunc("GET /api/runs/{id}/errors", s.handleRunErrors)
	m.HandleFunc("GET /api/runs/{id}/stream", s.handleStream)

	m.HandleFunc("GET /api/workers", s.handleWorkers)

	// Reporting.
	m.HandleFunc("PATCH /api/runs/{id}", s.handleUpdateRun)
	m.HandleFunc("DELETE /api/runs/{id}", s.handleDeleteRun)
	m.HandleFunc("GET /api/runs/{id}/report", s.handleRunReport)
	m.HandleFunc("GET /api/runs/{id}/report.html", s.handleRunReportHTML)
	m.HandleFunc("GET /api/runs/{id}/export.csv", s.handleExportCSV)
	m.HandleFunc("GET /api/runs/{id}/export.json", s.handleExportJSON)
	m.HandleFunc("GET /api/compare", s.handleCompare)
	m.HandleFunc("GET /api/tests/{id}/trend", s.handleTrend)
	m.HandleFunc("PUT /api/tests/{id}/baseline", s.handleSetBaseline)
	m.HandleFunc("GET /api/reports/overview", s.handleOverview)
}

// handler assembles the middleware chain:
// log -> recover -> CORS -> (auth -> API mux | UI).
func (s *server) handler() http.Handler {
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			s.requireAuth(http.HandlerFunc(s.serveAPI)).ServeHTTP(w, r)
			return
		}
		s.serveUI(w, r)
	})
	var h http.Handler = root
	if s.devCORS {
		h = cors(h)
	}
	h = s.recoverPanics(h)
	h = s.logRequests(h)
	return h
}

func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

// serveAPI dispatches to the API mux, turning the mux's plain-text 404/405
// replies into JSON model.APIError bodies.
func (s *server) serveAPI(w http.ResponseWriter, r *http.Request) {
	if _, pattern := s.api.Handler(r); pattern != "" {
		s.api.ServeHTTP(w, r)
		return
	}
	// No route matched: let the mux decide (404, 405 with Allow, or a
	// path-cleaning redirect), capture the outcome and re-emit it.
	rec := &captureWriter{header: http.Header{}, status: http.StatusOK}
	s.api.ServeHTTP(rec, r)
	for _, k := range []string{"Allow", "Location"} {
		if v := rec.header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	switch rec.status {
	case http.StatusNotFound:
		writeError(w, http.StatusNotFound, "not found")
	case http.StatusMethodNotAllowed:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		w.WriteHeader(rec.status)
	}
}

// captureWriter records the status and headers of a response, discarding
// the body.
type captureWriter struct {
	header http.Header
	status int
	wrote  bool
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(code int) {
	if !c.wrote {
		c.status, c.wrote = code, true
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	return len(b), nil
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func (s *server) handleWorkers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(s.runner.Workers()))
}
