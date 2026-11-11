package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
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
	// PublicURL is used in links returned to the agent (serve -public-url).
	// Empty = derived from the request (X-Forwarded-Proto/-Host or Host).
	PublicURL string
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
		pub := opts.PublicURL
		if pub == "" {
			pub = requestBaseURL(r)
		}
		// A read-scoped token cannot change anything, so it does not see
		// the mutating tools at all.
		readOnly := opts.ReadOnly || r.Context().Value(scopeKey{}) == "read"
		return New(Options{
			Client:  InProcessClient(api, bearerToken(r)),
			Version: opts.Version, ReadOnly: readOnly, PublicURL: pub,
			MaxUsers: opts.MaxUsers, MaxDuration: opts.MaxDuration,
			Guide: opts.Guide, CreateTestPrompt: opts.CreateTestPrompt,
			PollInterval: opts.PollInterval,
		})
	}
	inner := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: log,
		// The SDK's DNS-rebinding guard rejects requests that reach a
		// loopback address with a non-local Host header. That also blocks a
		// reverse proxy on the same host (nginx/Caddy -> 127.0.0.1). With
		// bearer auth a rebinding page cannot authenticate anyway, so the
		// guard stays on only when /mcp is open.
		DisableLocalhostProtection: opts.AuthRequired,
	})
	if !opts.AuthRequired {
		return inner
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			unauthorized(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		status, scope := checkToken(api, r)
		if status != http.StatusOK {
			unauthorized(w, status, "token rejected")
			return
		}
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scopeKey{}, scope)))
	})
}

func unauthorized(w http.ResponseWriter, status int, msg string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="goload-mcp"`)
	}
	http.Error(w, msg, status)
}

// scopeKey carries the caller's token scope ("read", "run", ...) from the
// auth check to getServer.
type scopeKey struct{}

// checkToken asks the API whether the caller's token is acceptable: GET
// /api/me, or GET /api/tests on servers that do not have /api/me yet.
// It returns 200 and the token's scope ("" when unknown) when accepted,
// else the status to answer with.
func checkToken(api http.Handler, r *http.Request) (int, string) {
	for _, path := range []string{"/api/me", "/api/tests"} {
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(r.Context())
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		switch {
		case rec.Code >= 200 && rec.Code < 300:
			var me struct {
				Scope string `json:"scope"`
			}
			if path == "/api/me" {
				_ = json.Unmarshal(rec.Body.Bytes(), &me)
			}
			return http.StatusOK, me.Scope
		case rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed:
			if path == "/api/me" {
				continue // API without /api/me: fall back
			}
			return http.StatusBadGateway, ""
		case rec.Code == http.StatusUnauthorized, rec.Code == http.StatusForbidden:
			return rec.Code, ""
		default:
			return http.StatusBadGateway, ""
		}
	}
	return http.StatusBadGateway, ""
}

// requestBaseURL is the URL the caller used to reach this server, as far
// as a reverse proxy lets us tell: X-Forwarded-Proto / X-Forwarded-Host,
// else the request's TLS state and Host.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); p == "http" || p == "https" {
		scheme = p
	}
	host := r.Host
	if h := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); h != "" {
		host = h
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
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
	// The URL never reaches the network: inProcessTransport hands every
	// request to api directly.
	c := apiclient.New("https://goload.inprocess", token)
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
