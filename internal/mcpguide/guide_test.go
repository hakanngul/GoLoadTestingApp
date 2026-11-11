package mcpguide

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)```")

func TestGuideToolsMentioned(t *testing.T) {
	g := Guide()
	if strings.TrimSpace(g) == "" {
		t.Fatal("empty guide")
	}
	if n := strings.Count(g, "\n"); n > 280 {
		t.Errorf("guide is %d lines, want <= 280", n)
	}
	for _, tool := range []string{
		"list_tests", "get_test", "validate_test", "try_test", "list_runs", "get_run",
		"wait_run", "get_report", "compare_runs", "list_suites", "get_suite", "get_suite_run",
		"wait_suite_run", "list_schedules", "get_server_info", "draft_test", "discover_links",
		"create_test", "update_test", "start_run", "stop_run", "set_baseline", "create_suite",
		"run_suite", "stop_suite_run",
	} {
		if !strings.Contains(g, tool) {
			t.Errorf("guide does not mention %s", tool)
		}
	}
}

func TestGuideYAMLBlocksValid(t *testing.T) {
	blocks := yamlBlock.FindAllStringSubmatch(Guide(), -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d yaml blocks, want >= 3", len(blocks))
	}
	for i, m := range blocks {
		// scenario.Parse is what POST /api/validate uses; a data file
		// need not exist at that point.
		if _, err := scenario.Parse([]byte(m[1])); err != nil {
			t.Errorf("yaml block %d invalid: %v\n%s", i+1, err, m[1])
		}
	}
}

func TestCreateTestPrompt(t *testing.T) {
	p := CreateTestPrompt("https://shop.example.com")
	for _, want := range []string{"https://shop.example.com", "draft_test", "try_test", "create_test", "start_run", "discover_links"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, "{{") {
		t.Error("unreplaced placeholder")
	}
	if n := strings.Count(p, "\n"); n > 60 {
		t.Errorf("prompt is %d lines, want <= 60", n)
	}
	empty := CreateTestPrompt("  ")
	if !strings.Contains(empty, "ask the user for the site URL") || strings.Contains(empty, "{{") {
		t.Error("empty URL prompt should ask for the URL")
	}
}
