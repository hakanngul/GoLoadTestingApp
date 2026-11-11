package draft

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

func mustBuild(t *testing.T, a Answers) (Result, *scenario.Scenario) {
	t.Helper()
	r, err := Build(a)
	if err != nil {
		t.Fatalf("Build(%+v): %v", a, err)
	}
	sc, err := scenario.Parse([]byte(r.YAML))
	if err != nil {
		t.Fatalf("generated YAML does not parse: %v\n%s", err, r.YAML)
	}
	return r, sc
}

func totalOf(sc *scenario.Scenario) time.Duration {
	var d time.Duration
	for _, s := range sc.Load.Stages {
		d += s.Duration.Duration
	}
	return d
}

func maxTarget(sc *scenario.Scenario) int {
	m := 0
	for _, s := range sc.Load.Stages {
		if s.Target > m {
			m = s.Target
		}
	}
	return m
}

func TestGoals(t *testing.T) {
	gs := Goals()
	want := []Goal{GoalSmoke, GoalLoad, GoalStress, GoalSpike}
	if len(gs) != len(want) {
		t.Fatalf("got %d goals", len(gs))
	}
	for i, g := range gs {
		if g.Goal != want[i] || g.Title == "" || g.Description == "" || g.Users < 1 || g.Duration < 10*time.Second {
			t.Errorf("bad goal info %+v", g)
		}
	}
	gs[0].Title = "changed"
	if Goals()[0].Title == "changed" {
		t.Error("Goals must return a copy")
	}
}

func TestBuildGoals(t *testing.T) {
	tests := []struct {
		name      string
		a         Answers
		wantUsers int
		wantTotal time.Duration
		wantP95   float64
		check     func(t *testing.T, st []scenario.Stage)
	}{
		{"smoke default", Answers{URL: "https://example.com"}, 3, time.Minute, 1000, func(t *testing.T, st []scenario.Stage) {
			if len(st) != 2 || st[0].Target != 3 || st[1].Target != 3 {
				t.Errorf("smoke stages: %+v", st)
			}
		}},
		{"load default", Answers{URL: "https://example.com", Goal: GoalLoad}, 20, 5 * time.Minute, 1000, func(t *testing.T, st []scenario.Stage) {
			if len(st) != 3 || st[0].Target != 20 || st[1].Target != 20 || st[2].Target != 0 {
				t.Fatalf("load stages: %+v", st)
			}
			if st[0].Duration.Duration != time.Minute || st[2].Duration.Duration != 30*time.Second {
				t.Errorf("load ramp up should be 20%% and ramp down short: %+v", st)
			}
		}},
		{"stress default", Answers{URL: "https://example.com", Goal: GoalStress}, 50, 5 * time.Minute, 3000, func(t *testing.T, st []scenario.Stage) {
			var peaks []int
			for _, s := range st {
				if len(peaks) == 0 || peaks[len(peaks)-1] != s.Target {
					peaks = append(peaks, s.Target)
				}
			}
			if !reflect.DeepEqual(peaks, []int{13, 25, 38, 50, 0}) {
				t.Errorf("stress steps: %v (%+v)", peaks, st)
			}
		}},
		{"spike default", Answers{URL: "https://example.com", Goal: GoalSpike}, 50, 3 * time.Minute, 2000, func(t *testing.T, st []scenario.Stage) {
			base, jump := 0, 0
			for _, s := range st {
				if s.Target == 50 {
					jump++
				} else {
					base = s.Target
				}
			}
			if base != 5 || jump != 2 {
				t.Errorf("spike should be baseline 5 then jump to 50: %+v", st)
			}
			if st[0].Target != 5 || st[len(st)-1].Target != 5 {
				t.Errorf("spike should start and end at baseline: %+v", st)
			}
		}},
		{"overrides", Answers{URL: "https://example.com", Goal: GoalLoad, Users: 7, Duration: 90 * time.Second}, 7, 90 * time.Second, 1000, nil},
		{"clamp low", Answers{URL: "https://example.com", Goal: GoalLoad, Users: -4, Duration: 2 * time.Second}, 1, 10 * time.Second, 1000, nil},
		{"tiny stress", Answers{URL: "https://example.com", Goal: GoalStress, Users: 2, Duration: 10 * time.Second}, 2, 10 * time.Second, 3000, nil},
		{"tiny spike", Answers{URL: "https://example.com", Goal: GoalSpike, Users: 1, Duration: 10 * time.Second}, 1, 10 * time.Second, 2000, nil},
		{"sub-second duration is truncated", Answers{URL: "https://example.com", Goal: GoalSmoke, Duration: 61500 * time.Millisecond}, 3, 61 * time.Second, 1000, nil},
		{"huge users capped", Answers{URL: "https://example.com", Users: 1000000}, 10000, time.Minute, 1000, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, sc := mustBuild(t, tc.a)
			if sc.Load.Model != scenario.ModelClosed {
				t.Errorf("model = %q", sc.Load.Model)
			}
			if got := maxTarget(sc); got != tc.wantUsers {
				t.Errorf("peak users = %d, want %d", got, tc.wantUsers)
			}
			if got := totalOf(sc); got != tc.wantTotal {
				t.Errorf("total = %s, want %s", got, tc.wantTotal)
			}
			for _, s := range sc.Load.Stages {
				if s.Duration.Duration < time.Second {
					t.Errorf("stage shorter than 1s: %+v", s)
				}
			}
			var p95, errRate float64 = -1, -1
			for _, th := range sc.Thresholds {
				if th.Op != "lt" {
					t.Errorf("threshold op %q", th.Op)
				}
				switch th.Metric {
				case "latency.p95":
					p95 = th.Value
				case "error_rate":
					errRate = th.Value
				}
			}
			if p95 != tc.wantP95 || errRate != 0.01 {
				t.Errorf("thresholds p95=%v err=%v", p95, errRate)
			}
			if tc.check != nil {
				tc.check(t, sc.Load.Stages)
			}
			if r.DataFile != "" || sc.Data != nil || sc.Auth != nil {
				t.Errorf("no login expected")
			}
		})
	}
}

func TestBuildSteps(t *testing.T) {
	r, sc := mustBuild(t, Answers{
		URL: "https://www.Example.com:8443/shop?page=2",
		ExtraPaths: []string{
			"/about", "contact", "https://www.example.com:8443/blog/2024/post", "https://other.com/x",
			"/about", "/shop?page=2", "", "ftp://www.example.com:8443/f",
		},
	})
	if sc.Target.BaseURL != "https://www.example.com:8443" {
		t.Errorf("base_url = %q", sc.Target.BaseURL)
	}
	var paths, names []string
	for _, s := range sc.Steps {
		paths = append(paths, s.Path)
		names = append(names, s.Name)
		if s.Method != "GET" || s.ThinkTime.Duration <= 0 || len(s.Assert) != 1 || s.Assert[0].Type != "status" || s.Assert[0].Value != "200" {
			t.Errorf("step %+v", s)
		}
	}
	if want := []string{"/shop?page=2", "/about", "/contact", "/blog/2024/post"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	if want := []string{"shop", "about", "contact", "blog 2024 post"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if r.Name != "example.com - shop" {
		t.Errorf("name = %q", r.Name)
	}
	joined := strings.Join(r.Notes, "\n")
	if !strings.Contains(joined, "other.com") || !strings.Contains(joined, "ftp://") {
		t.Errorf("dropped pages should be mentioned in notes: %v", r.Notes)
	}
}

func TestBuildNames(t *testing.T) {
	r, _ := mustBuild(t, Answers{URL: "https://example.com"})
	if r.Name != "example.com - home" {
		t.Errorf("name = %q", r.Name)
	}
	r, sc := mustBuild(t, Answers{URL: "https://example.com", Name: "  My shop  "})
	if r.Name != "My shop" || sc.Name != "My shop" {
		t.Errorf("name = %q / %q", r.Name, sc.Name)
	}
	// Steps whose names would collide still get unique names.
	_, sc = mustBuild(t, Answers{URL: "https://example.com/a/b", ExtraPaths: []string{"/a-b", "/a b", "/a/b?x=1"}})
	seen := map[string]bool{}
	for _, s := range sc.Steps {
		if seen[s.Name] {
			t.Errorf("duplicate name %q", s.Name)
		}
		seen[s.Name] = true
	}
}

func TestBuildLogin(t *testing.T) {
	t.Run("json token", func(t *testing.T) {
		r, sc := mustBuild(t, Answers{
			URL:   "https://example.com",
			Login: &Login{URL: "https://example.com/api/login", TokenPath: "data.access_token"},
		})
		a := sc.Auth
		if a == nil || a.Type != scenario.AuthLogin || a.Login == nil {
			t.Fatalf("auth = %+v", a)
		}
		if a.Login.Method != "POST" || a.Login.Path != "/api/login" {
			t.Errorf("login step %+v", a.Login)
		}
		if a.Login.Body != `{"username":"{{username}}","password":"{{password}}"}` {
			t.Errorf("body = %q", a.Login.Body)
		}
		if a.Login.Headers["Content-Type"] != "application/json" {
			t.Errorf("headers = %v", a.Login.Headers)
		}
		if len(a.Login.Extract) != 1 || a.Login.Extract[0].Var != "token" || a.Login.Extract[0].Expr != "data.access_token" || a.Login.Extract[0].From != scenario.FromJSON {
			t.Errorf("extract = %+v", a.Login.Extract)
		}
		if !a.InjectEnabled() || a.Header != "Authorization" || a.Value != "Bearer {{token}}" {
			t.Errorf("inject/header/value = %v %q %q", a.InjectEnabled(), a.Header, a.Value)
		}
		if sc.Data == nil || sc.Data.File != "users.csv" || r.DataFile != "users.csv" {
			t.Errorf("data = %+v / %q", sc.Data, r.DataFile)
		}
		noLiteralCreds(t, r)
	})
	t.Run("form custom fields", func(t *testing.T) {
		r, sc := mustBuild(t, Answers{
			URL: "https://example.com",
			Login: &Login{URL: "account/login?next=%2F", Format: "FORM", Method: "post",
				UsernameField: "user[email]", PasswordField: "pw", TokenPath: "token"},
		})
		l := sc.Auth.Login
		if l.Path != "/account/login?next=%2F" || l.Headers["Content-Type"] != "application/x-www-form-urlencoded" {
			t.Errorf("login %+v", l)
		}
		if l.Body != "user%5Bemail%5D={{username}}&pw={{password}}" {
			t.Errorf("body = %q", l.Body)
		}
		noLiteralCreds(t, r)
	})
	t.Run("json escapes field names", func(t *testing.T) {
		_, sc := mustBuild(t, Answers{URL: "https://example.com", Login: &Login{URL: "/l", UsernameField: `we"ird`}})
		if !strings.Contains(sc.Auth.Login.Body, `"we\"ird":"{{username}}"`) {
			t.Errorf("body = %q", sc.Auth.Login.Body)
		}
	})
	t.Run("cookie session", func(t *testing.T) {
		r, sc := mustBuild(t, Answers{URL: "https://example.com", Login: &Login{URL: "/login"}})
		if sc.Auth.InjectEnabled() {
			t.Error("cookie login must not inject a header")
		}
		if sc.Target.CookiesEnabled() != true {
			t.Error("cookies must stay on")
		}
		if len(sc.Auth.Login.Extract) != 0 {
			t.Errorf("extract = %+v", sc.Auth.Login.Extract)
		}
		if r.DataFile != "users.csv" || !strings.Contains(strings.Join(r.Notes, " "), "users.csv") {
			t.Errorf("notes should explain the data file: %v", r.Notes)
		}
	})
	t.Run("errors", func(t *testing.T) {
		for name, l := range map[string]*Login{
			"missing url":  {},
			"other host":   {URL: "https://evil.com/login"},
			"bad method":   {URL: "/l", Method: "GET"},
			"bad format":   {URL: "/l", Format: "xml"},
			"bad scheme":   {URL: "ftp://example.com/l"},
			"other port":   {URL: "https://example.com:444/l"},
			"invalid addr": {URL: "http://[::1"},
		} {
			if _, err := Build(Answers{URL: "https://example.com", Login: l}); err == nil {
				t.Errorf("%s: expected error", name)
			} else if !strings.HasPrefix(err.Error(), "login:") {
				t.Errorf("%s: error should start with login: %v", name, err)
			}
		}
	})
}

func noLiteralCreds(t *testing.T, r Result) {
	t.Helper()
	if !strings.Contains(r.YAML, "{{username}}") || !strings.Contains(r.YAML, "{{password}}") {
		t.Errorf("login must use placeholders:\n%s", r.YAML)
	}
	if !strings.Contains(r.YAML, "file: users.csv") {
		t.Errorf("yaml must declare the data file:\n%s", r.YAML)
	}
}

func TestBuildErrors(t *testing.T) {
	tests := []struct {
		name string
		a    Answers
		want string
	}{
		{"empty", Answers{}, "URL is required"},
		{"no scheme", Answers{URL: "example.com"}, "http:// or https://"},
		{"ftp", Answers{URL: "ftp://example.com"}, "http:// or https://"},
		{"no host", Answers{URL: "https://"}, "no website address"},
		{"no host path", Answers{URL: "https:///path"}, "no website address"},
		{"userinfo", Answers{URL: "https://u:p@example.com"}, "username or password"},
		{"bad goal", Answers{URL: "https://example.com", Goal: "soak"}, "unknown goal"},
		{"unparsable", Answers{URL: "http://[::1"}, "http:// or https://"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestEveryComboParses(t *testing.T) {
	for _, g := range []Goal{"", GoalSmoke, GoalLoad, GoalStress, GoalSpike} {
		for _, u := range []int{0, 1, 2, 3, 5, 100} {
			for _, d := range []time.Duration{0, 10 * time.Second, 11 * time.Second, 37 * time.Second, 5 * time.Minute, time.Hour} {
				for _, login := range []*Login{nil, {URL: "/login", TokenPath: "token"}, {URL: "/login", Format: "form"}} {
					r, sc := mustBuild(t, Answers{URL: "http://localhost:8080/", Goal: g, Users: u, Duration: d, Login: login})
					if d >= 10*time.Second && totalOf(sc) != d {
						t.Errorf("goal=%q users=%d d=%s: total %s", g, u, d, totalOf(sc))
					}
					if r.YAML == "" {
						t.Error("empty yaml")
					}
				}
			}
		}
	}
}

func TestLinks(t *testing.T) {
	const page = `<!doctype html>
<html><head>
<link rel="stylesheet" href="/static/app.css">
<script src="/app.js"></script>
<script>var a = '<a href="/from-script">x</a>';</script>
</head><body>
<!-- <a href="/commented-out">old</a> -->
<nav>
  <a class="logo" href="/">Home</a>
  <a href="/products">Products</a>
  <a href='/products/?page=2&amp;sort=asc#top'>Page 2</a>
  <A HREF=/about>About</A>
  <a href="contact.html">Contact</a>
  <a href="https://shop.example.com/cart">Cart</a>
  <a href="https://www.example.com/blog/post-1?utm=1">Blog</a>
  <a href="//www.example.com/faq">FAQ</a>
  <a href="https://other.com/x">Other</a>
  <a href="https://www.example.com:8443/y">Other port</a>
  <a href="mailto:hi@example.com">Mail</a>
  <a href="tel:+123">Call</a>
  <a href="javascript:void(0)">JS</a>
  <a href="#main">Skip</a>
  <a href="/logo.png">Logo</a>
  <a href="/files/brochure.pdf">PDF</a>
  <a href="/account/logout">Log out</a>
  <a href="/signout?next=/">Sign out</a>
  <a href="/products">Products again</a>
  <a name="anchor">no href</a>
  <a
     href="/multi/line"
     class="x">Multi</a>
</nav>
</body></html>`
	got := Links("https://www.example.com/shop/index.html", page, 50)
	want := []string{
		"https://www.example.com/",
		"https://www.example.com/products",
		"https://www.example.com/products/?page=2&sort=asc",
		"https://www.example.com/about",
		"https://www.example.com/shop/contact.html",
		"https://www.example.com/blog/post-1?utm=1",
		"https://www.example.com/faq",
		"https://www.example.com/multi/line",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Links =\n%v\nwant\n%v", got, want)
	}
	if got := Links("https://www.example.com/", page, 3); len(got) != 3 || got[2] != want[2] {
		t.Errorf("max not honoured: %v", got)
	}
	if got := Links("https://www.example.com/", page, 0); len(got) != 0 {
		t.Errorf("max 0: %v", got)
	}
	if got := Links("not a url", page, 5); got != nil {
		t.Errorf("bad page url: %v", got)
	}
	if got := Links("https://www.example.com/", "no links here", 5); len(got) != 0 {
		t.Errorf("no links: %v", got)
	}
}
