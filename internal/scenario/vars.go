package scenario

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// TemplateVars returns the names referenced as {{name}} in s, in order of
// first appearance, using the same parsing rules as Render (names are
// trimmed; "{{}}" and unclosed "{{" are not references).
func TemplateVars(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return out
		}
		j := strings.Index(s[i+2:], "}}")
		if j < 0 {
			return out
		}
		if key := strings.TrimSpace(s[i+2 : i+2+j]); key != "" && !slices.Contains(out, key) {
			out = append(out, key)
		}
		s = s[i+4+j:]
	}
}

// TargetURLs lists every URL a scenario sends load to: the base URL plus
// steps whose path is an absolute http(s) or ws(s) URL. (A path that is
// entirely a template, e.g. "{{url}}", is resolved per iteration and
// cannot be vetted here; see docs/security.md.)
func (s *Scenario) TargetURLs() []string {
	urls := []string{s.Target.BaseURL}
	for _, st := range s.Steps {
		scheme, _, ok := strings.Cut(strings.ToLower(st.Path), "://")
		if !ok {
			continue
		}
		switch scheme {
		case "http", "https", "ws", "wss":
			urls = append(urls, st.Path)
		}
	}
	return urls
}

// VarIssue is a {{var}} reference that no source defines (see CheckVars).
type VarIssue struct {
	// Path locates the template, e.g. "steps[1].headers.X-Id".
	Path string
	// Step is the index of the step (-1 for auth / target fields).
	Step int
	// Var is the referenced name.
	Var string
	// Msg explains the problem.
	Msg string
}

func (v VarIssue) String() string { return v.Path + ": " + v.Msg }

// CheckVars reports {{var}} references that no source defines, so that
// they would be sent literally. Sources are the CSV columns (data.file),
// the variables extracted by auth.login (visible to every step) and the
// extracts of earlier steps.
//
// columns are the CSV header names. Pass columnsKnown=false when the
// scenario has a data file whose header is not available: any name could
// then come from it, so only references to variables that are extracted
// by a later (or the same) step are reported. Without a data block the
// CSV defines nothing.
func (s *Scenario) CheckVars(columns []string, columnsKnown bool) []VarIssue {
	csvUnknown := s.Data != nil && !columnsKnown
	csv := make(map[string]bool, len(columns))
	if s.Data != nil {
		for _, c := range columns {
			csv[strings.TrimSpace(c)] = true
		}
	}
	authVars := map[string]bool{}
	if a := s.Auth; a != nil && a.Type == AuthLogin && a.Login != nil {
		for _, x := range a.Login.Extract {
			authVars[strings.TrimSpace(x.Var)] = true
		}
	}
	// Where each variable is extracted (first step index).
	extractedAt := map[string]int{}
	for i := range s.Steps {
		for _, x := range s.Steps[i].Extract {
			v := strings.TrimSpace(x.Var)
			if _, ok := extractedAt[v]; !ok {
				extractedAt[v] = i
			}
		}
	}

	var out []VarIssue
	check := func(path string, step int, tmpl string, defined map[string]bool) {
		for _, v := range TemplateVars(tmpl) {
			if csv[v] || defined[v] || v == "_auth_header" {
				continue
			}
			if j, ok := extractedAt[v]; ok && step >= 0 && j >= step {
				who := "this step"
				if j > step {
					who = fmt.Sprintf("steps[%d] (%s), which runs later", j, s.Steps[j].Name)
				}
				out = append(out, VarIssue{Path: path, Step: step, Var: v,
					Msg: fmt.Sprintf("{{%s}} is extracted by %s; only extracts of earlier steps are available, so it would be sent literally", v, who)})
				continue
			}
			if csvUnknown {
				continue // may be a CSV column
			}
			src := "no CSV column"
			if s.Data == nil {
				src = "no data file"
			}
			out = append(out, VarIssue{Path: path, Step: step, Var: v,
				Msg: fmt.Sprintf("{{%s}} is not defined (%s, no extract in an earlier step, no auth variable); it would be sent literally", v, src)})
		}
	}
	checkHeaders := func(path string, step int, h map[string]string, defined map[string]bool) {
		for _, k := range slices.Sorted(maps.Keys(h)) {
			check(path+"."+k, step, h[k], defined)
		}
	}

	none := map[string]bool{}
	if a := s.Auth; a != nil {
		switch a.Type {
		case AuthLogin:
			if l := a.Login; l != nil {
				check("auth.login.path", -1, l.Path, none)
				checkHeaders("auth.login.headers", -1, l.Headers, none)
				check("auth.login.body", -1, l.Body, none)
			}
			if a.InjectEnabled() {
				check("auth.value", -1, a.Value, authVars)
			}
		case AuthBearer:
			check("auth.token", -1, a.Token, none)
			if a.Value != DefaultAuthValue {
				check("auth.value", -1, Render(a.Value, map[string]string{"token": ""}), none)
			}
		case AuthBasic:
			check("auth.username", -1, a.Username, none)
			check("auth.password", -1, a.Password, none)
		}
	}
	checkHeaders("target.headers", -1, s.Target.Headers, authVars)

	defined := maps.Clone(authVars)
	for i := range s.Steps {
		st := &s.Steps[i]
		p := fmt.Sprintf("steps[%d]", i)
		check(p+".path", i, st.Path, defined)
		checkHeaders(p+".headers", i, st.Headers, defined)
		check(p+".body", i, st.Body, defined)
		if ws := st.WS; ws != nil {
			for j, m := range ws.Messages {
				mp := fmt.Sprintf("%s.ws.messages[%d]", p, j)
				check(mp+".send", i, m.Send, defined)
				check(mp+".expect", i, m.Expect, defined)
			}
			check(p+".ws.ping_message", i, ws.PingMessage, defined)
		}
		for _, x := range st.Extract {
			defined[strings.TrimSpace(x.Var)] = true
		}
	}
	return out
}
