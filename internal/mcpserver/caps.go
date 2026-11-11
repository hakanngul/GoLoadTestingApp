package mcpserver

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
)

// Defaults of the safety caps for runs started through MCP.
const (
	DefaultMaxUsers    = 50
	DefaultMaxDuration = 10 * time.Minute

	// openRatePerUser turns the user cap into an arrival-rate cap for
	// open-model tests: an open-model test may target at most
	// MaxUsers*openRatePerUser iterations per second (50 -> 500 it/s).
	openRatePerUser = 10

	// maxLabelLen is the server's limit for run labels (runner.MaxLabelLen).
	maxLabelLen = 100
)

// loadInfo is the load a scenario would generate.
type loadInfo struct {
	Model    scenario.LoadModel
	Peak     int // highest stage target: VUs (closed) or iterations/s (open)
	Duration time.Duration
	BaseURL  string
	Steps    int
}

func (l loadInfo) closed() bool { return l.Model == scenario.ModelClosed }

// describe is a short human form such as "50 VUs for 5m0s".
func (l loadInfo) describe() string {
	if l.closed() {
		return fmt.Sprintf("%d VUs for %s", l.Peak, l.Duration)
	}
	return fmt.Sprintf("%d it/s for %s", l.Peak, l.Duration)
}

// analyze parses a scenario with the repo's parser and measures its load.
func analyze(yamlSrc string) (loadInfo, error) {
	sc, err := scenario.Parse([]byte(yamlSrc))
	if err != nil {
		return loadInfo{}, fmt.Errorf("invalid scenario: %w", err)
	}
	info := loadInfo{Model: sc.Load.Model, Duration: sc.Load.TotalDuration(), BaseURL: sc.Target.BaseURL, Steps: len(sc.Steps)}
	for _, st := range sc.Load.Stages {
		info.Peak = max(info.Peak, st.Target)
	}
	return info, nil
}

// caps are the limits for runs started via MCP.
type caps struct {
	maxUsers    int
	maxDuration time.Duration
}

func (c caps) maxRate() int { return c.maxUsers * openRatePerUser }

const askUser = "Ask the user to start it from the GoLoad UI or rerun the MCP server with --max-users / --max-duration."

// load is the load of something about to start (a run or a whole suite):
// peak concurrent virtual users (closed model), peak arrival rate (open
// model) and total duration.
type load struct {
	users    int
	rate     int
	duration time.Duration
}

// check returns a refusal when l exceeds the caps. subject is "run" or
// "suite run".
func (c caps) check(subject string, l load) error {
	if l.users > c.maxUsers {
		return fmt.Errorf("This %s would use %d users; the MCP limit is %d. %s", subject, l.users, c.maxUsers, askUser)
	}
	if l.rate > c.maxRate() {
		return fmt.Errorf("This %s would target %d iterations per second (open model); the MCP limit is %d (10 x max users). %s", subject, l.rate, c.maxRate(), askUser)
	}
	if l.duration > c.maxDuration {
		return fmt.Errorf("This %s would last %s; the MCP limit is %s. %s", subject, l.duration.Round(time.Second), c.maxDuration, askUser)
	}
	return nil
}

func loadOf(i loadInfo) load {
	if i.closed() {
		return load{users: i.Peak, duration: i.Duration}
	}
	return load{rate: i.Peak, duration: i.Duration}
}

// suiteLoad combines the loads of a suite's items: sequential items add
// up in time (plus the gaps) and peak separately, parallel items add up
// in load and last as long as the longest.
func suiteLoad(mode model.SuiteMode, gap time.Duration, items []load) load {
	var out load
	for i, it := range items {
		if mode == model.SuiteParallel {
			out.users += it.users
			out.rate += it.rate
			out.duration = max(out.duration, it.duration)
			continue
		}
		out.users = max(out.users, it.users)
		out.rate = max(out.rate, it.rate)
		out.duration += it.duration
		if i > 0 {
			out.duration += gap
		}
	}
	return out
}

// buildOverride turns the users/rate/duration inputs of start_run into a
// load override for the test's scenario (nil when none is asked for).
//
//   - users or rate alone: scale the test's stages so the peak is that
//     value (same shape and total duration).
//   - duration (with or without users/rate): a ramp-and-hold profile of that
//     total length (ramp-up = 10% of it, between 1s and 30s, no ramp-down) at the
//     given peak, or at the test's own peak.
func buildOverride(info loadInfo, users, rate int, duration string) (*model.LoadOverride, error) {
	if users < 0 || rate < 0 {
		return nil, fmt.Errorf("users and rate must not be negative")
	}
	if users > 0 && rate > 0 {
		return nil, fmt.Errorf("set either users (closed-model tests) or rate (open-model tests), not both")
	}
	if users == 0 && rate == 0 && strings.TrimSpace(duration) == "" {
		return nil, nil
	}
	if info.closed() && rate > 0 {
		return nil, fmt.Errorf("this test uses the closed model (virtual users): set users, not rate")
	}
	if !info.closed() && users > 0 {
		return nil, fmt.Errorf("this test uses the open model (iterations per second): set rate, not users")
	}
	if strings.TrimSpace(duration) == "" {
		return &model.LoadOverride{Mode: model.LoadScale, Users: users, Rate: rate}, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(duration))
	if err != nil {
		return nil, fmt.Errorf("duration %q is not a duration like 30s, 2m or 1h30m", duration)
	}
	if d < 2*time.Second {
		return nil, fmt.Errorf("duration must be at least 2s")
	}
	ramp := max(time.Second, min(d/10, 30*time.Second).Round(time.Second))
	if users == 0 && rate == 0 {
		if info.closed() {
			users = info.Peak
		} else {
			rate = info.Peak
		}
	}
	return &model.LoadOverride{
		Mode: model.LoadProfile, Users: users, Rate: rate,
		RampUpMs: float64(ramp.Milliseconds()), HoldMs: float64((d - ramp).Milliseconds()),
	}, nil
}

// effective returns the analysis of the scenario with the override applied.
func effective(yamlSrc string, ov *model.LoadOverride) (loadInfo, error) {
	if ov != nil {
		if details := scheduler.CheckLoad(ov, yamlSrc); len(details) > 0 {
			return loadInfo{}, fmt.Errorf("load override does not fit the test: %s", strings.Join(details, "; "))
		}
		y, err := scheduler.ApplyLoad(yamlSrc, ov)
		if err != nil {
			return loadInfo{}, fmt.Errorf("load override does not fit the test: %w", err)
		}
		yamlSrc = y
	}
	return analyze(yamlSrc)
}

// runLabel marks a run as started through MCP: "mcp" or "mcp: <label>",
// cut to the server's label limit.
func runLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "mcp"
	}
	out := "mcp: " + label
	if utf8.RuneCountInString(out) > maxLabelLen {
		out = string([]rune(out)[:maxLabelLen])
	}
	return out
}
