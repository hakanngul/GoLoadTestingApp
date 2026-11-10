package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
)

// tryEnv is an env whose runner is a real *runner.Manager (which
// implements Try) over the fake store.
func tryEnv(t *testing.T, allow string) *env {
	t.Helper()
	st := newFakeStore()
	opts := runner.Options{Store: st}
	if allow != "" {
		l, err := allowlist.Parse(allow)
		if err != nil {
			t.Fatal(err)
		}
		opts.Allow = l.Check
	}
	m := runner.NewManager(opts)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	s := newServer(Config{Store: st, Runner: m})
	return &env{h: s.handler(), srv: s, store: st}
}

func tryYAML(base, extra string) string {
	return fmt.Sprintf(`
name: try
target:
  base_url: %s
load:
  stages: [{duration: 1s, target: 1}]
%s
steps:
  - name: echo
    path: /echo?u={{user}}
    assert: [{type: status, value: "200"}]
`, base, extra)
}

func echoTarget(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "https://www.example.test/moved", http.StatusMovedPermanently)
			return
		}
		io.WriteString(w, "hello "+r.URL.Query().Get("u"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTryEndpoint(t *testing.T) {
	srv := echoTarget(t)
	e := tryEnv(t, "")

	rec := e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: tryYAML(srv.URL, "")})
	wantStatus(t, rec, http.StatusOK)
	res := decode[model.TryResult](t, rec)
	// No data block: {{user}} is unrendered and reported.
	if res.OK != true || len(res.Steps) != 1 || res.Steps[0].Body != "hello {{user}}" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Hints) == 0 || !strings.Contains(res.Hints[0].Message, "{{user}}") {
		t.Errorf("hints = %+v", res.Hints)
	}

	// Saved test with a data file: the first row is used.
	y := tryYAML(srv.URL, "data: {file: users.csv}")
	test, err := e.store.CreateTest(context.Background(), model.Test{Name: "x", YAML: y})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.PutFile(context.Background(), test.ID, "users.csv", []byte("user\nalice\nbob\n")); err != nil {
		t.Fatal(err)
	}
	res = decode[model.TryResult](t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: y, TestID: test.ID}))
	if !res.OK || res.Steps[0].Body != "hello alice" || res.Data["user"] != "alice" || len(res.Hints) != 0 {
		t.Fatalf("res = %+v", res)
	}
	// Unsaved: no data, with a note.
	res = decode[model.TryResult](t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: y}))
	if res.DataNote == "" || res.Steps[0].Body != "hello {{user}}" {
		t.Fatalf("res = %+v", res)
	}

	// Errors.
	wantStatus(t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: invalidYAML}), http.StatusBadRequest)
	wantStatus(t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: ""}), http.StatusBadRequest)
	wantStatus(t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: y, TestID: "nope"}), http.StatusNotFound)
}

func TestTryRedirectHintOverAPI(t *testing.T) {
	srv := echoTarget(t)
	e := tryEnv(t, "")
	// follow_redirects: false keeps the 3xx itself (the redirect target
	// does not exist); followed redirects are covered in package trial.
	y := strings.Replace(tryYAML(srv.URL, ""), "/echo?u={{user}}", "/moved\n    follow_redirects: false", 1)
	res := decode[model.TryResult](t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: y}))
	if res.OK || len(res.Hints) != 1 || res.Hints[0].Fix == nil || res.Hints[0].Fix.Value != "https://www.example.test" {
		t.Fatalf("res = %+v", res)
	}
}

func TestTryAllowlist(t *testing.T) {
	srv := echoTarget(t)
	e := tryEnv(t, "allowed.test")
	res := decode[model.TryResult](t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: tryYAML(srv.URL, "")}))
	if res.OK || res.Steps[0].State != model.TryNotRun || len(res.Hints) == 0 ||
		!strings.Contains(res.Hints[0].Message, `"127.0.0.1" is not on this server's allowlist`) {
		t.Fatalf("res = %+v", res)
	}
}

func TestTryConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(maxConcurrentTries)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Done()
		<-release
	}))
	defer slow.Close()
	defer close(release)

	e := tryEnv(t, "")
	y := tryYAML(slow.URL, "")
	var wg sync.WaitGroup
	for i := 0; i < maxConcurrentTries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/tests/try", strings.NewReader(fmt.Sprintf(`{"yaml":%q}`, y)))
			e.h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	started.Wait() // every slot is busy
	rec := e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: y})
	wantStatus(t, rec, http.StatusTooManyRequests)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	release <- struct{}{} // let one finish (the others follow on close)
	for i := 1; i < maxConcurrentTries; i++ {
		select {
		case release <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatal("stuck")
		}
	}
	wg.Wait()
}

func TestTryRequiresAuth(t *testing.T) {
	st := newFakeStore()
	m := runner.NewManager(runner.Options{Store: st})
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	h := NewServer(Config{Store: st, Runner: m, AuthToken: "secret"})
	req := httptest.NewRequest("POST", "/api/tests/try", strings.NewReader(`{"yaml":"x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestTryNotSupported(t *testing.T) {
	e := newEnv(t) // fakeRunner has no Try
	wantStatus(t, e.json(t, "POST", "/api/tests/try", model.TryRequest{YAML: validYAML}), http.StatusNotImplemented)
}

func TestValidateWarnings(t *testing.T) {
	e := newEnv(t)
	y := tryYAML("https://api.example.test", "data: {file: users.csv}")
	y = strings.Replace(y, "/echo?u={{user}}", "/echo?u={{user}}&t={{tenant}}", 1)

	// Header unknown: nothing to warn about.
	v := decode[model.ValidateResult](t, e.json(t, "POST", "/api/validate", model.ValidateRequest{YAML: y}))
	if !v.Valid || v.Warnings == nil || len(v.Warnings) != 0 {
		t.Fatalf("v = %+v", v)
	}
	test, _ := e.store.CreateTest(context.Background(), model.Test{Name: "w", YAML: y})
	_ = e.store.PutFile(context.Background(), test.ID, "users.csv", []byte("\uFEFFuser\nalice\n"))
	v = decode[model.ValidateResult](t, e.json(t, "POST", "/api/validate", model.ValidateRequest{YAML: y, TestID: test.ID}))
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "steps[0].path: {{tenant}} is not defined") {
		t.Fatalf("warnings = %v", v.Warnings)
	}
	// Invalid scenarios still answer warnings: [].
	v = decode[model.ValidateResult](t, e.json(t, "POST", "/api/validate", model.ValidateRequest{YAML: invalidYAML}))
	if v.Valid || v.Warnings == nil {
		t.Fatalf("v = %+v", v)
	}
}
