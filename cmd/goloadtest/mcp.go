package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/apiclient"
	"github.com/hakanngul/GoLoadTestingApp/internal/mcpguide"
	"github.com/hakanngul/GoLoadTestingApp/internal/mcpserver"
)

// mcpOpts are the parsed flags of `goloadtest mcp`.
type mcpOpts struct {
	server      string // normalized base URL, no trailing slash, no credentials
	token       string
	readOnly    bool
	maxUsers    int
	maxDuration time.Duration
}

func parseMCPOpts(args []string, stderr io.Writer) (*mcpOpts, int) {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", os.Getenv("GOLOAD_SERVER"), "GoLoad server URL, e.g. https://goload.example.com (env GOLOAD_SERVER)")
	token := fs.String("token", "", "API bearer token (prefer env GOLOAD_TOKEN; flags show up in ps)")
	readOnly := fs.Bool("read-only", false, "hide every tool that changes anything (create/update/start/stop/baseline/suites)")
	maxUsers := fs.Int("max-users", mcpserver.DefaultMaxUsers, "refuse runs with more virtual users (open-model tests: 10x this many iterations/s)")
	maxDuration := fs.Duration("max-duration", mcpserver.DefaultMaxDuration, "refuse runs that last longer")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: goloadtest mcp [--server URL] [--token T] [--read-only] [--max-users N] [--max-duration 10m]")
		fmt.Fprintln(stderr, "Runs an MCP (Model Context Protocol) server on stdin/stdout so AI agents (Claude Code, Cursor, Kiro)")
		fmt.Fprintln(stderr, "can manage tests and runs on a GoLoad server. Logs go to stderr only.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, exitUsage
	}
	bad := func(format string, a ...any) (*mcpOpts, int) {
		fmt.Fprintf(stderr, "mcp: "+format+"\n", a...)
		return nil, exitUsage
	}
	if fs.NArg() != 0 {
		return bad("unexpected argument %q", fs.Arg(0))
	}
	if *maxUsers < 1 {
		return bad("--max-users must be at least 1")
	}
	if *maxDuration <= 0 {
		return bad("--max-duration must be positive")
	}
	if *server == "" {
		return bad("--server (or env GOLOAD_SERVER) is required")
	}
	base, err := normalizeServerURL(*server)
	if err != nil {
		return bad("%v", err)
	}
	tok := *token
	if tok == "" {
		tok = os.Getenv("GOLOAD_TOKEN")
	}
	return &mcpOpts{server: base, token: tok, readOnly: *readOnly, maxUsers: *maxUsers, maxDuration: *maxDuration}, exitOK
}

// normalizeServerURL checks an http(s) URL and strips credentials, query,
// fragment and trailing slashes.
func normalizeServerURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("--server must be an http:// or https:// URL")
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// cmdMCP runs `goloadtest mcp`: the MCP server on stdin/stdout. stdout
// belongs to the protocol, so every message goes to stderr.
func cmdMCP(args []string, stderr io.Writer) int {
	o, code := parseMCPOpts(args, stderr)
	if o == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runMCP(ctx, o, &mcp.StdioTransport{}, stderr)
}

// runMCP serves MCP over t until the client disconnects or ctx ends.
func runMCP(ctx context.Context, o *mcpOpts, t mcp.Transport, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	client := apiclient.New(o.server, o.token)

	// One health check up front: an unreachable server (VPN down) is
	// logged, not fatal; the agent sees the problem in the tool errors.
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var health struct {
		Version string `json:"version"`
	}
	hc := *client
	hc.Retries = 0
	if err := hc.Do(hctx, http.MethodGet, "/api/health", nil, &health); err != nil {
		log.Warn("GoLoad server is not reachable; tools will report errors until it is", "server", o.server, "error", err.Error())
	} else {
		log.Info("connected to GoLoad server", "server", o.server, "version", health.Version)
		// A read-scoped token cannot change anything: hide the mutating
		// tools instead of letting every call fail with 403.
		var me struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		}
		if err := hc.Do(hctx, http.MethodGet, "/api/me", nil, &me); err == nil {
			log.Info("authenticated", "as", me.Name, "scope", me.Scope)
			if me.Scope == "read" && !o.readOnly {
				o.readOnly = true
				log.Info("token has read scope: serving read-only tools")
			}
		}
	}
	cancel()

	srv := mcpserver.New(mcpserver.Options{
		Client: client, Version: version, ReadOnly: o.readOnly, MaxUsers: o.maxUsers, MaxDuration: o.maxDuration,
		Guide: mcpguide.Guide(), CreateTestPrompt: mcpguide.CreateTestPrompt,
	})
	log.Info("MCP server ready on stdio", "read_only", o.readOnly, "max_users", o.maxUsers, "max_duration", o.maxDuration.String())
	if err := srv.Run(ctx, t); err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		log.Error("MCP server stopped", "error", err.Error())
		return exitRun
	}
	return exitOK
}
