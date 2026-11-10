package trial

import (
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// maxValueLen caps extracted / data values in reports.
const maxValueLen = 200

// sensitiveWords mark header, field, variable and parameter names whose
// values are masked.
var sensitiveWords = []string{
	"pass", "pwd", "secret", "token", "auth", "cookie", "session",
	"apikey", "api_key", "api-key", "signature", "credential", "private",
}

func sensitiveName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range sensitiveWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

var jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)

// mask hides a secret value but keeps an auth scheme ("Bearer ") and
// unrendered {{templates}}, so a user can still see whether it was set.
func mask(v string) string {
	if v == "" || strings.Contains(v, "{{") {
		return v
	}
	prefix := ""
	if sch, rest, ok := strings.Cut(v, " "); ok && !strings.Contains(sch, "=") && len(sch) <= 12 {
		prefix, v = sch+" ", rest
	}
	n := utf8.RuneCountInString(v)
	if n >= 12 {
		r := []rune(v)
		return fmt.Sprintf("%s%s… (%d chars)", prefix, string(r[:4]), n)
	}
	return prefix + "***"
}

// maskVar masks sensitive variables and truncates long values.
func maskVar(name, v string) string {
	if sensitiveName(name) || jwtRe.MatchString(v) {
		return mask(v)
	}
	if utf8.RuneCountInString(v) > maxValueLen {
		return string([]rune(v)[:maxValueLen]) + "…"
	}
	return v
}

func maskVars(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = maskVar(k, v)
	}
	return out
}

var (
	jsonFieldRe = regexp.MustCompile(`"([^"\\]{1,64})"(\s*:\s*)"((?:[^"\\]|\\.)*)"`)
	formFieldRe = regexp.MustCompile(`(^|[&?])([A-Za-z0-9_.\-\[\]]{1,64})=([^&\s]*)`)
)

// redactBody masks values of sensitive JSON / form fields and JWTs.
func redactBody(s string) string {
	s = jsonFieldRe.ReplaceAllStringFunc(s, func(m string) string {
		p := jsonFieldRe.FindStringSubmatch(m)
		if !sensitiveName(p[1]) {
			return m
		}
		return `"` + p[1] + `"` + p[2] + `"` + mask(p[3]) + `"`
	})
	if !strings.HasPrefix(strings.TrimSpace(s), "{") && !strings.HasPrefix(strings.TrimSpace(s), "[") {
		s = formFieldRe.ReplaceAllStringFunc(s, func(m string) string {
			p := formFieldRe.FindStringSubmatch(m)
			if !sensitiveName(p[2]) {
				return m
			}
			return p[1] + p[2] + "=" + mask(p[3])
		})
	}
	return jwtRe.ReplaceAllStringFunc(s, mask)
}

// redactURL masks sensitive query parameters.
func redactURL(raw string) string {
	base, q, ok := strings.Cut(raw, "?")
	if !ok {
		return raw
	}
	parts := strings.Split(q, "&")
	for i, p := range parts {
		if k, v, ok := strings.Cut(p, "="); ok && (sensitiveName(k) || jwtRe.MatchString(v)) {
			parts[i] = k + "=" + mask(v)
		}
	}
	return base + "?" + strings.Join(parts, "&")
}

// requestHeaders returns the sent headers sorted, sensitive ones masked.
func requestHeaders(h http.Header, host string) []model.TryHeader {
	out := []model.TryHeader{}
	if host != "" {
		out = append(out, model.TryHeader{Name: "Host", Value: host})
	}
	for _, k := range slices.Sorted(maps.Keys(h)) {
		v := strings.Join(h[k], ", ")
		if sensitiveName(k) || jwtRe.MatchString(v) {
			v = mask(v)
		}
		out = append(out, model.TryHeader{Name: k, Value: v})
	}
	return out
}

// shownResponseHeaders are the response headers worth showing.
var shownResponseHeaders = []string{
	"Content-Type", "Content-Length", "Location", "Server", "Set-Cookie",
	"Cf-Ray", "Cf-Mitigated", "Cf-Cache-Status", "Retry-After", "Www-Authenticate",
	"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset",
	"Via", "X-Cache", "X-Powered-By", "X-Request-Id", "Strict-Transport-Security",
}

// responseHeaders selects shownResponseHeaders; Set-Cookie shows cookie
// names only.
func responseHeaders(h http.Header) []model.TryHeader {
	out := []model.TryHeader{}
	for _, k := range shownResponseHeaders {
		vs := h.Values(k)
		if len(vs) == 0 {
			continue
		}
		if k == "Set-Cookie" {
			names := make([]string, 0, len(vs))
			for _, c := range vs {
				n, _, _ := strings.Cut(c, "=")
				names = append(names, strings.TrimSpace(n))
			}
			out = append(out, model.TryHeader{Name: k, Value: strings.Join(names, ", ") + " (values hidden)"})
			continue
		}
		out = append(out, model.TryHeader{Name: k, Value: strings.Join(vs, ", ")})
	}
	return out
}

// capText cuts s to at most n bytes on a rune boundary.
func capText(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// isBinary reports whether a body should not be shown as text.
func isBinary(contentType string, b []byte) bool {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "text/"), strings.Contains(ct, "json"), strings.Contains(ct, "xml"),
		strings.Contains(ct, "javascript"), strings.Contains(ct, "x-www-form-urlencoded"),
		strings.Contains(ct, "html"):
		// declared text; still verify below
	case ct == "":
	default:
		return true
	}
	sample := b[:min(len(b), 1024)]
	if slices.Contains(sample, 0) {
		return true
	}
	// A multi-byte rune cut at the end of the sample is fine.
	for k := 0; k < 3 && len(sample) > 0 && !utf8.Valid(sample) && len(sample) < len(b); k++ {
		sample = sample[:len(sample)-1]
	}
	return !utf8.Valid(sample)
}
