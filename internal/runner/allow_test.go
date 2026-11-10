package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func TestAllowlistCoversAbsoluteStepURLs(t *testing.T) {
	list, err := allowlist.Parse("allowed.test")
	if err != nil {
		t.Fatal(err)
	}
	st := newFakeStore()
	m := newManager(t, st, func(o *Options) { o.Allow = list.Check })

	ok := scenarioYAML("http://allowed.test", "1s", 1, "/", "")
	sneaky := scenarioYAML("http://allowed.test", "1s", 1, "https://victim.example/", "")
	badBase := scenarioYAML("http://victim.example", "1s", 1, "/", "")

	_, err = m.Start(context.Background(), testFor(badBase), model.StartRunRequest{})
	if !errors.Is(err, ErrTargetNotAllowed) || !strings.Contains(err.Error(), "victim.example") {
		t.Errorf("base url: err = %v", err)
	}
	// Absolute step URLs are rejected either by scenario validation (HTTP
	// paths must be relative) or by the allowlist (e.g. ws:// URLs).
	if _, err := m.Start(context.Background(), testFor(sneaky), model.StartRunRequest{}); err == nil {
		t.Error("absolute step URL to a non-allowlisted host was accepted")
	}
	if st.runCount() != 0 {
		t.Fatal("rejected scenarios created runs")
	}
	if _, err := m.Start(context.Background(), testFor(ok), model.StartRunRequest{}); err != nil {
		t.Fatalf("allowed target rejected: %v", err)
	}
}
