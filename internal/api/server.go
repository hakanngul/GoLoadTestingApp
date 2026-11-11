package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
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
	// AuthToken, when non-empty, is the master token: "Authorization: Bearer
	// <token>" with admin scope on every /api/* route except /api/health.
	// Scoped API tokens (store.TokenStore, /api/tokens) are accepted next
	// to it. Because browsers cannot set
	// headers on EventSource, the SSE stream route additionally accepts the
	// token in the "access_token" query parameter.
	AuthToken string
	// DevCORS allows cross-origin requests from the Vite dev server
	// (http://localhost:5173). Leave false in production.
	DevCORS bool
	// Retention is the active data retention (serve -retention), reported
	// by GET /api/health; 0 = runs are kept forever.
	Retention time.Duration
	// Scheduler serves /api/schedules. nil answers those routes with 503.
	Scheduler Scheduler
	// Suites serves /api/suites and /api/suite-runs. nil answers those
	// routes with 503.
	Suites Suites
	// Notifier sends the test message of POST /api/notifications/{id}/test.
	// nil answers that route with 503. The channels themselves live in the
	// store (store.NotificationStore; 503 when it is not implemented).
	Notifier NotificationSender
}

// Limits on request bodies.
const (
	maxJSONBody = 1 << 20  // 1 MiB
	maxFileBody = 50 << 20 // 50 MiB

	defaultRunLimit = 50
	maxRunLimit     = 500

	maxTestNameLen = 200

	defaultHeartbeat = 15 * time.Second

	// maxConcurrentTries caps POST /api/tests/try requests in progress
	// server-wide; more answer 429.
	maxConcurrentTries = 4

	devOrigin = "http://localhost:5173"
)

// server holds the dependencies shared by all handlers.
type server struct {
	store     store.Store
	runner    runner.Runner
	ui        fs.FS
	uiFiles   map[string]string // regular files in ui, by path (see uiFileSet)
	version   string
	log       *slog.Logger
	authToken string
	devCORS   bool
	retention time.Duration
	reporter  *report.Reporter
	scheduler Scheduler
	suites    Suites
	notifier  NotificationSender
	tokens    *tokenAuth // nil when the store has no store.TokenStore

	// scopes maps every registered route pattern to the scope it needs.
	scopes map[string]model.TokenScope
	// patterns lists the registered route patterns in order.
	patterns []string

	// heartbeat is the SSE keep-alive interval (overridable in tests).
	heartbeat time.Duration
	// trySem holds one token per test request in progress.
	trySem chan struct{}

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
		uiFiles:   uiFileSet(cfg.UI),
		version:   cfg.Version,
		log:       log,
		authToken: cfg.AuthToken,
		devCORS:   cfg.DevCORS,
		retention: cfg.Retention,
		reporter:  report.New(cfg.Store),
		scheduler: cfg.Scheduler,
		suites:    cfg.Suites,
		notifier:  cfg.Notifier,
		heartbeat: defaultHeartbeat,
		trySem:    make(chan struct{}, maxConcurrentTries),
		api:       http.NewServeMux(),
		scopes:    make(map[string]model.TokenScope),
	}
	if ts, ok := cfg.Store.(store.TokenStore); ok {
		s.tokens = newTokenAuth(ts)
	}
	s.routes()
	return s
}

// handle registers one /api route and records the scope it needs (see
// routeScope).
func (s *server) handle(pattern string, h http.HandlerFunc) {
	s.scopes[pattern] = routeScope(pattern)
	s.patterns = append(s.patterns, pattern)
	s.api.HandleFunc(pattern, h)
}

// routes registers every /api route on s.api.
func (s *server) routes() {
	s.handle("GET /api/health", s.handleHealth)
	s.handle("GET /api/me", s.handleMe)

	// API tokens (admin only; 409 while auth is disabled).
	s.handle("GET /api/tokens", s.handleListTokens)
	s.handle("POST /api/tokens", s.handleCreateToken)
	s.handle("DELETE /api/tokens/{id}", s.handleDeleteToken)

	s.handle("GET /api/tests", s.handleListTests)
	s.handle("POST /api/tests", s.handleCreateTest)
	s.handle("GET /api/tests/{id}", s.handleGetTest)
	s.handle("PUT /api/tests/{id}", s.handleUpdateTest)
	s.handle("DELETE /api/tests/{id}", s.handleDeleteTest)
	s.handle("PUT /api/tests/{id}/files/{name}", s.handlePutFile)
	s.handle("DELETE /api/tests/{id}/files/{name}", s.handleDeleteFile)
	s.handle("POST /api/validate", s.handleValidate)
	s.handle("POST /api/tests/try", s.handleTry)

	s.handle("POST /api/tests/{id}/runs", s.handleStartRun)
	s.handle("GET /api/runs", s.handleListRuns)
	s.handle("GET /api/runs/{id}", s.handleGetRun)
	s.handle("POST /api/runs/{id}/stop", s.handleStopRun)
	s.handle("GET /api/runs/{id}/metrics", s.handleRunMetrics)
	s.handle("GET /api/runs/{id}/errors", s.handleRunErrors)
	s.handle("GET /api/runs/{id}/error-groups", s.handleRunErrorGroups)
	s.handle("GET /api/runs/{id}/stream", s.handleStream)

	s.handle("GET /api/workers", s.handleWorkers)

	// Reporting.
	s.handle("PATCH /api/runs/{id}", s.handleUpdateRun)
	s.handle("DELETE /api/runs/{id}", s.handleDeleteRun)
	s.handle("GET /api/runs/{id}/report", s.handleRunReport)
	s.handle("GET /api/runs/{id}/report.html", s.handleRunReportHTML)
	s.handle("GET /api/runs/{id}/export.csv", s.handleExportCSV)
	s.handle("GET /api/runs/{id}/export.json", s.handleExportJSON)
	s.handle("GET /api/compare", s.handleCompare)
	s.handle("GET /api/tests/{id}/trend", s.handleTrend)
	s.handle("PUT /api/tests/{id}/baseline", s.handleSetBaseline)
	s.handle("GET /api/reports/overview", s.handleOverview)

	// Schedules.
	s.handle("GET /api/schedules", s.handleListSchedules)
	s.handle("POST /api/schedules", s.handleCreateSchedule)
	s.handle("POST /api/schedules/preview", s.handlePreviewSchedule)
	s.handle("GET /api/schedules/{id}", s.handleGetSchedule)
	s.handle("PUT /api/schedules/{id}", s.handleUpdateSchedule)
	s.handle("DELETE /api/schedules/{id}", s.handleDeleteSchedule)
	s.handle("POST /api/schedules/{id}/run-now", s.handleRunScheduleNow)

	// Test suites.
	s.handle("GET /api/suites", s.withSuites(s.handleListSuites))
	s.handle("POST /api/suites", s.withSuites(s.handleCreateSuite))
	s.handle("GET /api/suites/{id}", s.withSuites(s.handleGetSuite))
	s.handle("PUT /api/suites/{id}", s.withSuites(s.handleUpdateSuite))
	s.handle("DELETE /api/suites/{id}", s.withSuites(s.handleDeleteSuite))
	s.handle("POST /api/suites/{id}/runs", s.withSuites(s.handleStartSuite))
	s.handle("GET /api/suites/{id}/runs", s.withSuites(s.handleListSuiteRuns))
	s.handle("GET /api/suite-runs", s.withSuites(s.handleListSuiteRuns))
	s.handle("GET /api/suite-runs/{id}", s.withSuites(s.handleGetSuiteRun))
	s.handle("POST /api/suite-runs/{id}/stop", s.withSuites(s.handleStopSuiteRun))

	// Notification channels.
	s.handle("GET /api/notifications", s.handleListNotifications)
	s.handle("POST /api/notifications", s.handleCreateNotification)
	s.handle("PUT /api/notifications/{id}", s.handleUpdateNotification)
	s.handle("DELETE /api/notifications/{id}", s.handleDeleteNotification)
	s.handle("POST /api/notifications/{id}/test", s.handleTestNotification)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": s.version,
		"retention_seconds": int64(s.retention / time.Second),
	})
}

func (s *server) handleWorkers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, nonNil(s.runner.Workers()))
}
