package scenario

import (
	"slices"
	"strings"
	"testing"
)

func TestTemplateVars(t *testing.T) {
	got := TemplateVars("/u/{{ id }}/{{x}}?a={{id}}&b={{}}&c={{open")
	if want := []string{"id", "x"}; !slices.Equal(got, want) {
		t.Fatalf("TemplateVars = %v, want %v", got, want)
	}
}

const varsYAML = `
name: vars
target:
  base_url: https://api.test
  headers: {X-Tenant: "{{tenant}}"}
load:
  stages: [{duration: 1s, target: 1}]
%DATA%
auth:
  type: login
  login:
    path: /login
    body: '{"u":"{{user}}","p":"{{pass}}"}'
    extract: [{var: token, from: json, expr: token}]
steps:
  - name: early
    path: /orders/{{order_id}}
    headers: {X-Token: "{{token}}"}
  - name: create
    method: POST
    path: /orders
    body: '{"sku":"{{sku}}"}'
    extract: [{var: order_id, from: json, expr: id}]
  - name: get
    path: /orders/{{order_id}}?q={{typo}}
`

func varsScenario(t *testing.T, data string) *Scenario {
	t.Helper()
	s, err := Parse([]byte(strings.Replace(varsYAML, "%DATA%", data, 1)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func issueSet(is []VarIssue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Path+" "+i.Var)
	}
	slices.Sort(out)
	return out
}

func TestCheckVarsKnownColumns(t *testing.T) {
	s := varsScenario(t, "data: {file: users.csv}")
	got := issueSet(s.CheckVars([]string{"user", "pass", "sku"}, true))
	want := []string{
		"steps[0].path order_id", // extracted later
		"steps[2].path typo",
		"target.headers.X-Tenant tenant",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("issues = %v, want %v", got, want)
	}
	for _, is := range s.CheckVars([]string{"user", "pass", "sku"}, true) {
		if is.Var == "order_id" && !strings.Contains(is.Msg, "runs later") {
			t.Errorf("order_id msg = %q", is.Msg)
		}
	}
}

func TestCheckVarsUnknownColumns(t *testing.T) {
	// Data file header unknown: only the use-before-extract is reported.
	s := varsScenario(t, "data: {file: users.csv}")
	if got := issueSet(s.CheckVars(nil, false)); !slices.Equal(got, []string{"steps[0].path order_id"}) {
		t.Fatalf("issues = %v", got)
	}
}

func TestCheckVarsNoData(t *testing.T) {
	s := varsScenario(t, "")
	got := issueSet(s.CheckVars([]string{"ignored"}, false))
	for _, w := range []string{"auth.login.body user", "auth.login.body pass", "steps[1].body sku"} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	for _, is := range s.CheckVars(nil, true) {
		if is.Var == "sku" && !strings.Contains(is.Msg, "no data file") {
			t.Errorf("msg = %q", is.Msg)
		}
	}
}
