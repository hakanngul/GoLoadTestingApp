package metrics

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// ErrorMessageMaxRunes is the length (in runes) error messages are cut to
// before grouping, so that messages embedding long bodies still group.
const ErrorMessageMaxRunes = 300

// errKey identifies one error group.
type errKey struct {
	step    string
	status  int
	message string
}

// otherKey is the key of the overflow group.
var otherKey = errKey{message: model.ErrorGroupOther}

// ErrorGrouper counts failed requests by (step, status, normalized
// message), keeping
// at most max distinct groups; failures that would open a further group
// are counted in one overflow group {Step: "", Status: 0, Message:
// model.ErrorGroupOther}. It is safe for concurrent use.
//
// Messages are normalized with NormalizeErrorMessage so that failures
// differing only in IDs, URL values, timestamps or ephemeral ports share a
// group; each group keeps the first raw message as its Sample.
//
// Only failures touch it (one mutex), so the success path costs nothing.
// Counts from other groupers (remote workers) are folded in with Merge.
type ErrorGrouper struct {
	max int
	now func() time.Time

	mu     sync.Mutex
	groups map[errKey]*model.ErrorGroup
	other  *model.ErrorGroup // nil until the cap is first exceeded
}

// NewErrorGrouper returns an empty grouper. max <= 0 means
// model.MaxErrorGroups; now nil means time.Now.
func NewErrorGrouper(max int, now func() time.Time) *ErrorGrouper {
	if max <= 0 {
		max = model.MaxErrorGroups
	}
	if now == nil {
		now = time.Now
	}
	return &ErrorGrouper{max: max, now: now, groups: make(map[errKey]*model.ErrorGroup)}
}

// ErrorMessage returns err's text cut to ErrorMessageMaxRunes runes (""
// for a nil error).
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return truncateRunes(err.Error(), ErrorMessageMaxRunes)
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if len(s) <= n { // n bytes hold at most n runes
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// groupMessage returns the grouping key message (normalized, cut to
// ErrorMessageMaxRunes) and the raw sample (cut likewise) for msg. The raw
// text is cut generously before normalizing so that an ID straddling the
// final cut is still recognized.
func groupMessage(msg string) (key, sample string) {
	raw := truncateRunes(msg, 2*ErrorMessageMaxRunes)
	return truncateRunes(NormalizeErrorMessage(raw), ErrorMessageMaxRunes),
		truncateRunes(raw, ErrorMessageMaxRunes)
}

// Record counts one failed result. Results without an error are ignored.
func (g *ErrorGrouper) Record(step string, status int, err error) {
	if err == nil {
		return
	}
	msg, sample := groupMessage(err.Error())
	k := errKey{step: step, status: status, message: msg}
	t := g.now()
	g.mu.Lock()
	g.addLocked(k, 1, t, t, sample)
	g.mu.Unlock()
}

// Merge adds counts from another grouper's groups (e.g. a worker's
// per-second delta). Messages are normalized and truncated like Record's
// (a no-op for groups from another ErrorGrouper); a group's Sample (or,
// without one, its raw Message) becomes the merged group's sample if it is
// the earliest seen. A group with Message model.ErrorGroupOther and no
// step/status goes to the overflow group. Groups with Count <= 0 are
// ignored.
func (g *ErrorGrouper) Merge(groups []model.ErrorGroup) {
	if len(groups) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, e := range groups {
		if e.Count <= 0 {
			continue
		}
		msg, sample := groupMessage(e.Message)
		if e.Step == "" && e.Status == 0 && e.Message == model.ErrorGroupOther {
			msg = e.Message
		}
		if e.Sample != "" {
			sample = truncateRunes(e.Sample, ErrorMessageMaxRunes)
		} else if msg == model.ErrorGroupOther {
			sample = ""
		}
		k := errKey{step: e.Step, status: e.Status, message: msg}
		g.addLocked(k, e.Count, e.First, e.Last, sample)
	}
}

// addLocked adds n failures to k's group (or the overflow group). sample
// becomes the group's Sample when the group has none yet or first is
// earlier than every failure counted so far.
func (g *ErrorGrouper) addLocked(k errKey, n int64, first, last time.Time, sample string) {
	e, ok := g.groups[k]
	if !ok {
		if k == otherKey || len(g.groups) >= g.max {
			if g.other == nil {
				g.other = &model.ErrorGroup{Message: model.ErrorGroupOther}
			}
			e = g.other
		} else {
			e = &model.ErrorGroup{Step: k.step, Status: k.status, Message: k.message}
			g.groups[k] = e
		}
	}
	e.Count += n
	earliest := !first.IsZero() && (e.First.IsZero() || first.Before(e.First))
	if sample != "" && (e.Sample == "" || earliest) {
		e.Sample = sample
	}
	if earliest {
		e.First = first
	}
	if !last.IsZero() && last.After(e.Last) {
		e.Last = last
	}
}

// Snapshot returns a copy of every group, sorted by SortErrorGroups.
// It is never nil.
func (g *ErrorGrouper) Snapshot() []model.ErrorGroup {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.listLocked()
}

// Drain returns the groups like Snapshot and resets the grouper (worker
// per-second deltas).
func (g *ErrorGrouper) Drain() []model.ErrorGroup {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.listLocked()
	if len(g.groups) > 0 {
		g.groups = make(map[errKey]*model.ErrorGroup)
	}
	g.other = nil
	return out
}

// Total returns the number of failures counted so far.
func (g *ErrorGrouper) Total() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var n int64
	for _, e := range g.groups {
		n += e.Count
	}
	if g.other != nil {
		n += g.other.Count
	}
	return n
}

func (g *ErrorGrouper) listLocked() []model.ErrorGroup {
	out := make([]model.ErrorGroup, 0, len(g.groups)+1)
	for _, e := range g.groups {
		out = append(out, *e)
	}
	if g.other != nil {
		out = append(out, *g.other)
	}
	SortErrorGroups(out)
	return out
}

// SortErrorGroups orders groups by count (descending), then step, status
// and message; the overflow group always comes last.
func SortErrorGroups(gs []model.ErrorGroup) {
	isOther := func(e model.ErrorGroup) bool {
		return e.Step == "" && e.Status == 0 && e.Message == model.ErrorGroupOther
	}
	slices.SortFunc(gs, func(a, b model.ErrorGroup) int {
		if ao, bo := isOther(a), isOther(b); ao != bo {
			if ao {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Step, b.Step); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Status, b.Status); c != 0 {
			return c
		}
		return cmp.Compare(a.Message, b.Message)
	})
}
