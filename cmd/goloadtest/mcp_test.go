package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseMCPOpts(t *testing.T) {
	t.Setenv("GOLOAD_SERVER", "")
	t.Setenv("GOLOAD_TOKEN", "from-env")
	var e bytes.Buffer
	o, code := parseMCPOpts([]string{"--server", "https://bob:pw@goload.example.com/base/?x=1#f", "--read-only", "--max-users", "7", "--max-duration", "90s"}, &e)
	if o == nil {
		t.Fatalf("code %d: %s", code, e.String())
	}
	if o.server != "https://goload.example.com/base" || o.token != "from-env" || !o.readOnly || o.maxUsers != 7 || o.maxDuration != 90*time.Second {
		t.Errorf("opts = %+v", o)
	}

	t.Setenv("GOLOAD_SERVER", "http://envhost:8080")
	if o, _ := parseMCPOpts(nil, &e); o == nil || o.server != "http://envhost:8080" || o.maxUsers != 50 || o.maxDuration != 10*time.Minute {
		t.Errorf("defaults = %+v", o)
	}

	for _, args := range [][]string{
		{"--server", "ftp://x"}, {"--max-users", "0"}, {"--max-duration", "0s"}, {"extra"}, {"--bogus"},
	} {
		var e bytes.Buffer
		if o, code := parseMCPOpts(args, &e); o != nil || code != exitUsage {
			t.Errorf("%v: opts %v code %d, want usage error", args, o, code)
		}
	}
	t.Setenv("GOLOAD_SERVER", "")
	if o, code := parseMCPOpts(nil, &e); o != nil || code != exitUsage {
		t.Errorf("missing server accepted")
	}
}

// An unreachable server is a warning on stderr; the MCP server still
// starts and serves tools (which then report the problem).
func TestRunMCPUnreachableServerStillServes(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()

	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runMCP(ctx, &mcpOpts{server: url, token: "tok-xyz", maxUsers: 5, maxDuration: time.Minute}, st, &stderr)
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_tests"})
	if err != nil || !res.IsError {
		t.Fatalf("list_tests = %v, %v; want a tool error", res, err)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "VPN") {
		t.Errorf("error text: %s", txt)
	}
	_ = cs.Close()
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runMCP did not return")
	}
	if !strings.Contains(stderr.String(), "not reachable") {
		t.Errorf("no warning on stderr: %s", stderr.String())
	}
	if strings.Contains(stderr.String(), "tok-xyz") {
		t.Errorf("token logged: %s", stderr.String())
	}
}
