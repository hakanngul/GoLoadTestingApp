// Package mcpserver exposes a GoLoad server to AI agents through the
// Model Context Protocol: tools to author, try, run and analyse load
// tests, a prompt for creating a test and a markdown guide as a resource.
//
// The server holds no state of its own. Every tool calls the GoLoad REST
// API (internal/api/doc.go) through an apiclient.Client, answers with
// compact structured JSON plus a one-line summary, masks secrets in
// everything it returns, and refuses runs that exceed the configured
// caps (Options.MaxUsers, Options.MaxDuration).
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hakanngul/GoLoadTestingApp/internal/apiclient"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// Options configures New.
type Options struct {
	// Client talks to the GoLoad server.
	Client *apiclient.Client
	// Version is the goloadtest version, reported to MCP clients.
	Version string
	// ReadOnly hides every mutating tool.
	ReadOnly bool
	// MaxUsers caps the virtual users of runs started via MCP (closed
	// model; open-model tests are capped at 10x this many iterations per
	// second). 0 = DefaultMaxUsers (50).
	MaxUsers int
	// MaxDuration caps the duration of runs started via MCP; 0 =
	// DefaultMaxDuration (10m).
	MaxDuration time.Duration
	// Guide is markdown served as resource goload://guide ("" = no resource).
	Guide string
	// CreateTestPrompt renders the prompt "create_load_test" for a URL.
	// nil uses a short built-in text.
	CreateTestPrompt func(url string) string
	// PollInterval is how often wait_run / wait_suite_run poll (default 2s).
	PollInterval time.Duration
}

// handlers holds what every tool needs.
type handlers struct {
	opts   Options
	client *apiclient.Client
	caps   caps
	poll   time.Duration
}

// GuideURI is the resource URI of the guide.
const GuideURI = "goload://guide"

const instructions = `GoLoad load-testing tools. Typical flow: validate_test or try_test (sends ONE real request iteration) -> create_test -> start_run -> wait_run -> get_report. ` +
	`Tests and suites are addressed by name or id. Secrets in tests are masked as "***" in what you read; {{var}} templates are not secrets. ` +
	`Runs started here are limited in users and duration (see get_server_info); never try to work around a refusal, ask the user instead. ` +
	`Only load-test systems the user owns or is allowed to test.`

// New builds the MCP server.
func New(opts Options) *mcp.Server {
	h := &handlers{opts: opts, client: opts.Client, poll: opts.PollInterval}
	if h.client == nil {
		h.client = apiclient.New("", "")
	}
	h.caps = caps{maxUsers: opts.MaxUsers, maxDuration: opts.MaxDuration}
	if h.caps.maxUsers <= 0 {
		h.caps.maxUsers = DefaultMaxUsers
	}
	if h.caps.maxDuration <= 0 {
		h.caps.maxDuration = DefaultMaxDuration
	}
	if h.poll <= 0 {
		h.poll = 2 * time.Second
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "goload", Title: "GoLoad", Version: version}, &mcp.ServerOptions{Instructions: instructions})

	h.registerReadTools(s)
	if !opts.ReadOnly {
		h.registerWriteTools(s)
	}
	registerDraftTools(s, h)
	h.registerPrompts(s)
	h.registerResources(s)
	return s
}

// ---- tool plumbing ----

// toolKind tells clients what a tool does (hints only).
type toolKind int

const (
	kindRead     toolKind = iota // changes nothing on the server
	kindWrite                    // creates or starts something
	kindOverride                 // replaces or stops something
)

// addTool registers a typed tool. fn returns the structured output and a
// one-line summary; the text content is "<summary>\n<json>" so clients
// that ignore structured content still get everything.
func addTool[In, Out any](s *mcp.Server, name, title, description string, kind toolKind, openWorld bool,
	fn func(ctx context.Context, in In) (Out, string, error)) {
	f := false
	ann := &mcp.ToolAnnotations{Title: title, ReadOnlyHint: kind == kindRead, OpenWorldHint: &openWorld, DestructiveHint: &f}
	if kind == kindOverride {
		t := true
		ann.DestructiveHint = &t
	}
	mcp.AddTool(s, &mcp.Tool{Name: name, Title: title, Description: description, Annotations: ann},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			out, summary, err := fn(ctx, in)
			if err != nil {
				var zero Out
				return nil, zero, err
			}
			b, merr := json.Marshal(out)
			if merr != nil {
				var zero Out
				return nil, zero, merr
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: summary + "\n" + string(b)}}}, out, nil
		})
}

// apiErr turns an API failure into a message for the model.
func (h *handlers) apiErr(what string, err error) error {
	var ae *apiclient.APIError
	if !errors.As(err, &ae) {
		return fmt.Errorf("%s: %w", what, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: ", what)
	switch {
	case ae.Status == 0:
		fmt.Fprintf(&b, "cannot reach the GoLoad server at %s (%s). Check the server URL and that the VPN is connected.", h.client.BaseURL, ae.Message)
	case ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden:
		fmt.Fprintf(&b, "%v. The server requires a token: ask the user to set GOLOAD_TOKEN for the MCP server.", ae)
	default:
		b.WriteString(ae.Error())
	}
	if len(ae.Details) > 0 {
		b.WriteString(" (" + strings.Join(ae.Details, "; ") + ")")
	}
	return errors.New(b.String())
}

func (h *handlers) get(ctx context.Context, path string, out any) error {
	return h.client.Do(ctx, http.MethodGet, path, nil, out)
}

func esc(s string) string { return url.PathEscape(s) }

// ---- name resolution ----

func (h *handlers) resolveTest(ctx context.Context, ref string) (apiclient.Ref, error) {
	if strings.TrimSpace(ref) == "" {
		return apiclient.Ref{}, errors.New("test is required (a test name or id; use list_tests)")
	}
	var ts []model.Test
	if err := h.get(ctx, "/api/tests", &ts); err != nil {
		return apiclient.Ref{}, h.apiErr("listing tests", err)
	}
	items := make([]apiclient.Ref, len(ts))
	for i, t := range ts {
		items[i] = apiclient.Ref{ID: t.ID, Name: t.Name}
	}
	r, err := apiclient.ResolveRef("test", strings.TrimSpace(ref), items)
	if err != nil {
		return r, err
	}
	return r, nil
}

func (h *handlers) resolveSuite(ctx context.Context, ref string) (apiclient.Ref, error) {
	if strings.TrimSpace(ref) == "" {
		return apiclient.Ref{}, errors.New("suite is required (a suite name or id; use list_suites)")
	}
	var ss []model.Suite
	if err := h.get(ctx, "/api/suites", &ss); err != nil {
		return apiclient.Ref{}, h.apiErr("listing suites", err)
	}
	items := make([]apiclient.Ref, len(ss))
	for i, s := range ss {
		items[i] = apiclient.Ref{ID: s.ID, Name: s.Name}
	}
	return apiclient.ResolveRef("suite", strings.TrimSpace(ref), items)
}

// getTest resolves a test by name or id and fetches it in full.
func (h *handlers) getTest(ctx context.Context, ref string) (model.Test, error) {
	r, err := h.resolveTest(ctx, ref)
	if err != nil {
		return model.Test{}, err
	}
	var t model.Test
	if err := h.get(ctx, "/api/tests/"+esc(r.ID), &t); err != nil {
		return t, h.apiErr(fmt.Sprintf("reading test %q", r.Name), err)
	}
	return t, nil
}

// waitFor fetches once (an error here is returned), then polls fetch every
// `every` until done reports true or the timeout passes. It returns the
// last fetched value and whether it is done. Errors while polling after
// the first success are retried until the timeout.
func waitFor[T any](ctx context.Context, every, timeout time.Duration, fetch func(context.Context) (T, bool, error)) (T, bool, error) {
	last, done, err := fetch(ctx)
	if err != nil || done {
		return last, done, err
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-dctx.Done():
			return last, false, ctx.Err()
		case <-t.C:
		}
		v, d, err := fetch(dctx)
		if err != nil {
			continue // transient; the next tick or the timeout ends it
		}
		last = v
		if d {
			return v, true, nil
		}
	}
}

// clampTimeout maps timeout_s (default 60, max 120) to a Duration.
func clampTimeout(s int) time.Duration {
	switch {
	case s <= 0:
		s = 60
	case s > 120:
		s = 120
	}
	return time.Duration(s) * time.Second
}
