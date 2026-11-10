package scheduler

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// instantRamp is the first stage of a profile with ramp_up 0: the engine
// ramps linearly within every stage, so "start at full load" is a 1 s
// ramp (stage durations must be > 0).
const instantRamp = time.Second

func msDuration(ms float64) time.Duration {
	return time.Duration(math.Round(ms)) * time.Millisecond
}

// validateLoad checks a load override on its own (without a scenario).
func validateLoad(l *model.LoadOverride) []string {
	if l == nil {
		return nil
	}
	var details []string
	add := func(f string, a ...any) { details = append(details, fmt.Sprintf(f, a...)) }
	switch l.Mode {
	case model.LoadScale, model.LoadProfile:
	default:
		add("load.mode: must be %q or %q, got %q", model.LoadScale, model.LoadProfile, l.Mode)
	}
	switch {
	case l.Users < 0:
		add("load.users: must not be negative")
	case l.Rate < 0:
		add("load.rate: must not be negative")
	case l.Users > 0 && l.Rate > 0:
		add("load: set either users (closed model) or rate (open model), not both")
	case l.Users == 0 && l.Rate == 0:
		add("load: set users (virtual users) or rate (iterations per second)")
	}
	if l.Mode == model.LoadProfile {
		if l.HoldMs < 1000 {
			add("load.hold_ms: must be at least 1000 (1 s)")
		}
		if l.RampUpMs < 0 {
			add("load.ramp_up_ms: must not be negative")
		}
		if l.RampDownMs < 0 {
			add("load.ramp_down_ms: must not be negative")
		}
		if msDuration(l.RampUpMs+l.HoldMs+l.RampDownMs) > MaxProfileDuration {
			add("load: ramp-up + hold + ramp-down must be at most 7 days")
		}
	} else if l.Mode == model.LoadScale && (l.RampUpMs != 0 || l.HoldMs != 0 || l.RampDownMs != 0) {
		add("load: ramp_up_ms, hold_ms and ramp_down_ms only apply to mode profile")
	}
	return details
}

// CheckLoad validates a load override on its own and, when testYAML is
// not empty, against that scenario (e.g. users on an open-model test).
// It returns "path: message" details (paths start with "load"); nil = ok.
func CheckLoad(l *model.LoadOverride, testYAML string) []string {
	details := validateLoad(l)
	if len(details) > 0 || l == nil || testYAML == "" {
		return details
	}
	if _, err := ApplyLoad(testYAML, l); err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			return ve.Details
		}
		return []string{fmt.Sprintf("load: %v", err)}
	}
	return nil
}

// DescribeLoad is a short human description of a load override
// ("50 VUs, ramp 30s, hold 5m0s"); "" for nil.
func DescribeLoad(l *model.LoadOverride) string {
	if l == nil {
		return ""
	}
	peak := fmt.Sprintf("%d it/s", l.Rate)
	if l.Users > 0 {
		peak = fmt.Sprintf("%d VUs", l.Users)
	}
	if l.Mode == model.LoadScale {
		return "peak " + peak + " (scaled)"
	}
	ramp := msDuration(l.RampUpMs)
	if ramp <= 0 {
		ramp = instantRamp // what ApplyLoad runs for ramp_up 0
	}
	out := fmt.Sprintf("%s, ramp %s, hold %s", peak, ramp, msDuration(l.HoldMs))
	if l.RampDownMs > 0 {
		out += fmt.Sprintf(", down %s", msDuration(l.RampDownMs))
	}
	return out
}

// ApplyLoad returns the scenario YAML src with the load override l
// applied; src itself is not changed. Only the load section is rewritten:
//
//   - scale: the model is kept and every stage target is multiplied by
//     peak/(largest stage target), rounded, never below 1 for a stage
//     that was > 0. users requires a closed scenario, rate an open one.
//   - profile: the stages become ramp-up (0 -> peak; ramp_up 0 = 1 s),
//     hold (at peak) and, when ramp_down > 0, ramp-down (peak -> 0). The
//     model becomes closed for users and open for rate; max_vus is kept.
//
// The result is validated with scenario.Parse; a *ValidationError is
// returned when the override does not fit the scenario.
func ApplyLoad(src string, l *model.LoadOverride) (string, error) {
	if l == nil {
		return src, nil
	}
	if d := validateLoad(l); len(d) > 0 {
		return "", &ValidationError{Details: d}
	}
	sc, err := scenario.Parse([]byte(src))
	if err != nil {
		return "", fmt.Errorf("scenario: %w", err)
	}
	var stages []scenario.Stage
	var mdl scenario.LoadModel
	switch l.Mode {
	case model.LoadScale:
		mdl = sc.Load.Model
		want := l.Rate
		if l.Users > 0 {
			want = l.Users
			if mdl != scenario.ModelClosed {
				return "", &ValidationError{Details: []string{
					"load.users: the scenario uses the open model (iterations per second); scale it with rate, or use a ramp/hold profile with users"}}
			}
		} else if mdl != scenario.ModelOpen {
			return "", &ValidationError{Details: []string{
				"load.rate: the scenario uses the closed model (virtual users); scale it with users, or use a ramp/hold profile with rate"}}
		}
		peak := 0
		for _, st := range sc.Load.Stages {
			peak = max(peak, st.Target)
		}
		factor := float64(want) / float64(peak)
		for _, st := range sc.Load.Stages {
			t := 0
			if st.Target > 0 {
				t = max(1, int(math.Round(float64(st.Target)*factor)))
			}
			stages = append(stages, scenario.Stage{Duration: st.Duration, Target: t})
		}
	default: // profile
		target := l.Rate
		mdl = scenario.ModelOpen
		if l.Users > 0 {
			target, mdl = l.Users, scenario.ModelClosed
		}
		ramp := msDuration(l.RampUpMs)
		if ramp <= 0 {
			ramp = instantRamp
		}
		stages = append(stages,
			scenario.Stage{Duration: scenario.Duration{Duration: ramp}, Target: target},
			scenario.Stage{Duration: scenario.Duration{Duration: msDuration(l.HoldMs)}, Target: target})
		if down := msDuration(l.RampDownMs); down > 0 {
			stages = append(stages, scenario.Stage{Duration: scenario.Duration{Duration: down}, Target: 0})
		}
	}
	out, err := rewriteLoad([]byte(src), mdl, stages)
	if err != nil {
		return "", err
	}
	if _, err := scenario.Parse(out); err != nil {
		return "", fmt.Errorf("scenario with load override: %w", err)
	}
	return string(out), nil
}

// rewriteLoad replaces load.model and load.stages in the YAML document,
// keeping every other node (and comments) as they are.
func rewriteLoad(src []byte, mdl scenario.LoadModel, stages []scenario.Stage) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("scenario: top level must be a mapping")
	}
	root := doc.Content[0]
	load := mapValue(root, "load")
	if load == nil {
		load = &yaml.Node{Kind: yaml.MappingNode}
		setMapValue(root, "load", load)
	}
	if load.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("scenario: load must be a mapping")
	}
	setMapValue(load, "model", &yaml.Node{Kind: yaml.ScalarNode, Value: string(mdl)})
	var st yaml.Node
	if err := st.Encode(stages); err != nil {
		return nil, fmt.Errorf("encode stages: %w", err)
	}
	for _, item := range st.Content {
		item.Style = yaml.FlowStyle // - {duration: 30s, target: 20}
	}
	setMapValue(load, "stages", &st)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encode scenario: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode scenario: %w", err)
	}
	return buf.Bytes(), nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setMapValue(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep comments attached to the old value.
			v.HeadComment, v.LineComment, v.FootComment =
				m.Content[i+1].HeadComment, m.Content[i+1].LineComment, m.Content[i+1].FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}
