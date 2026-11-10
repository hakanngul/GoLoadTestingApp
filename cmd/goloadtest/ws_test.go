package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// TestRunWebSocketEndToEnd drives a mixed HTTP login -> WebSocket scenario
// through the CLI, i.e. through the same executor factory the master and
// workers use.
func TestRunWebSocketEndToEnd(t *testing.T) {
	var joins atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"abc"}`)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "abc" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			if strings.Contains(string(msg), "join") {
				joins.Add(1)
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"joined"}`))
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	yaml := fmt.Sprintf(`name: ws-e2e
target:
  base_url: %s
load:
  model: closed
  stages:
    - duration: 1s
      target: 5
steps:
  - name: login
    method: POST
    path: /login
    extract:
      - {var: token, from: json, expr: token}
  - name: chat
    type: ws
    path: /ws?token={{token}}
    ws:
      messages:
        - send: '{"type":"join"}'
          expect: joined
          timeout: 2s
      hold: 100ms
thresholds:
  - {metric: error_rate, op: lt, value: 0.01}
`, srv.URL)
	path := filepath.Join(t.TempDir(), "ws.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"run", "-quiet", path}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if joins.Load() == 0 {
		t.Fatal("server never saw a join over WebSocket")
	}
	for _, step := range []string{"login", "chat", "chat:1"} {
		if !strings.Contains(stdout.String(), step) {
			t.Errorf("summary lacks step %q:\n%s", step, stdout.String())
		}
	}
}
