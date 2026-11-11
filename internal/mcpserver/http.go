package mcpserver

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/apiclient"
)

// HTTPPath is where the server mounts the Streamable HTTP MCP endpoint.
const HTTPPath = "/mcp"

// HTTPOptions configures HTTPHandler.
type HTTPOptions struct {
	// Version, ReadOnly, MaxUsers, MaxDuration, Guide, CreateTestPrompt and
	// PollInterval are passed to New for every request's server.
	Version          string
	ReadOnly         bool
	MaxUsers         int
	MaxDuration      time.Duration
	Guide            string
	CreateTestPrompt func(url string) string
	PollInterval     time.Duration
	// AuthRequired is true when the API enforces a bearer token. Requests
	// without a token the API accepts are then answered 401 before any MCP
	// processing.
	AuthRequired bool
	// Logger receives SDK warnings (nil = slog.Default()).
	Logger *slog.Logger
}

// HTTPHandler returns the MCP Streamable HTTP handler. Every tool call goes
// to api in-process (no network loopback) carrying the caller's own
// Authorization header, so the API's auth and per-token scopes apply as if
// the agent had called the REST API directly.
//
// The handler is stateless with JSON responses: no session is kept between
// requests, a fresh mcp.Server is built per request (the tools hold no
// state), and any instance behind a load balancer can answer any request.
func HTTPHandler(api http.Handler, opts HTTPOptions) http.Handler {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	getServer := func(r *http.Request) *mcp.Server {
		return New(Options{
			Client:  InProcessClient(api, bearerToken(r)),
			Version: opts.Version, ReadOnly: opts.ReadOnly,
			MaxUsers: opts.MaxUsers, MaxDuration: opts.MaxDuration,
			Guide: opts.Guide, CreateTestPrompt: opts.CreateTestPrompt,
			PollInterval: opts.PollInterval,
		})
	}
	inner := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: log,
	})
	if !opts.AuthRequired {
		return inner
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			unauthorized(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if status := checkToken(api, r); status != http.StatusOK {
			unauthorized(w, status, "token rejected")
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, status int, msg string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="goload-mcp"`)
	}
	http.Error(w, msg, status)
}

// checkToken asks the API whether the caller's token is acceptable: GET
// /api/me, or GET /api/tests on servers that do not have /api/me yet.
// It returns 200 when accepted, else the status to answer with.
func checkToken(api http.Handler, r *http.Request) int {
	for _, path := range []string{"/api/me", "/api/tests"} {
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(r.Context())
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		switch {
		case rec.Code >= 200 && rec.Code < 300:
			return http.StatusOK
		case rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed:
			if path == "/api/me" {
				continue // API without /api/me: fall back
			}
			return http.StatusBadGateway
		case rec.Code == http.StatusUnauthorized, rec.Code == http.StatusForbidden:
			return rec.Code
		default:
			return http.StatusBadGateway
		}
	}
	return http.StatusBadGateway
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// InProcessClient returns an apiclient.Client that sends its requests to
// api directly instead of over the network, authenticated with token.
func InProcessClient(api http.Handler, token string) *apiclient.Client {
	c := apiclient.New("http://goload.inprocess", token)
	c.HTTP = &http.Client{Transport: &inProcessTransport{api: api}}
	return c
}

// inProcessTransport is an http.RoundTripper that invokes a handler and
// records its answer. Responses are buffered, so it suits request/response
// API calls, not event streams.
type inProcessTransport struct{ api http.Handler }

func (t *inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body io.Reader = http.NoBody
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	in := httptest.NewRequest(req.Method, req.URL.RequestURI(), body).WithContext(req.Context())
	in.Header = req.Header.Clone()
	rec := httptest.NewRecorder()
	t.api.ServeHTTP(rec, in)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}
