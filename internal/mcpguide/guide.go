// Package mcpguide holds the text an MCP server hands to AI agents: a
// reference guide for writing and running GoLoad tests, and the prompt for
// the "create_load_test" conversation.
package mcpguide

import (
	"embed"
	"strings"
)

//go:embed guide.md prompt.md
var files embed.FS

func read(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic("mcpguide: " + err.Error()) // embedded at build time; cannot happen
	}
	return string(b)
}

// Guide returns the agent-facing reference (markdown).
func Guide() string { return read("guide.md") }

// CreateTestPrompt returns the prompt text for the create_load_test MCP
// prompt. url may be empty, in which case the agent is told to ask for it.
func CreateTestPrompt(url string) string {
	url = strings.TrimSpace(url)
	p := read("prompt.md")
	if url == "" {
		p = strings.ReplaceAll(p, "{{URL}}", "a site the user has not named yet")
		p = strings.ReplaceAll(p, "{{URLSTEP}}", "First, ask the user for the site URL (the address of the site or API to test). Do not continue until you have it.")
	} else {
		p = strings.ReplaceAll(p, "{{URL}}", url)
		p = strings.ReplaceAll(p, "{{URLSTEP}}", "The target URL is "+url+".")
	}
	return p
}
