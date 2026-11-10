package scheduler

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustCron(t *testing.T, expr string) *Cron {
	t.Helper()
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q): %v", expr, err)
	}
	return c
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseCronErrors(t *testing.T) {
	cases := map[string]string{
		"":                "5 fields",
		"* * * *":         "5 fields",
		"* * * * * *":     "5 fields",
		"60 * * * *":      "minute: value 60 out of range 0-59",
		"* 24 * * *":      "hour: value 24",
		"* * 0 * *":       "day-of-month: value 0",
		"* * * 13 *":      "month: value 13",
		"* * * * 8":       "day-of-week: value 8",
		"5-1 * * * *":     "reversed",
		"*/0 * * * *":     "invalid step",
		"a * * * *":       "invalid value",
		"1,,2 * * * *":    "empty list item",
		"* * * foo *":     "month: invalid value",
		"*/x * * * *":     "invalid step",
		"@every 5m":       "5 fields",
		"1-2-3 * * * * ":  "invalid value",
		"* * * * mon-xyz": "invalid value",
	}
	for expr, want := range cases {
		_, err := ParseCron(expr)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseCron(%q) = %v, want error containing %q", expr, err, want)
		}
	}
}

func TestCronNextUTC(t *testing.T) {
	cases := []struct {
		expr, after string
		want        []string
	}{
		{"0 3 * * *", "2026-11-10T02:59:30Z", []string{"2026-11-10T03:00:00Z", "2026-11-11T03:00:00Z"}},
		{"0 3 * * *", "2026-11-10T03:00:00Z", []string{"2026-11-11T03:00:00Z"}},
		{"*/15 * * * *", "2026-11-10T10:07:00Z", []string{"2026-11-10T10:15:00Z", "2026-11-10T10:30:00Z", "2026-11-10T10:45:00Z", "2026-11-10T11:00:00Z"}},
		{"* * * * *", "2026-11-10T10:07:59Z", []string{"2026-11-10T10:08:00Z", "2026-11-10T10:09:00Z"}},
		// Weekdays (2026-11-13 is a Friday).
		{"0 3 * * 1-5", "2026-11-13T04:00:00Z", []string{"2026-11-16T03:00:00Z", "2026-11-17T03:00:00Z"}},
		{"30 9 * * mon,wed,fri", "2026-11-10T00:00:00Z", []string{"2026-11-11T09:30:00Z", "2026-11-13T09:30:00Z", "2026-11-16T09:30:00Z"}},
		// 7 = Sunday (2026-11-15).
		{"0 0 * * 7", "2026-11-10T00:00:00Z", []string{"2026-11-15T00:00:00Z", "2026-11-22T00:00:00Z"}},
		// Both day fields restricted: either matches (the 1st, or Mondays).
		{"0 12 1 * 1", "2026-11-28T00:00:00Z", []string{"2026-11-30T12:00:00Z", "2026-12-01T12:00:00Z", "2026-12-07T12:00:00Z"}},
		// Only day-of-month restricted.
		{"0 0 31 * *", "2026-11-10T00:00:00Z", []string{"2026-12-31T00:00:00Z", "2027-01-31T00:00:00Z"}},
		{"0 0 29 2 *", "2026-11-10T00:00:00Z", []string{"2028-02-29T00:00:00Z"}},
		{"@daily", "2026-11-10T12:00:00Z", []string{"2026-11-11T00:00:00Z"}},
		{"@hourly", "2026-11-10T12:00:00Z", []string{"2026-11-10T13:00:00Z"}},
		{"0 9-17/4 * * *", "2026-11-10T09:00:00Z", []string{"2026-11-10T13:00:00Z", "2026-11-10T17:00:00Z", "2026-11-11T09:00:00Z"}},
		{"5/20 * * * *", "2026-11-10T09:00:00Z", []string{"2026-11-10T09:05:00Z", "2026-11-10T09:25:00Z", "2026-11-10T09:45:00Z", "2026-11-10T10:05:00Z"}},
		{"0 0 1 jan,jul *", "2026-11-10T00:00:00Z", []string{"2027-01-01T00:00:00Z", "2027-07-01T00:00:00Z"}},
	}
	for _, c := range cases {
		cr := mustCron(t, c.expr)
		after := utc(c.after)
		for i, w := range c.want {
			got, ok := cr.Next(after, time.UTC)
			if !ok || !got.Equal(utc(w)) {
				t.Fatalf("%q #%d after %s = %s %v, want %s", c.expr, i, after, got, ok, w)
			}
			after = got
		}
	}
}

func TestCronNeverMatches(t *testing.T) {
	if _, ok := mustCron(t, "0 0 30 2 *").Next(utc("2026-01-01T00:00:00Z"), time.UTC); ok {
		t.Fatal("Feb 30 matched")
	}
}

func TestCronIstanbul(t *testing.T) {
	// Europe/Istanbul is UTC+3 all year (no DST).
	ist := mustLoc(t, "Europe/Istanbul")
	c := mustCron(t, "0 3 * * 1-5")
	got, _ := c.Next(utc("2026-11-10T12:00:00Z"), ist)
	if want := utc("2026-11-11T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
	got, _ = c.Next(utc("2026-03-29T12:00:00Z"), ist) // Monday 30 March
	if want := utc("2026-03-30T00:00:00Z"); !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func seq(t *testing.T, c *Cron, loc *time.Location, after string, n int) []string {
	t.Helper()
	var out []string
	a := utc(after)
	for range n {
		got, ok := c.Next(a, loc)
		if !ok {
			t.Fatal("no next")
		}
		out = append(out, got.UTC().Format(time.RFC3339))
		a = got
	}
	return out
}

func eqSeq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("%s:\n got  %v\n want %v", what, got, want)
	}
}

func TestCronDSTBerlin(t *testing.T) {
	ber := mustLoc(t, "Europe/Berlin")
	// Spring forward 2026-03-29: 02:00 CET -> 03:00 CEST (01:00Z).
	// 02:30 does not exist: fires once at 03:00 CEST.
	eqSeq(t, "spring daily 02:30", seq(t, mustCron(t, "30 2 * * *"), ber, "2026-03-28T12:00:00Z", 3),
		[]string{"2026-03-29T01:00:00Z", "2026-03-30T00:30:00Z", "2026-03-31T00:30:00Z"})
	eqSeq(t, "spring daily 03:00", seq(t, mustCron(t, "0 3 * * *"), ber, "2026-03-28T12:00:00Z", 2),
		[]string{"2026-03-29T01:00:00Z", "2026-03-30T01:00:00Z"})
	// Every minute: 01:59 CET is followed by 03:00 CEST.
	eqSeq(t, "spring every minute", seq(t, mustCron(t, "* * * * *"), ber, "2026-03-29T00:58:00Z", 3),
		[]string{"2026-03-29T00:59:00Z", "2026-03-29T01:00:00Z", "2026-03-29T01:01:00Z"})

	// Fall back 2026-10-25: 03:00 CEST -> 02:00 CET (01:00Z); 02:30 occurs
	// twice and fires at its first occurrence only.
	eqSeq(t, "fall daily 02:30", seq(t, mustCron(t, "30 2 * * *"), ber, "2026-10-24T12:00:00Z", 2),
		[]string{"2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"})
	// Every 30 min: the repeated hour does not fire again.
	eqSeq(t, "fall every 30 min", seq(t, mustCron(t, "*/30 * * * *"), ber, "2026-10-24T23:45:00Z", 4),
		[]string{"2026-10-25T00:00:00Z", "2026-10-25T00:30:00Z", "2026-10-25T02:00:00Z", "2026-10-25T02:30:00Z"})
	// Weekly Sunday 02:30 on both change days.
	eqSeq(t, "weekly sunday", seq(t, mustCron(t, "30 2 * * sun"), ber, "2026-03-22T12:00:00Z", 2),
		[]string{"2026-03-29T01:00:00Z", "2026-04-05T00:30:00Z"})
}

func TestCronDSTNewYork(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// Spring forward 2026-03-08: 02:00 EST -> 03:00 EDT (07:00Z).
	eqSeq(t, "spring daily 02:30", seq(t, mustCron(t, "30 2 * * *"), ny, "2026-03-07T00:00:00Z", 3),
		[]string{"2026-03-07T07:30:00Z", "2026-03-08T07:00:00Z", "2026-03-09T06:30:00Z"})
	// Fall back 2026-11-01: 02:00 EDT -> 01:00 EST (06:00Z); 01:30 occurs
	// twice (05:30Z EDT, 06:30Z EST).
	eqSeq(t, "fall daily 01:30", seq(t, mustCron(t, "30 1 * * *"), ny, "2026-10-31T12:00:00Z", 3),
		[]string{"2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z", "2026-11-03T06:30:00Z"})
	// Daily 09:00 keeps its wall time across the change (UTC shifts).
	eqSeq(t, "daily 09:00", seq(t, mustCron(t, "0 9 * * *"), ny, "2026-10-31T00:00:00Z", 3),
		[]string{"2026-10-31T13:00:00Z", "2026-11-01T14:00:00Z", "2026-11-02T14:00:00Z"})
	// Hourly across fall-back: 01:00 EDT, 02:00 EST (the repeated 01:00 EST
	// is skipped).
	eqSeq(t, "fall hourly", seq(t, mustCron(t, "0 * * * *"), ny, "2026-11-01T04:30:00Z", 3),
		[]string{"2026-11-01T05:00:00Z", "2026-11-01T07:00:00Z", "2026-11-01T08:00:00Z"})
}

func TestParseAtDST(t *testing.T) {
	ber := mustLoc(t, "Europe/Berlin")
	ny := mustLoc(t, "America/New_York")
	cases := []struct {
		at   string
		loc  *time.Location
		want string
	}{
		{"2026-11-12T03:00", mustLoc(t, "Europe/Istanbul"), "2026-11-12T00:00:00Z"},
		{"2026-03-29T02:30", ber, "2026-03-29T01:00:00Z"}, // gap -> 03:00 CEST
		{"2026-10-25T02:30", ber, "2026-10-25T00:30:00Z"}, // first occurrence (CEST)
		{"2026-03-08T02:15", ny, "2026-03-08T07:00:00Z"},
		{"2026-11-01T01:30", ny, "2026-11-01T05:30:00Z"},
		{"2026-07-01T12:00", time.UTC, "2026-07-01T12:00:00Z"},
	}
	for _, c := range cases {
		got, err := ParseAt(c.at, c.loc)
		if err != nil || !got.Equal(utc(c.want)) {
			t.Errorf("ParseAt(%q, %s) = %s, %v; want %s", c.at, c.loc, got.UTC(), err, c.want)
		}
	}
	for _, bad := range []string{"", "2026-11-12", "2026-11-12 03:00", "2026-13-01T00:00", "tomorrow"} {
		if _, err := ParseAt(bad, time.UTC); err == nil {
			t.Errorf("ParseAt(%q) accepted", bad)
		}
	}
}

func TestLoadLocation(t *testing.T) {
	for _, bad := range []string{"", "Local", "local", "Mars/Olympus"} {
		if _, err := LoadLocation(bad); err == nil {
			t.Errorf("LoadLocation(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"UTC", "Europe/Istanbul", "America/New_York", "Asia/Kolkata"} {
		if _, err := LoadLocation(ok); err != nil {
			t.Errorf("LoadLocation(%q): %v", ok, err)
		}
	}
}
