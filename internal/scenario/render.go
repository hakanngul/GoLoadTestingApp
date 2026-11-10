package scenario

import "strings"

// Render replaces {{name}} placeholders with values from vars.
// Unknown placeholders are left untouched so they surface in responses/logs.
func Render(s string, vars map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(s[i+2:], "}}")
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		key := strings.TrimSpace(s[i+2 : i+2+j])
		b.WriteString(s[:i])
		if v, ok := vars[key]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i : i+4+j])
		}
		s = s[i+4+j:]
	}
}
