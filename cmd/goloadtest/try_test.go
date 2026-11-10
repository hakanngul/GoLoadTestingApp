package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTryCommand(t *testing.T) {
	srv, _ := testTarget(t) // POST /login -> token, GET /me needs it
	path := writeScenario(t, srv.URL, "5000")
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"try", path}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"data row: password=*** username=alice", "all steps passed", "extract ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// A redirect that is not followed (follow_redirects: false; its
	// target does not exist) fails with a hint and exit 1.
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.example.test/", http.StatusMovedPermanently)
	}))
	defer moved.Close()
	dir := t.TempDir()
	p := filepath.Join(dir, "s.yaml")
	y := fmt.Sprintf("name: m\ntarget: {base_url: %s}\nload: {stages: [{duration: 1s, target: 1}]}\nsteps:\n  - {name: home, path: /, follow_redirects: false, assert: [{type: status, value: '200'}]}\n  - {name: next, path: /x}\n", moved.URL)
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := dispatch([]string{"try", p}, &stdout, &stderr); code != exitThreshold {
		t.Fatalf("exit %d\n%s", code, stdout.String())
	}
	out = stdout.String()
	for _, want := range []string{"redirects to https://www.example.test/", "fix: Set base_url to https://www.example.test", "not run", "FAILED"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	stdout.Reset()
	if code := dispatch([]string{"try", "-json", p}, &stdout, &stderr); code != exitThreshold || !strings.Contains(stdout.String(), `"kind": "base_url"`) {
		t.Errorf("json exit %d:\n%s", code, stdout.String())
	}
}

func TestValidateWarnsUndefinedVars(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "u.csv"), []byte("user\nalice\n"), 0o644)
	p := filepath.Join(dir, "s.yaml")
	y := "name: v\ntarget: {base_url: 'https://h.test'}\nload: {stages: [{duration: 1s, target: 1}]}\ndata: {file: u.csv}\nsteps:\n  - {name: a, path: '/x/{{user}}/{{nope}}'}\n"
	os.WriteFile(p, []byte(y), 0o644)
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"validate", p}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "warning: steps[0].path: {{nope}} is not defined") || strings.Contains(stdout.String(), "{{user}}") {
		t.Errorf("stdout:\n%s", stdout.String())
	}
}
