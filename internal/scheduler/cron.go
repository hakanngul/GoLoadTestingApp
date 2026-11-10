package scheduler

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed 5-field cron expression:
//
//	minute hour day-of-month month day-of-week
//
// Each field accepts *, a value, a range a-b, a list a,b,c and a step
// (*/n, a-b/n, a/n = a to the field maximum every n). Months and weekdays
// accept three-letter English names (jan..dec, sun..sat); day-of-week 0
// and 7 are both Sunday. The macros @hourly, @daily (@midnight), @weekly,
// @monthly and @yearly (@annually) are accepted.
//
// As in Vixie cron, when both day-of-month and day-of-week are restricted
// (neither starts with *), a day matches if EITHER matches; otherwise
// both must match.
type Cron struct {
	minute, hour, dom, month, dow uint64 // bit i set = value i allowed
	domStar, dowStar              bool
	expr                          string
}

// String returns the expression as parsed.
func (c *Cron) String() string { return c.expr }

type cronField struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

	cronFields = [5]cronField{
		{name: "minute", min: 0, max: 59},
		{name: "hour", min: 0, max: 23},
		{name: "day-of-month", min: 1, max: 31},
		{name: "month", min: 1, max: 12, names: monthNames},
		{name: "day-of-week", min: 0, max: 7, names: dowNames},
	}

	cronMacros = map[string]string{
		"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
		"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
	}
)

// ParseCron parses a 5-field cron expression (see Cron).
func ParseCron(expr string) (*Cron, error) {
	expr = strings.TrimSpace(expr)
	src := expr
	if m, ok := cronMacros[strings.ToLower(expr)]; ok {
		src = m
	}
	parts := strings.Fields(src)
	if len(parts) != 5 {
		return nil, fmt.Errorf("must have 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	c := &Cron{expr: expr}
	dst := [5]*uint64{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, p := range parts {
		v, err := parseCronField(p, cronFields[i])
		if err != nil {
			return nil, err
		}
		*dst[i] = v
	}
	if c.dow&(1<<7) != 0 { // 7 = Sunday
		c.dow = c.dow&^(1<<7) | 1
	}
	c.domStar = strings.HasPrefix(parts[2], "*")
	c.dowStar = strings.HasPrefix(parts[4], "*")
	return c, nil
}

func parseCronField(s string, f cronField) (uint64, error) {
	var out uint64
	for _, item := range strings.Split(s, ",") {
		if item == "" {
			return 0, fmt.Errorf("%s: empty list item in %q", f.name, s)
		}
		rng, stepStr, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("%s: invalid step %q", f.name, stepStr)
			}
			step = n
		}
		var lo, hi int
		switch {
		case rng == "*":
			lo, hi = f.min, f.max
			if f.name == "day-of-week" {
				hi = 6
			}
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = cronValue(a, f); err != nil {
				return 0, err
			}
			if hi, err = cronValue(b, f); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("%s: range %q is reversed", f.name, rng)
			}
		default:
			v, err := cronValue(rng, f)
			if err != nil {
				return 0, err
			}
			lo, hi = v, v
			if hasStep { // a/n = a to max every n
				hi = f.max
			}
		}
		for v := lo; v <= hi; v += step {
			out |= 1 << uint(v)
		}
	}
	return out, nil
}

func cronValue(s string, f cronField) (int, error) {
	if v, ok := f.names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q", f.name, s)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("%s: value %d out of range %d-%d", f.name, v, f.min, f.max)
	}
	return v, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// matchDay reports whether the civil date matches the day fields.
func (c *Cron) matchDay(d time.Time) bool {
	if !has(c.month, int(d.Month())) {
		return false
	}
	domOK := has(c.dom, d.Day())
	dowOK := has(c.dow, int(d.Weekday()))
	if c.domStar || c.dowStar {
		return domOK && dowOK
	}
	return domOK || dowOK
}

// values lists the set bits of set in ascending order.
func values(set uint64) []int {
	out := make([]int, 0, bits.OnesCount64(set))
	for set != 0 {
		v := bits.TrailingZeros64(set)
		out = append(out, v)
		set &^= 1 << uint(v)
	}
	return out
}

// maxSearchDays bounds Next: an expression that matches no day within
// this many days (e.g. "0 0 30 2 *") never fires.
const maxSearchDays = 366*4 + 1

// Next returns the first fire time strictly after after, evaluating the
// expression on the wall clock of loc. ok is false when the expression
// never matches.
//
// Daylight saving time: every matching wall-clock time fires once.
//   - A time that does not exist (skipped by a spring-forward change,
//     e.g. 02:30 in Europe/Berlin on the last Sunday of March) fires at
//     the instant the change happens (03:00 local).
//   - A time that occurs twice (fall-back) fires at its first occurrence
//     only; the repeated hour does not fire again.
func (c *Cron) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	la := after.In(loc)
	// Civil (wall-clock) start, one minute past after's minute, kept in
	// UTC so date arithmetic is free of DST effects.
	start := time.Date(la.Year(), la.Month(), la.Day(), la.Hour(), la.Minute(), 0, 0, time.UTC).Add(time.Minute)
	hours, minutes := values(c.hour), values(c.minute)
	day0 := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxSearchDays; i++ {
		day := day0.AddDate(0, 0, i)
		if !c.matchDay(day) {
			continue
		}
		for _, h := range hours {
			for _, m := range minutes {
				wall := day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
				if wall.Before(start) {
					continue
				}
				t := resolveWall(wall, loc)
				if t.After(after) {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// resolveWall converts a civil time (given as a UTC time.Time whose
// fields are the wall clock) to an instant in loc. An ambiguous time
// (fall-back) resolves to its first (earlier) occurrence; a nonexistent
// time (spring-forward gap) resolves to the instant the gap ends.
func resolveWall(wall time.Time, loc *time.Location) time.Time {
	var best time.Time
	for _, probe := range [3]time.Duration{-26 * time.Hour, 0, 26 * time.Hour} {
		_, off := wall.Add(probe).In(loc).Zone()
		c := wall.Add(-time.Duration(off) * time.Second)
		if sameWall(c.In(loc), wall) && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if !best.IsZero() {
		return best
	}
	// Gap: time.Date picks one of the two zones; the transition instant is
	// the boundary of that zone on the side of the requested wall time.
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, loc)
	zs, ze := t.ZoneBounds()
	lt := t.In(loc)
	if time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), lt.Minute(), 0, 0, time.UTC).After(wall) {
		return zs
	}
	return ze
}

func sameWall(t, wall time.Time) bool {
	return t.Year() == wall.Year() && t.Month() == wall.Month() && t.Day() == wall.Day() &&
		t.Hour() == wall.Hour() && t.Minute() == wall.Minute() && t.Second() == 0
}
