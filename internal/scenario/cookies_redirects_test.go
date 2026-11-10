package scenario

import (
	"strings"
	"testing"
)

func TestCookieAndRedirectDefaults(t *testing.T) {
	s, err := Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Target.CookiesEnabled() || !s.Target.FollowsRedirects() || s.Target.Cookies != nil ||
		s.Target.FollowRedirects != nil || s.Target.MaxRedirects != 0 || s.Steps[0].FollowRedirects != nil {
		t.Errorf("defaults: target %+v step %+v", s.Target, s.Steps[0])
	}

	s, err = Parse([]byte(`
name: t
target:
  base_url: https://api.example.test
  cookies: false
  follow_redirects: false
  max_redirects: 3
load:
  stages: [{duration: 10s, target: 5}]
steps:
  - path: /a
  - path: /b
    follow_redirects: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Target.CookiesEnabled() || s.Target.FollowsRedirects() || s.Target.MaxRedirects != 3 {
		t.Errorf("target = %+v", s.Target)
	}
	if s.Steps[0].FollowRedirects != nil || s.Steps[1].FollowRedirects == nil || !*s.Steps[1].FollowRedirects {
		t.Errorf("steps = %+v", s.Steps)
	}
}

func TestCookieAndRedirectValidation(t *testing.T) {
	for _, tc := range []struct{ target, step, want string }{
		{"  max_redirects: -1", "", "target.max_redirects: must be between 1 and 50"},
		{"  max_redirects: 51", "", "target.max_redirects: must be between 1 and 50"},
		{"  cookies: maybe", "", "decode"},
		{"", "    type: ws\n    ws: {hold: 1s}\n    follow_redirects: false", "steps[0].follow_redirects: not allowed on ws steps"},
	} {
		y := "name: t\ntarget:\n  base_url: https://api.example.test\n" + tc.target +
			"\nload:\n  stages: [{duration: 10s, target: 5}]\nsteps:\n  - path: /a\n" + tc.step + "\n"
		_, err := Parse([]byte(y))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q / %q: err = %v, want %q", tc.target, tc.step, err, tc.want)
		}
	}
}
