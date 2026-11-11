// Package draft turns a handful of plain answers (a URL, a goal, a number of
// users, optionally a login) into a valid GoLoad scenario YAML. It is meant
// for non-technical users and AI agents that should not have to know the
// scenario format. Every generated YAML is checked with the repo's own
// scenario parser before it is returned.
package draft

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// Goal is what the user wants to find out, in plain words.
type Goal string

const (
	GoalSmoke  Goal = "smoke"  // quick check: few users, ~1 min
	GoalLoad   Goal = "load"   // normal day traffic
	GoalStress Goal = "stress" // find the limit: ramp up in steps
	GoalSpike  Goal = "spike"  // sudden burst (campaign moment)
)

// GoalInfo describes a goal in plain, non-technical words for end users.
type GoalInfo struct {
	Goal        Goal
	Title       string        // e.g. "Quick check"
	Description string        // one plain sentence
	Users       int           // default concurrent users
	Duration    time.Duration // default total duration
}

var goalInfos = []GoalInfo{
	{GoalSmoke, "Quick check", "A few visitors for about a minute, to make sure the site answers correctly.", 3, time.Minute},
	{GoalLoad, "Normal day", "The amount of visitors you expect on a normal busy day: ramping up, staying steady and winding down.", 20, 5 * time.Minute},
	{GoalStress, "Find the limit", "Adds more and more visitors in steps to see where the site starts to slow down or fail.", 50, 5 * time.Minute},
	{GoalSpike, "Sudden burst", "A calm period followed by a sudden wave of visitors, like the moment a campaign goes live.", 50, 3 * time.Minute},
}

// Goals lists the supported goals in a stable order.
func Goals() []GoalInfo {
	out := make([]GoalInfo, len(goalInfos))
	copy(out, goalInfos)
	return out
}

// Login describes how a virtual user signs in before the pages are visited.
type Login struct {
	URL           string // absolute login URL (same host as Answers.URL, or relative path)
	Method        string // default POST
	Format        string // "json" (default) or "form"
	UsernameField string // default "username"
	PasswordField string // default "password"
	TokenPath     string // JSON path of the token in the response, e.g. "data.access_token"; empty = cookie-based session
}

// Answers are the simple answers a scenario is built from.
type Answers struct {
	URL        string        // required, absolute http(s) URL of the first page/endpoint
	Goal       Goal          // default smoke
	Users      int           // 0 = goal default
	Duration   time.Duration // 0 = goal default
	Name       string        // optional; default derived from host+path
	ExtraPaths []string      // optional extra GET pages on the same host (paths or same-host absolute URLs)
	Login      *Login        // optional
}

// Result is the generated scenario and what the user should know about it.
type Result struct {
	Name     string
	YAML     string
	DataFile string   // "" or the data file name the YAML expects, header "username,password"
	Notes    []string // plain-English notes (what was assumed / defaulted)
}

// reTrailingZero turns "1m0s" into "1m" and "1h0m0s" into "1h" in the YAML.
var reTrailingZero = regexp.MustCompile(`(duration: "?\d+(?:h\d+)?m)0s`)

const (
	dataFileName = "users.csv"
	minUsers     = 1
	maxUsers     = 10000
	minDuration  = 10 * time.Second
	maxExtra     = 20
	thinkTime    = time.Second
)

// Build validates the answers and returns a scenario YAML.
func Build(a Answers) (Result, error) {
	var notes []string
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }

	base, first, err := parseTarget(a.URL)
	if err != nil {
		return Result{}, err
	}

	goal := a.Goal
	if goal == "" {
		goal = GoalSmoke
		note("No goal was given, so a quick check was chosen.")
	}
	var info GoalInfo
	for _, g := range goalInfos {
		if g.Goal == goal {
			info = g
		}
	}
	if info.Goal == "" {
		return Result{}, fmt.Errorf("unknown goal %q: choose one of smoke, load, stress or spike", goal)
	}

	users := a.Users
	switch {
	case users == 0:
		users = info.Users
		note("Number of users not given: using %d (the usual for %q).", users, info.Title)
	case users < minUsers:
		users = minUsers
		note("The number of users must be at least 1, so 1 was used.")
	case users > maxUsers:
		users = maxUsers
		note("The number of users was reduced to %d, the maximum this builder creates.", maxUsers)
	}
	dur := a.Duration
	switch {
	case dur == 0:
		dur = info.Duration
		note("Duration not given: using %s.", humanDuration(dur))
	case dur < minDuration:
		dur = minDuration
		note("The test must last at least 10 seconds, so 10s was used.")
	}
	dur = dur.Truncate(time.Second)

	// Pages: the first URL, then the extra ones (same host only).
	paths := []string{first}
	seen := map[string]bool{first: true}
	extras := 0
	for _, raw := range a.ExtraPaths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p, err := extraPath(raw, base)
		if err != nil {
			note("Page %q was skipped: %v.", raw, err)
			continue
		}
		if seen[p] {
			continue
		}
		if extras >= maxExtra {
			note("Only the first %d extra pages were used; %q and later ones were skipped.", maxExtra, raw)
			break
		}
		seen[p] = true
		paths = append(paths, p)
		extras++
	}

	site, _ := url.Parse(base)
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = defaultName(site.Hostname(), first)
	}

	sc := scenario.Scenario{
		Name:   name,
		Target: scenario.Target{BaseURL: base, Timeout: scenario.Duration{Duration: 30 * time.Second}},
		Load: scenario.Load{
			Model:  scenario.ModelClosed,
			Stages: stages(goal, users, dur),
		},
		Thresholds: []scenario.Threshold{
			{Metric: "error_rate", Op: "lt", Value: 0.01},
			{Metric: "latency.p95", Op: "lt", Value: p95Limit(goal)},
		},
	}
	names := map[string]int{}
	for _, p := range paths {
		sc.Steps = append(sc.Steps, scenario.Step{
			Name:      stepName(p, names),
			Method:    "GET",
			Path:      p,
			ThinkTime: scenario.Duration{Duration: thinkTime},
			Assert:    []scenario.Assert{{Type: scenario.AssertStatus, Op: "eq", Value: "200"}},
		})
	}

	dataFile := ""
	if a.Login != nil {
		auth, lnotes, err := buildAuth(a.Login, site)
		if err != nil {
			return Result{}, err
		}
		notes = append(notes, lnotes...)
		sc.Auth = auth
		sc.Data = &scenario.Data{File: dataFileName, Mode: scenario.DataSequential}
		dataFile = dataFileName
		note("Usernames and passwords are never written into the scenario. Put them in the data file %s "+
			"(first line: username,password, then one account per line) and upload it with the test.", dataFileName)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&sc); err != nil {
		return Result{}, fmt.Errorf("could not write the scenario: %w", err)
	}
	_ = enc.Close()
	body := reTrailingZero.ReplaceAllString(buf.String(), "$1")
	out := fmt.Sprintf("# Generated by GoLoad (goal: %s, %d users, %s).\n%s", goal, users, humanDuration(dur), body)

	if _, err := scenario.Parse([]byte(out)); err != nil {
		return Result{}, fmt.Errorf("internal error: the generated scenario is not valid (%v); please report this", err)
	}
	return Result{Name: name, YAML: out, DataFile: dataFile, Notes: notes}, nil
}

// parseTarget returns scheme://host[:port] and the first path (+query).
func parseTarget(raw string) (base, first string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("URL is required, for example https://example.com")
	}
	u, perr := url.Parse(raw)
	low := strings.ToLower(raw)
	if perr != nil || (!strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://")) {
		return "", "", errors.New("URL must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return "", "", errors.New("URL has no website address; use something like https://example.com")
	}
	if u.User != nil {
		return "", "", errors.New("URL must not contain a username or password; use the login option instead")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), pathQuery(u), nil
}

func pathQuery(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// extraPath turns a relative path or same-host absolute URL into a path.
func extraPath(raw, base string) (string, error) {
	if strings.Contains(raw, "{{") {
		return "", errors.New("it contains {{ }} which is not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("it is not a valid address")
	}
	if u.Scheme != "" || u.Host != "" {
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", errors.New("only http:// and https:// addresses are supported")
		}
		if strings.ToLower(u.Scheme)+"://"+strings.ToLower(u.Host) != base {
			return "", errors.New("it is on a different website than the first URL")
		}
	} else if !strings.HasPrefix(raw, "/") {
		u, err = url.Parse("/" + raw)
		if err != nil {
			return "", errors.New("it is not a valid address")
		}
	}
	return pathQuery(u), nil
}

func defaultName(host, first string) string {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	p := first
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.Trim(p, "/")
	if p == "" {
		p = "home"
	}
	return host + " - " + p
}

// stepName derives a short, unique name from a path.
func stepName(p string, used map[string]int) string {
	s := p
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if un, err := url.PathUnescape(s); err == nil {
		s = un
	}
	s = strings.Trim(s, "/")
	if s == "" {
		s = "home"
	} else {
		s = strings.ReplaceAll(s, "/", " ")
	}
	if len(s) > 40 {
		s = s[:40]
	}
	used[s]++
	if n := used[s]; n > 1 {
		return fmt.Sprintf("%s %d", s, n)
	}
	return s
}

func p95Limit(g Goal) float64 {
	switch g {
	case GoalStress:
		return 3000
	case GoalSpike:
		return 2000
	}
	return 1000
}

func humanDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}

// buildAuth creates the auth block for a login.
func buildAuth(l *Login, site *url.URL) (*scenario.Auth, []string, error) {
	var notes []string
	raw := strings.TrimSpace(l.URL)
	if raw == "" {
		return nil, nil, errors.New("login: the login URL is required")
	}
	lu, err := url.Parse(raw)
	if err != nil {
		return nil, nil, errors.New("login: the login URL is not a valid address")
	}
	if lu.Scheme != "" || lu.Host != "" {
		if (lu.Scheme != "http" && lu.Scheme != "https") || !strings.EqualFold(lu.Host, site.Host) {
			return nil, nil, errors.New("login: the login URL must be on the same website as the main URL")
		}
	} else if !strings.HasPrefix(raw, "/") {
		if lu, err = url.Parse("/" + raw); err != nil {
			return nil, nil, errors.New("login: the login URL is not a valid address")
		}
	}
	method := strings.ToUpper(strings.TrimSpace(l.Method))
	if method == "" {
		method = "POST"
	}
	switch method {
	case "POST", "PUT", "PATCH":
	default:
		return nil, nil, fmt.Errorf("login: method %q is not supported; use POST, PUT or PATCH", method)
	}
	uf, pf := strings.TrimSpace(l.UsernameField), strings.TrimSpace(l.PasswordField)
	if uf == "" {
		uf = "username"
	}
	if pf == "" {
		pf = "password"
	}

	step := scenario.Step{Name: "login", Method: method, Path: pathQuery(lu)}
	switch strings.ToLower(strings.TrimSpace(l.Format)) {
	case "", "json":
		ju, _ := json.Marshal(uf)
		jp, _ := json.Marshal(pf)
		step.Headers = map[string]string{"Content-Type": "application/json"}
		step.Body = "{" + string(ju) + `:"{{username}}",` + string(jp) + `:"{{password}}"}`
	case "form":
		step.Headers = map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
		step.Body = url.QueryEscape(uf) + "={{username}}&" + url.QueryEscape(pf) + "={{password}}"
	default:
		return nil, nil, fmt.Errorf("login: format %q is not supported; use json or form", l.Format)
	}

	auth := &scenario.Auth{Type: scenario.AuthLogin, Scope: scenario.AuthPerUser, Login: &step}
	if tp := strings.TrimSpace(l.TokenPath); tp != "" {
		step.Extract = []scenario.Extract{{Var: "token", From: scenario.FromJSON, Expr: tp}}
		notes = append(notes, fmt.Sprintf("Each user logs in once; the token found at %q in the login response is sent as a Bearer token on every request.", tp))
	} else {
		// Cookie session: the cookies the login sets (also on redirect hops)
		// are cached and replayed into each user's cookie jar, so the login
		// needs no extract and nothing is injected into requests.
		off := false
		auth.Inject = &off
		notes = append(notes, "No token location was given, so each user keeps the session cookie from the login response (no Authorization header is added). If your site returns a token instead, tell me where to find it in the response.")
	}
	return auth, notes, nil
}
