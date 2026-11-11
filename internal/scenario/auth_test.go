package scenario

import (
	"errors"
	"strings"
	"testing"
)

const authBase = `
name: t
target:
  base_url: https://api.example.test
load:
  stages: [{duration: 10s, target: 5}]
steps:
  - path: /me
`

func TestAuthLoginDefaults(t *testing.T) {
	s, err := Parse([]byte(authBase + `
auth:
  type: login
  login:
    path: /login
    body: '{"u":"{{username}}"}'
    extract: [{var: token, from: json, expr: access_token}]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := s.Auth
	checks := []struct {
		name      string
		got, want any
	}{
		{"scope", a.Scope, AuthPerUser},
		{"header", a.Header, "Authorization"},
		{"value", a.Value, "Bearer {{token}}"},
		{"login method", a.Login.Method, "POST"},
		{"login name", a.Login.Name, AuthLoginStep},
		{"inject", a.InjectEnabled(), true},
		{"template", a.ValueTemplate(), "Bearer {{token}}"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// Idempotent.
	s.ApplyDefaults()
	if err := s.Validate(); err != nil {
		t.Fatalf("re-validate: %v", err)
	}
}

func TestAuthBearerAndBasicDefaults(t *testing.T) {
	s, err := Parse([]byte(authBase + `
auth: {type: bearer, token: "{{api_key}}"}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Auth.ValueTemplate(); got != "Bearer {{api_key}}" {
		t.Errorf("bearer template = %q", got)
	}
	if s.Auth.Scope != "" {
		t.Errorf("scope defaulted on bearer: %q", s.Auth.Scope)
	}

	s, err = Parse([]byte(authBase + `
auth: {type: bearer, token: abc, header: X-Api-Key, value: "{{token}}"}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Auth.ValueTemplate(); got != "abc" {
		t.Errorf("custom bearer template = %q", got)
	}

	s, err = Parse([]byte(authBase + `
auth: {type: basic, username: u, password: p, inject: false}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Auth.Value != "" || s.Auth.Header != "Authorization" || s.Auth.InjectEnabled() {
		t.Errorf("basic defaults: %+v", s.Auth)
	}
}

func TestAuthValidation(t *testing.T) {
	cases := []struct {
		name  string
		auth  string
		paths []string
	}{
		{"bad type", `{type: oauth}`, []string{"auth.type"}},
		{"login missing", `{type: login}`, []string{"auth.login"}},
		{"login no extract", `{type: login, login: {path: /login}}`, []string{"auth.login.extract"}},
		{"login default value needs token", `{type: login, login: {path: /login, extract: [{var: jwt, from: json, expr: a}]}}`, []string{"auth.login.extract"}},
		{"login ws", `{type: login, login: {type: ws, path: /ws, ws: {}, extract: [{var: token, from: json, expr: a}]}}`, []string{"auth.login.type"}},
		{"login bad step", `{type: login, login: {method: FETCH, path: login, extract: [{var: token, from: xml, expr: a}]}}`,
			[]string{"auth.login.method", "auth.login.path", "auth.login.extract[0].from"}},
		{"login think", `{type: login, login: {path: /l, think_time: 1s, extract: [{var: token, from: json, expr: a}]}}`, []string{"auth.login.think_time"}},
		{"login bad scope/refresh", `{type: login, scope: global, refresh: -1s, login: {path: /l, extract: [{var: token, from: json, expr: a}]}}`,
			[]string{"auth.scope", "auth.refresh"}},
		{"login with token", `{type: login, token: x, username: u, login: {path: /l, extract: [{var: token, from: json, expr: a}]}}`,
			[]string{"auth.token", "auth.username"}},
		{"bearer missing token", `{type: bearer}`, []string{"auth.token"}},
		{"bearer with login fields", `{type: bearer, token: x, scope: shared, refresh: 1m, login: {path: /l}}`,
			[]string{"auth.scope", "auth.refresh", "auth.login"}},
		{"basic missing", `{type: basic}`, []string{"auth.username", "auth.password"}},
		{"basic value", `{type: basic, username: u, password: p, value: x}`, []string{"auth.value"}},
		{"basic token", `{type: basic, username: u, password: p, token: t}`, []string{"auth.token"}},
		{"bad header", `{type: bearer, token: x, header: "X Bad"}`, []string{"auth.header"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(authBase + "auth: " + c.auth + "\n"))
			if err == nil {
				t.Fatal("expected error")
			}
			got := fieldPaths(err)
			for _, p := range c.paths {
				if !strings.Contains(got, "|"+p+"|") {
					t.Errorf("missing error at %s; got %v", p, err)
				}
			}
		})
	}
}

func TestAuthValidationOK(t *testing.T) {
	for _, a := range []string{
		`{type: login, value: "Token {{jwt}}", login: {path: /l, extract: [{var: jwt, from: json, expr: a}]}}`,
		`{type: login, inject: false, login: {path: /l, extract: [{var: jwt, from: json, expr: a}]}}`,
		`{type: login, inject: false, login: {path: /l}}`, // session cookie only
		`{type: login, scope: shared, refresh: 10m, login: {method: put, path: /l, extract: [{var: token, from: header, expr: X-Token}]}}`,
		`{type: bearer, token: "{{api_key}}"}`,
		`{type: basic, username: "{{user}}", password: "{{pass}}", header: Proxy-Authorization}`,
	} {
		if _, err := Parse([]byte(authBase + "auth: " + a + "\n")); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
}

// A cookie-only login (no extract) needs cookies; without them it has no effect.
func TestAuthCookieLoginNeedsCookies(t *testing.T) {
	src := strings.Replace(authBase, "  base_url: https://api.example.test\n", "  base_url: https://api.example.test\n  cookies: false\n", 1)
	_, err := Parse([]byte(src + "auth: {type: login, inject: false, login: {path: /l}}\n"))
	if err == nil || !strings.Contains(fieldPaths(err), "|auth.login.extract|") {
		t.Fatalf("want auth.login.extract error, got %v", err)
	}
}

func TestAuthReservedStepName(t *testing.T) {
	_, err := Parse([]byte(authBase + `  - name: auth:login
    path: /x
auth: {type: bearer, token: x}
`))
	if err == nil || !strings.Contains(fieldPaths(err), "|steps[1].name|") {
		t.Fatalf("want reserved name error, got %v", err)
	}
}

func fieldPaths(err error) string {
	var b strings.Builder
	b.WriteString("|")
	var walk func(error)
	walk = func(err error) {
		if j, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range j.Unwrap() {
				walk(e)
			}
			return
		}
		var fe *FieldError
		if errors.As(err, &fe) {
			b.WriteString(fe.Path + "|")
		}
	}
	walk(err)
	return b.String()
}
