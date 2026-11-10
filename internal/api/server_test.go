package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

const validYAML = `
name: t
target:
  base_url: https://api.example.test
load:
  stages:
    - duration: 10s
      target: 5
    - duration: 20s
      target: 5
steps:
  - path: /health
  - path: /items
`

const invalidYAML = `
name: ""
target:
  base_url: ftp://x
load:
  stages:
    - duration: 10s
      target: 5
steps:
  - path: /health
`

type env struct {
	h      http.Handler
	srv    *server
	store  *fakeStore
	runner *fakeRunner
}

func newEnv(t *testing.T, mutate ...func(*Config)) *env {
	t.Helper()
	st := newFakeStore()
	rn := newFakeRunner(st)
	cfg := Config{Store: st, Runner: rn, Version: "v1.2.3"}
	for _, m := range mutate {
		m(&cfg)
	}
	s := newServer(cfg)
	return &env{h: s.handler(), srv: s, store: st, runner: rn}
}

func (e *env) do(t *testing.T, method, path string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) json(t *testing.T, method, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	switch v := v.(type) {
	case nil:
	case string:
		body = strings.NewReader(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(b))
	}
	return e.do(t, method, path, body, "Content-Type", "application/json")
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, code, rec.Body.String())
	}
}

// wantAPIError asserts a JSON APIError response with the given status.
func wantAPIError(t *testing.T, rec *httptest.ResponseRecorder, code int, contains string) model.APIError {
	t.Helper()
	wantStatus(t, rec, code)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON", ct)
	}
	e := decode[model.APIError](t, rec)
	if !strings.Contains(e.Error, contains) {
		t.Fatalf("error = %q, want it to contain %q", e.Error, contains)
	}
	return e
}

func (e *env) createTest(t *testing.T, name string) model.Test {
	t.Helper()
	rec := e.json(t, "POST", "/api/tests", map[string]string{"name": name, "yaml": validYAML})
	wantStatus(t, rec, http.StatusCreated)
	return decode[model.Test](t, rec)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "GET", "/api/health", nil)
	wantStatus(t, rec, 200)
	got := decode[map[string]string](t, rec)
	if got["status"] != "ok" || got["version"] != "v1.2.3" {
		t.Fatalf("health = %v", got)
	}
}

func TestTestsCRUD(t *testing.T) {
	e := newEnv(t)

	rec := e.do(t, "GET", "/api/tests", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %s, want []", rec.Body.String())
	}

	created := e.createTest(t, "  checkout  ")
	if created.ID == "" || created.Name != "checkout" || created.Files == nil {
		t.Fatalf("created = %+v", created)
	}

	rec = e.do(t, "GET", "/api/tests/"+created.ID, nil)
	wantStatus(t, rec, 200)
	if got := decode[model.Test](t, rec); got.Name != "checkout" {
		t.Fatalf("get = %+v", got)
	}

	rec = e.json(t, "PUT", "/api/tests/"+created.ID, map[string]string{"name": "checkout2", "yaml": validYAML})
	wantStatus(t, rec, 200)
	if got := decode[model.Test](t, rec); got.Name != "checkout2" {
		t.Fatalf("update = %+v", got)
	}

	rec = e.do(t, "GET", "/api/tests", nil)
	wantStatus(t, rec, 200)
	if got := decode[[]model.Test](t, rec); len(got) != 1 {
		t.Fatalf("list = %+v", got)
	}

	wantStatus(t, e.do(t, "DELETE", "/api/tests/"+created.ID, nil), http.StatusNoContent)
	wantAPIError(t, e.do(t, "GET", "/api/tests/"+created.ID, nil), 404, "not found")
	wantAPIError(t, e.do(t, "DELETE", "/api/tests/"+created.ID, nil), 404, "not found")
	wantAPIError(t, e.json(t, "PUT", "/api/tests/nope", map[string]string{"name": "x", "yaml": validYAML}), 404, "not found")
}

func TestCreateTestErrors(t *testing.T) {
	e := newEnv(t)
	e.createTest(t, "dup")

	t.Run("duplicate name", func(t *testing.T) {
		rec := e.json(t, "POST", "/api/tests", map[string]string{"name": "dup", "yaml": validYAML})
		wantAPIError(t, rec, 409, "test name already exists")
	})
	t.Run("duplicate name on update", func(t *testing.T) {
		other := e.createTest(t, "other")
		rec := e.json(t, "PUT", "/api/tests/"+other.ID, map[string]string{"name": "dup", "yaml": validYAML})
		wantAPIError(t, rec, 409, "test name already exists")
	})
	t.Run("invalid yaml has details", func(t *testing.T) {
		rec := e.json(t, "POST", "/api/tests", map[string]string{"name": "bad", "yaml": invalidYAML})
		ae := wantAPIError(t, rec, 400, "invalid scenario")
		joined := strings.Join(ae.Details, "\n")
		if len(ae.Details) < 2 || !strings.Contains(joined, "name: must not be empty") ||
			!strings.Contains(joined, "target.base_url: ") {
			t.Fatalf("details = %q", ae.Details)
		}
	})
	t.Run("unparseable yaml", func(t *testing.T) {
		rec := e.json(t, "POST", "/api/tests", map[string]string{"name": "bad", "yaml": "steps: [\n"})
		ae := wantAPIError(t, rec, 400, "invalid scenario")
		if len(ae.Details) != 1 || !strings.Contains(ae.Details[0], "decode") {
			t.Fatalf("details = %q", ae.Details)
		}
	})
	t.Run("invalid yaml on update", func(t *testing.T) {
		other := e.createTest(t, "upd")
		rec := e.json(t, "PUT", "/api/tests/"+other.ID, map[string]string{"name": "upd", "yaml": invalidYAML})
		wantAPIError(t, rec, 400, "invalid scenario")
	})
	for _, name := range []string{"", "   ", strings.Repeat("x", 201)} {
		t.Run(fmt.Sprintf("bad name len %d", len(name)), func(t *testing.T) {
			rec := e.json(t, "POST", "/api/tests", map[string]string{"name": name, "yaml": validYAML})
			wantAPIError(t, rec, 400, "name must be between")
		})
	}
	t.Run("name 200 runes ok", func(t *testing.T) {
		e.createTest(t, strings.Repeat("é", 200))
	})
	t.Run("unknown field", func(t *testing.T) {
		rec := e.json(t, "POST", "/api/tests", `{"name":"x","yaml":"y","extra":1}`)
		wantAPIError(t, rec, 400, `unknown field "extra"`)
	})
	t.Run("malformed json", func(t *testing.T) {
		wantAPIError(t, e.json(t, "POST", "/api/tests", `{"name":`), 400, "malformed JSON")
	})
	t.Run("wrong type", func(t *testing.T) {
		wantAPIError(t, e.json(t, "POST", "/api/tests", `{"name":5}`), 400, "invalid type")
	})
	t.Run("trailing data", func(t *testing.T) {
		wantAPIError(t, e.json(t, "POST", "/api/tests", `{"name":"a"} {}`), 400, "single JSON object")
	})
	t.Run("empty body", func(t *testing.T) {
		wantAPIError(t, e.json(t, "POST", "/api/tests", ""), 400, "must not be empty")
	})
	t.Run("body too large", func(t *testing.T) {
		big := `{"name":"x","yaml":"` + strings.Repeat("a", maxJSONBody) + `"}`
		wantAPIError(t, e.json(t, "POST", "/api/tests", big), 413, "too large")
	})
}

func TestFiles(t *testing.T) {
	e := newEnv(t)
	tt := e.createTest(t, "files")
	base := "/api/tests/" + tt.ID + "/files/"

	wantStatus(t, e.do(t, "PUT", base+"users.csv", strings.NewReader("id\n1\n")), http.StatusNoContent)
	got, err := e.store.GetFile(context.Background(), tt.ID, "users.csv")
	if err != nil || string(got) != "id\n1\n" {
		t.Fatalf("stored file = %q, %v", got, err)
	}
	wantStatus(t, e.do(t, "PUT", base+"users.csv", strings.NewReader("id\n2\n")), http.StatusNoContent) // upsert

	for _, bad := range []string{"a..b", "..csv", "a%20b.csv", "a%2Fb.csv", strings.Repeat("a", 129), "%C3%A9.csv"} {
		t.Run("bad name "+bad, func(t *testing.T) {
			wantAPIError(t, e.do(t, "PUT", base+bad, strings.NewReader("x")), 400, "invalid file name")
		})
	}

	wantAPIError(t, e.do(t, "PUT", "/api/tests/nope/files/a.csv", strings.NewReader("x")), 404, "not found")

	t.Run("too large", func(t *testing.T) {
		rec := e.do(t, "PUT", base+"big.csv", io.LimitReader(zeroReader{}, maxFileBody+1))
		wantAPIError(t, rec, 413, "too large")
	})

	wantStatus(t, e.do(t, "DELETE", base+"users.csv", nil), http.StatusNoContent)
	wantAPIError(t, e.do(t, "DELETE", base+"users.csv", nil), 404, "not found")
	wantAPIError(t, e.do(t, "DELETE", base+"a..b", nil), 400, "invalid file name")
	// A bare ".." segment is cleaned by the mux (redirect) before routing;
	// the validator rejects it anyway.
	for _, n := range []string{"..", ".", ""} {
		if checkFileName(n) == nil {
			t.Fatalf("checkFileName(%q) accepted", n)
		}
	}
	if err := checkFileName("users-1_v2.csv"); err != nil {
		t.Fatal(err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestValidate(t *testing.T) {
	e := newEnv(t)
	rec := e.json(t, "POST", "/api/validate", map[string]string{"yaml": validYAML})
	wantStatus(t, rec, 200)
	got := decode[model.ValidateResult](t, rec)
	if !got.Valid || got.Model != "open" || got.Duration != 30 || got.Steps != 2 || got.Errors == nil {
		t.Fatalf("valid result = %+v", got)
	}

	rec = e.json(t, "POST", "/api/validate", map[string]string{"yaml": invalidYAML})
	wantStatus(t, rec, 200)
	got = decode[model.ValidateResult](t, rec)
	if got.Valid || len(got.Errors) < 2 {
		t.Fatalf("invalid result = %+v", got)
	}
	for _, msg := range got.Errors {
		if !strings.Contains(msg, ": ") {
			t.Fatalf("error %q not in path: msg form", msg)
		}
	}

	rec = e.json(t, "POST", "/api/validate", map[string]string{"yaml": ""})
	wantStatus(t, rec, 200)
	if got := decode[model.ValidateResult](t, rec); got.Valid || len(got.Errors) != 1 {
		t.Fatalf("empty result = %+v", got)
	}

	wantAPIError(t, e.json(t, "POST", "/api/validate", `{"yml":"x"}`), 400, "unknown field")
}

func TestRuns(t *testing.T) {
	e := newEnv(t)
	tt := e.createTest(t, "runs")

	rec := e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", map[string]int{"workers": 3})
	wantStatus(t, rec, http.StatusCreated)
	run := decode[model.Run](t, rec)
	if run.ID == "" || run.TestID != tt.ID || run.Status != model.RunRunning || e.runner.lastReq.Workers != 3 {
		t.Fatalf("run = %+v (req %+v)", run, e.runner.lastReq)
	}
	if !strings.Contains(rec.Body.String(), `"thresholds":[]`) {
		t.Fatalf("nil slices should encode as []: %s", rec.Body.String())
	}

	// Empty body is allowed and means defaults.
	wantStatus(t, e.do(t, "POST", "/api/tests/"+tt.ID+"/runs", nil), http.StatusCreated)
	wantAPIError(t, e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", `{"workers":-1}`), 400, "workers")
	wantAPIError(t, e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", `{"vus":1}`), 400, "unknown field")
	wantAPIError(t, e.json(t, "POST", "/api/tests/nope/runs", `{}`), 404, "not found")

	rec = e.do(t, "GET", "/api/runs/"+run.ID, nil)
	wantStatus(t, rec, 200)
	if got := decode[model.Run](t, rec); got.ID != run.ID {
		t.Fatalf("get run = %+v", got)
	}
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope", nil), 404, "not found")

	rec = e.do(t, "GET", "/api/runs?test_id="+tt.ID, nil)
	wantStatus(t, rec, 200)
	if got := decode[[]model.Run](t, rec); len(got) != 2 {
		t.Fatalf("list runs = %d", len(got))
	}
	if e.store.lastRF != (store.RunFilter{TestID: tt.ID, Limit: 50}) {
		t.Fatalf("filter = %+v", e.store.lastRF)
	}
	wantStatus(t, e.do(t, "GET", "/api/runs?limit=9999", nil), 200)
	if e.store.lastRF.Limit != 500 {
		t.Fatalf("limit = %d, want clamp to 500", e.store.lastRF.Limit)
	}
	wantStatus(t, e.do(t, "GET", "/api/runs?limit=7", nil), 200)
	if e.store.lastRF.Limit != 7 {
		t.Fatalf("limit = %d, want 7", e.store.lastRF.Limit)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		wantAPIError(t, e.do(t, "GET", "/api/runs?limit="+bad, nil), 400, "limit")
	}

	// Stop: active -> 202, unknown -> 404, finished -> 409.
	wantStatus(t, e.do(t, "POST", "/api/runs/"+run.ID+"/stop", nil), http.StatusAccepted)
	wantAPIError(t, e.do(t, "POST", "/api/runs/nope/stop", nil), 404, "not found")
	finished, _ := e.store.CreateRun(context.Background(), model.Run{TestID: tt.ID, Status: model.RunPassed})
	wantAPIError(t, e.do(t, "POST", "/api/runs/"+finished.ID+"/stop", nil), 409, "not active")

	// Metrics and error samples.
	ctx := context.Background()
	_ = e.store.AppendMetric(ctx, run.ID, model.MetricPoint{Elapsed: 1, Requests: 10}, nil)
	_ = e.store.AppendErrorSamples(ctx, run.ID, []model.ErrorSample{{Step: "s", Status: 500}})
	rec = e.do(t, "GET", "/api/runs/"+run.ID+"/metrics", nil)
	wantStatus(t, rec, 200)
	if got := decode[[]model.MetricPoint](t, rec); len(got) != 1 || got[0].Requests != 10 {
		t.Fatalf("metrics = %+v", got)
	}
	rec = e.do(t, "GET", "/api/runs/"+run.ID+"/errors", nil)
	wantStatus(t, rec, 200)
	if got := decode[[]model.ErrorSample](t, rec); len(got) != 1 || got[0].Status != 500 {
		t.Fatalf("errors = %+v", got)
	}
	rec = e.do(t, "GET", "/api/runs/"+finished.ID+"/metrics", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty metrics = %s", rec.Body.String())
	}
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/metrics", nil), 404, "not found")
	wantAPIError(t, e.do(t, "GET", "/api/runs/nope/errors", nil), 404, "not found")
}

func TestStartRunErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"target not allowed", fmt.Errorf("start: %w", runner.ErrTargetNotAllowed), 403, "target not allowed"},
		{"not running", runner.ErrNotRunning, 409, "not active"},
		{"field error", errors.Join(fieldErr("data.file", "file \"u.csv\" not uploaded")), 400, "invalid scenario"},
		{"prefixed", errors.New("invalid scenario: data file missing"), 400, "invalid scenario: data file missing"},
		{"detailer", detailErr{"a: b"}, 400, "invalid scenario"},
		{"internal", errors.New("boom secret"), 500, "internal server error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			tt := e.createTest(t, "x")
			e.runner.startErr = c.err
			rec := e.json(t, "POST", "/api/tests/"+tt.ID+"/runs", `{}`)
			ae := wantAPIError(t, rec, c.code, c.msg)
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal error leaked: %s", rec.Body.String())
			}
			if c.name == "field error" && (len(ae.Details) != 1 || ae.Details[0] != `data.file: file "u.csv" not uploaded`) {
				t.Fatalf("details = %q", ae.Details)
			}
		})
	}
}

type detailErr []string

func (d detailErr) Error() string               { return "validation failed" }
func (d detailErr) ValidationDetails() []string { return d }

func TestWorkers(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "GET", "/api/workers", nil)
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("workers = %s", rec.Body.String())
	}
	e.runner.workers = []model.Worker{{ID: "local", State: "idle"}}
	rec = e.do(t, "GET", "/api/workers", nil)
	if got := decode[[]model.Worker](t, rec); len(got) != 1 || got[0].ID != "local" {
		t.Fatalf("workers = %+v", got)
	}
}

func TestAPINotFoundAndMethodNotAllowed(t *testing.T) {
	e := newEnv(t)
	wantAPIError(t, e.do(t, "GET", "/api/nope", nil), 404, "not found")
	wantAPIError(t, e.do(t, "GET", "/api", nil), 404, "not found")
	rec := e.do(t, "PATCH", "/api/tests", nil)
	wantAPIError(t, rec, 405, "method not allowed")
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Fatalf("Allow = %q", allow)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AuthToken = "s3cret" })
	wantStatus(t, e.do(t, "GET", "/api/health", nil), 200)

	rec := e.do(t, "GET", "/api/tests", nil)
	wantAPIError(t, rec, 401, "unauthorized")
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
	}
	wantAPIError(t, e.do(t, "GET", "/api/tests", nil, "Authorization", "Bearer wrong"), 401, "unauthorized")
	wantAPIError(t, e.do(t, "GET", "/api/tests", nil, "Authorization", "s3cret"), 401, "unauthorized")
	wantAPIError(t, e.do(t, "GET", "/api/tests?access_token=s3cret", nil), 401, "unauthorized")
	wantAPIError(t, e.do(t, "GET", "/api/nope", nil), 401, "unauthorized") // don't leak routes
	wantStatus(t, e.do(t, "GET", "/api/tests", nil, "Authorization", "Bearer s3cret"), 200)
	wantStatus(t, e.do(t, "GET", "/api/tests", nil, "Authorization", "bearer s3cret"), 200)

	// The SSE route also accepts ?access_token= (EventSource can't set headers).
	run, _ := e.store.CreateRun(context.Background(), model.Run{Status: model.RunPassed})
	wantAPIError(t, e.do(t, "GET", "/api/runs/"+run.ID+"/stream", nil), 401, "unauthorized")
	wantStatus(t, e.do(t, "GET", "/api/runs/"+run.ID+"/stream?access_token=s3cret", nil), 200)

	// The UI is not protected (it holds no data).
	wantStatus(t, e.do(t, "GET", "/", nil), 404) // no UI configured
}

func TestCORS(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.DevCORS = true })
	rec := e.do(t, "OPTIONS", "/api/tests", nil, "Origin", devOrigin, "Access-Control-Request-Method", "POST")
	wantStatus(t, rec, http.StatusNoContent)
	if rec.Header().Get("Access-Control-Allow-Origin") != devOrigin {
		t.Fatalf("headers = %v", rec.Header())
	}
	rec = e.do(t, "GET", "/api/tests", nil, "Origin", "http://evil.example")
	wantStatus(t, rec, 200)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("foreign origin allowed")
	}

	off := newEnv(t)
	rec = off.do(t, "GET", "/api/tests", nil, "Origin", devOrigin)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS enabled without DevCORS")
	}
}

type panicRunner struct{ *fakeRunner }

func (panicRunner) Workers() []model.Worker { panic("kaboom") }

func TestPanicRecovery(t *testing.T) {
	st := newFakeStore()
	h := NewServer(Config{Store: st, Runner: panicRunner{newFakeRunner(st)}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workers", nil))
	wantAPIError(t, rec, 500, "internal server error")
}

func TestUI(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"assets/app-1a.js": {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
	}
	e := newEnv(t, func(c *Config) { c.UI = ui })

	cases := []struct {
		path, body, cache string
		code              int
	}{
		{"/", "<html>app</html>", "no-cache", 200},
		{"/runs/abc", "<html>app</html>", "no-cache", 200},     // SPA fallback
		{"/tests/1/edit", "<html>app</html>", "no-cache", 200}, // SPA fallback
		{"/assets/app-1a.js", "console.log(1)", "public, max-age=31536000, immutable", 200},
		{"/favicon.svg", "<svg/>", "no-cache", 200},
		{"/missing.js", "", "", 404},
		{"/assets/missing.css", "", "", 404},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := e.do(t, "GET", c.path, nil)
			wantStatus(t, rec, c.code)
			if c.code != 200 {
				return
			}
			if rec.Body.String() != c.body || rec.Header().Get("Cache-Control") != c.cache {
				t.Fatalf("body %q cache %q", rec.Body.String(), rec.Header().Get("Cache-Control"))
			}
		})
	}
	wantStatus(t, e.do(t, "POST", "/", nil), 405)
	// /api paths never fall back to the SPA.
	wantAPIError(t, e.do(t, "GET", "/api/unknown", nil), 404, "not found")
}

// --- helpers shared with stream tests ---

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
