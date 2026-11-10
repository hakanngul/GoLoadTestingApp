package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// testTarget serves a login → me flow so the full pipeline is exercised:
// YAML → CSV feeder → engine → HTTP executor → metrics → thresholds → exit code.
func testTarget(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body struct{ Username string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"token":"tok-%s"}}`, body.Username)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func writeScenario(t *testing.T, baseURL, thresholdP95 string) string {
	t.Helper()
	dir := t.TempDir()
	csv := "username,password\nalice,pw1\nbob,pw2\n"
	if err := os.WriteFile(filepath.Join(dir, "users.csv"), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`name: e2e
target:
  base_url: %s
load:
  model: open
  stages:
    - duration: 2s
      target: 50
data:
  file: users.csv
steps:
  - name: login
    method: POST
    path: /login
    headers:
      Content-Type: application/json
    body: '{"username":"{{username}}","password":"{{password}}"}'
    extract:
      - var: token
        from: json
        expr: data.token
    assert:
      - type: status
        value: "200"
  - name: me
    path: /me
    headers:
      Authorization: Bearer {{token}}
    assert:
      - type: json
        expr: ok
        value: "true"
thresholds:
  - metric: error_rate
    op: lt
    value: 0.01
  - metric: latency.p95
    op: lt
    value: %s
`, baseURL, thresholdP95)
	path := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunEndToEndPass(t *testing.T) {
	srv, hits := testTarget(t)
	path := writeScenario(t, srv.URL, "1000")
	report := filepath.Join(t.TempDir(), "out.json")

	var stdout, stderr bytes.Buffer
	code := dispatch([]string{"run", "-quiet", "-out", report, path}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	// 2s ramp 0→50 rps ≈ 50 iterations × 2 requests.
	if got := hits.Load(); got < 80 || got > 120 {
		t.Errorf("server hits = %d, want ≈100", got)
	}
	var rep jsonReport
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.Summary.Errors != 0 {
		t.Errorf("report: passed=%v errors=%d", rep.Passed, rep.Summary.Errors)
	}
	if _, ok := rep.Summary.ByStep["login"]; !ok {
		t.Errorf("missing per-step stats: %v", rep.Summary.ByStep)
	}
}

func TestRunThresholdFailExitCode(t *testing.T) {
	srv, _ := testTarget(t)
	// p95 < 0.001 ms can never pass.
	path := writeScenario(t, srv.URL, "0.001")
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"run", "-quiet", path}, &stdout, &stderr); code != exitThreshold {
		t.Fatalf("exit %d, want %d\n%s%s", code, exitThreshold, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "FAIL") {
		t.Errorf("summary does not mark failure:\n%s", stdout.String())
	}
}

func TestValidate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, f := range []string{"smoke.yaml", "login_flow.yaml", "spike.yaml"} {
		stdout.Reset()
		stderr.Reset()
		if code := dispatch([]string{"validate", filepath.Join("..", "..", "examples", f)}, &stdout, &stderr); code != exitOK {
			t.Errorf("%s: exit %d: %s", f, code, stderr.String())
		}
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(bad, []byte("name: x\ntarget: {base_url: nope}\nload: {stages: []}\nsteps: []\n"), 0o644)
	if code := dispatch([]string{"validate", bad}, &stdout, &stderr); code != exitUsage {
		t.Errorf("bad scenario: exit %d, want %d", code, exitUsage)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"nope"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestPrintErrorGroups(t *testing.T) {
	var b bytes.Buffer
	printErrorGroups(&b, nil, 10)
	if b.Len() != 0 {
		t.Fatalf("printed %q for no errors", b.String())
	}
	var gs []model.ErrorGroup
	for i := 0; i < 12; i++ {
		gs = append(gs, model.ErrorGroup{Step: fmt.Sprintf("s%d", i), Status: 500, Message: "boom", Count: int64(20 - i)})
	}
	printErrorGroups(&b, gs, 10)
	out := b.String()
	if !strings.Contains(out, "== errors ==") || !strings.Contains(strings.Join(strings.Fields(out), " "), "20 s0 500 boom") ||
		!strings.Contains(out, "(2 more groups)") || strings.Contains(out, "s10") {
		t.Fatalf("output:\n%s", out)
	}
}
