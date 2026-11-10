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
	"sync"
	"sync/atomic"
	"testing"
)

// TestRunAuthLoginEndToEnd runs a per_user login scenario with CSV users
// through the CLI: every user logs in once, every step carries the token.
func TestRunAuthLoginEndToEnd(t *testing.T) {
	var logins, unauthorized, ok atomic.Int64
	var mu sync.Mutex
	tokens := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Password != "pw-"+in.Username {
			http.Error(w, "bad credentials", http.StatusForbidden)
			return
		}
		logins.Add(1)
		tok := "tok-" + in.Username
		mu.Lock()
		tokens[tok] = true
		mu.Unlock()
		fmt.Fprintf(w, `{"data":{"access_token":%q}}`, tok)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		valid := tokens[tok]
		mu.Unlock()
		if !valid {
			unauthorized.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ok.Add(1)
		fmt.Fprint(w, `{"id":1}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	csv := "username,password\nu1,pw-u1\nu2,pw-u2\nu3,pw-u3\n"
	if err := os.WriteFile(filepath.Join(dir, "users.csv"), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf(`name: auth-e2e
target:
  base_url: %s
load:
  model: closed
  stages:
    - duration: 1s
      target: 3
data:
  file: users.csv
auth:
  type: login
  login:
    path: /login
    body: '{"username":"{{username}}","password":"{{password}}"}'
    extract:
      - {var: token, from: json, expr: data.access_token}
steps:
  - name: me
    path: /me
    think_time: 20ms
thresholds:
  - {metric: error_rate, op: lt, value: 0.01}
`, srv.URL)
	path := filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"run", "-quiet", path}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if got := logins.Load(); got != 3 {
		t.Errorf("logins = %d, want 3 (one per CSV user)", got)
	}
	if ok.Load() < 10 || unauthorized.Load() != 0 {
		t.Errorf("authenticated requests = %d, unauthorized = %d", ok.Load(), unauthorized.Load())
	}
	for _, step := range []string{"auth:login", "me"} {
		if !strings.Contains(stdout.String(), step) {
			t.Errorf("summary lacks step %q:\n%s", step, stdout.String())
		}
	}
}
