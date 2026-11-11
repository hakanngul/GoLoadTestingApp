package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDraftTools is the hook for the tools that draft a scenario from
// a URL.
//
// TODO(draft): another package provides `draft_test` (propose a scenario
// YAML for a URL) and `discover_links` (crawl a page for candidate
// endpoints). Register them here with one call each, e.g.
//
//	addTool(s, "draft_test", ..., kindRead, true, h.draftTest)
//
// They only read, so they belong to the read-only set. Until then this is
// a no-op.
func registerDraftTools(s *mcp.Server, h *handlers) {}

func (h *handlers) registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "create_load_test",
		Title:       "Create a load test",
		Description: "Guide the agent through creating, trying and running a load test for a URL.",
		Arguments:   []*mcp.PromptArgument{{Name: "url", Title: "URL", Description: "page or API to load-test, e.g. https://staging.example.com", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		u := strings.TrimSpace(req.Params.Arguments["url"])
		if u == "" {
			return nil, errors.New("argument url is required")
		}
		text := defaultCreateTestPrompt(u)
		if h.opts.CreateTestPrompt != nil {
			text = h.opts.CreateTestPrompt(u)
		}
		return &mcp.GetPromptResult{
			Description: "Create a load test for " + u,
			Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
		}, nil
	})
}

// defaultCreateTestPrompt is used when Options.CreateTestPrompt is nil.
func defaultCreateTestPrompt(u string) string {
	return fmt.Sprintf(`Create a load test for %s with the GoLoad tools.

1. Read the resource %s for the scenario YAML format, if it is available.
2. Check that the server is reachable with get_server_info, and list_tests to avoid duplicating a test.
3. Write a small scenario for %s (a few realistic steps, assertions on status, a sensible threshold such as p95 latency and error_rate) and run validate_test on it.
4. Send ONE iteration with try_test and fix what it reports (wrong paths, redirects, missing auth) until it passes. Never invent credentials: ask the user for them or use {{variables}} from a data file.
5. Save it with create_test, then start a SHORT, small run (for example 5 users for 30s) with start_run and wait_run. Only load-test a system the user owns or may test.
6. Summarise get_report in a few lines: verdict, rps, error rate, p95, the worst step and top errors. Offer set_baseline if the result is good.`, u, GuideURI, u)
}

func (h *handlers) registerResources(s *mcp.Server) {
	if strings.TrimSpace(h.opts.Guide) == "" {
		return
	}
	s.AddResource(&mcp.Resource{
		URI: GuideURI, Name: "guide", Title: "GoLoad guide",
		Description: "How to write GoLoad scenarios (YAML) and use the tools.", MIMEType: "text/markdown",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: GuideURI, MIMEType: "text/markdown", Text: h.opts.Guide}}}, nil
	})
}
