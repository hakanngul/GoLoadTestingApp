package scheduler

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones also on hosts / images without zoneinfo
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/runner"
)

// Limits on schedule fields.
const (
	MaxNameLen = 200
	MaxWorkers = 1000

	// DefaultQueueTimeout is the queue policy's wait when QueueTimeoutMs
	// is 0; MaxQueueTimeout caps it.
	DefaultQueueTimeout = 30 * time.Minute
	MaxQueueTimeout     = 24 * time.Hour

	// MaxProfileDuration caps a ramp/hold load profile.
	MaxProfileDuration = 7 * 24 * time.Hour

	// AtLayout is the format of ScheduleSpec.At (local wall clock).
	AtLayout = "2006-01-02T15:04"
)

// ValidationError lists every problem of a schedule definition as
// "field: message" strings. It implements ValidationDetails so the API
// answers 400 with APIError.Details.
type ValidationError struct{ Details []string }

func (e *ValidationError) Error() string {
	return "invalid schedule: " + strings.Join(e.Details, "; ")
}

// ValidationDetails returns Details.
func (e *ValidationError) ValidationDetails() []string { return e.Details }

// Normalize trims and defaults a spec in place: Timezone "" -> UTC,
// Overlap "" -> skip, name and label trimmed.
func Normalize(s *model.ScheduleSpec) {
	s.Name = strings.TrimSpace(s.Name)
	s.Label = strings.TrimSpace(s.Label)
	s.Timezone = strings.TrimSpace(s.Timezone)
	s.Cron = strings.Join(strings.Fields(s.Cron), " ")
	s.At = strings.TrimSpace(s.At)
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if s.Overlap == "" {
		s.Overlap = model.OverlapSkip
	}
	if s.Kind == model.ScheduleOnce {
		s.Cron = ""
	} else if s.Kind == model.ScheduleRecurring {
		s.At = ""
	}
}

// LoadLocation resolves an IANA zone name ("Local" is refused: the
// server's zone is not the user's).
func LoadLocation(name string) (*time.Location, error) {
	if name == "" || strings.EqualFold(name, "local") {
		return nil, errors.New("must be an IANA time zone such as Europe/Istanbul or UTC")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	return loc, nil
}

// ParseAt resolves the local wall time at ("YYYY-MM-DDTHH:MM") in loc. A
// time skipped by a DST change resolves to the end of the gap; a repeated
// time to its first occurrence.
func ParseAt(at string, loc *time.Location) (time.Time, error) {
	wall, err := time.ParseInLocation(AtLayout, at, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("must be a local date and time like 2026-05-01T03:00, got %q", at)
	}
	return resolveWall(wall, loc), nil
}

// timing is a validated fire definition.
type timing struct {
	kind model.ScheduleKind
	loc  *time.Location
	at   time.Time // once
	cron *Cron     // recurring
}

// parseTiming validates kind/at/cron/timezone, appending problems to
// details.
func parseTiming(kind model.ScheduleKind, at, cron, tz string, details *[]string) *timing {
	add := func(f string, a ...any) { *details = append(*details, fmt.Sprintf(f, a...)) }
	n0 := len(*details)
	t := &timing{kind: kind}
	loc, err := LoadLocation(tz)
	if err != nil {
		add("timezone: %v", err)
	}
	t.loc = loc
	switch kind {
	case model.ScheduleOnce:
		if at == "" {
			add("at: required for a once schedule")
		} else if loc != nil {
			if t.at, err = ParseAt(at, loc); err != nil {
				add("at: %v", err)
			}
		}
	case model.ScheduleRecurring:
		if cron == "" {
			add("cron: required for a recurring schedule")
		} else if c, err := ParseCron(cron); err != nil {
			add("cron: %v", err)
		} else if _, ok := c.Next(time.Now(), time.UTC); !ok {
			add("cron: %q never matches a date", cron)
		} else {
			t.cron = c
		}
	default:
		add("kind: must be %q or %q, got %q", model.ScheduleOnce, model.ScheduleRecurring, kind)
	}
	if len(*details) > n0 {
		return nil
	}
	return t
}

// next returns the first fire time strictly after after (ok false: none).
func (t *timing) next(after time.Time) (time.Time, bool) {
	if t.kind == model.ScheduleOnce {
		if t.at.After(after) {
			return t.at, true
		}
		return time.Time{}, false
	}
	return t.cron.Next(after, t.loc)
}

// NextRun returns the next fire time of spec strictly after after, or
// nil when there is none (a once schedule in the past). The spec must be
// valid.
func NextRun(spec model.ScheduleSpec, after time.Time) (*time.Time, error) {
	var details []string
	t := parseTiming(spec.Kind, spec.At, spec.Cron, spec.Timezone, &details)
	if t == nil {
		return nil, &ValidationError{Details: details}
	}
	n, ok := t.next(after)
	if !ok {
		return nil, nil
	}
	n = n.UTC()
	return &n, nil
}

// Preview returns the next count fire times of a definition after now.
func Preview(req model.SchedulePreviewRequest, now time.Time) model.SchedulePreview {
	out := model.SchedulePreview{Errors: []string{}, Times: []time.Time{}}
	count := req.Count
	if count <= 0 {
		count = 5
	}
	count = min(count, 50)
	tz := strings.TrimSpace(req.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	t := parseTiming(req.Kind, strings.TrimSpace(req.At), strings.Join(strings.Fields(req.Cron), " "), tz, &out.Errors)
	if t == nil {
		return out
	}
	if req.Kind == model.ScheduleOnce && !t.at.After(now) {
		out.Errors = append(out.Errors, "at: must be in the future")
		return out
	}
	after := now
	for len(out.Times) < count {
		n, ok := t.next(after)
		if !ok {
			break
		}
		out.Times = append(out.Times, n.UTC())
		after = n
	}
	out.Valid = true
	return out
}

// Validate checks a normalized spec. enabled once schedules must lie in
// the future of now. testYAML (optional) is the scenario the load
// override must apply to.
func Validate(spec model.ScheduleSpec, enabled bool, testYAML string, now time.Time) error {
	var details []string
	add := func(f string, a ...any) { details = append(details, fmt.Sprintf(f, a...)) }
	if spec.TestID == "" {
		add("test_id: required")
	}
	if n := utf8.RuneCountInString(spec.Name); n == 0 || n > MaxNameLen {
		add("name: must be between 1 and %d characters", MaxNameLen)
	}
	t := parseTiming(spec.Kind, spec.At, spec.Cron, spec.Timezone, &details)
	if t != nil && enabled && spec.Kind == model.ScheduleOnce && !t.at.After(now) {
		add("at: must be in the future (%s is %s)", spec.At, t.at.In(t.loc).Format("2006-01-02 15:04 MST"))
	}
	if spec.Workers < 0 || spec.Workers > MaxWorkers {
		add("workers: must be between 0 and %d", MaxWorkers)
	}
	var vd interface{ ValidationDetails() []string }
	if err := runner.ValidateRunMeta(spec.Label, spec.Notes); err != nil && errors.As(err, &vd) {
		details = append(details, vd.ValidationDetails()...)
	}
	switch spec.Overlap {
	case model.OverlapSkip, model.OverlapQueue:
	default:
		add("overlap: must be %q or %q, got %q", model.OverlapSkip, model.OverlapQueue, spec.Overlap)
	}
	if spec.QueueTimeoutMs < 0 || time.Duration(spec.QueueTimeoutMs)*time.Millisecond > MaxQueueTimeout {
		add("queue_timeout_ms: must be between 0 and %d (24 h)", MaxQueueTimeout.Milliseconds())
	}
	if len(details) == 0 {
		details = append(details, CheckLoad(spec.Load, testYAML)...)
	} else {
		details = append(details, validateLoad(spec.Load)...)
	}
	if len(details) > 0 {
		return &ValidationError{Details: details}
	}
	return nil
}

// QueueTimeout is the effective wait of the queue policy.
func QueueTimeout(spec model.ScheduleSpec) time.Duration {
	if spec.QueueTimeoutMs <= 0 {
		return DefaultQueueTimeout
	}
	return time.Duration(spec.QueueTimeoutMs) * time.Millisecond
}

// RunLabel is the label of a run the schedule starts.
func RunLabel(spec model.ScheduleSpec) string {
	if spec.Label != "" {
		return spec.Label
	}
	l := "scheduled: " + spec.Name
	if utf8.RuneCountInString(l) > runner.MaxLabelLen {
		r := []rune(l)
		l = string(r[:runner.MaxLabelLen-1]) + "…"
	}
	return l
}
