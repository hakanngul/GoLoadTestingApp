package mcpserver

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mask replaces secret values in everything the model sees. {{var}}
// templates are not secrets and stay visible.
const Mask = "***"

var (
	templateRe = regexp.MustCompile(`\{\{[^}]*\}\}`)
	schemeRe   = regexp.MustCompile(`(?i)^(bearer|basic|token)\s+`)

	// jsonSecretRe matches "<key containing pass/secret/token/apikey>": <value>
	// in JSON text (also truncated or templated JSON, which a parser would
	// reject). Group 1 is everything up to the value, group 2 the value: a
	// {{template}}, a string or a bare scalar.
	jsonSecretRe = regexp.MustCompile(`(?i)("(?:[^"\\]|\\.)*(?:pass|secret|token|api[_-]?key)(?:[^"\\]|\\.)*"\s*:\s*)(\{\{[^}]*\}\}|"(?:[^"\\]|\\.)*"|[^\s,}\]]+)`)
	// formSecretRe is the same for form-encoded text (a=b&password=c).
	formSecretRe = regexp.MustCompile(`(?i)((?:^|[&?\s])[^=&\s]*(?:pass|secret|token|api[_-]?key)[^=&\s]*=)([^&\s#]*)`)
	// urlUserRe matches the user:password@ part of a URL.
	urlUserRe = regexp.MustCompile(`(?i)(^[a-z][a-z0-9+.-]*://[^/@\s:]*:)([^/@\s]*)(@)`)
)

// isTemplateOnly reports whether s holds nothing secret: it is empty, or
// only {{var}} references (optionally after a scheme such as "Bearer ").
func isTemplateOnly(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if !templateRe.MatchString(s) {
		return false
	}
	rest := strings.TrimSpace(templateRe.ReplaceAllString(s, ""))
	rest = strings.TrimSpace(schemeRe.ReplaceAllString(rest+" ", ""))
	return rest == ""
}

// secretKey reports whether a field or header name denotes a secret.
func secretKey(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "api-key"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// SensitiveHeader reports whether the values of header name are secret.
func SensitiveHeader(name string) bool {
	switch n := strings.ToLower(strings.TrimSpace(name)); n {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "x-auth-key", "www-authenticate":
		return true
	default:
		return secretKey(n) || strings.HasPrefix(n, "x-auth")
	}
}

// MaskHeader returns value, or Mask when header name is sensitive and the
// value is not just a {{template}}.
func MaskHeader(name, value string) string {
	if SensitiveHeader(name) && !isTemplateOnly(value) {
		return Mask
	}
	return value
}

// MaskBody masks secret fields of a JSON or form-encoded text (a request
// body or a response snippet). Other text is returned unchanged.
func MaskBody(s string) string {
	s = jsonSecretRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := jsonSecretRe.FindStringSubmatch(m)
		val := strings.Trim(sub[2], `"`)
		if isTemplateOnly(val) {
			return m
		}
		return sub[1] + `"` + Mask + `"`
	})
	return formSecretRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := formSecretRe.FindStringSubmatch(m)
		if isTemplateOnly(sub[2]) {
			return m
		}
		return sub[1] + Mask
	})
}

// MaskURL masks credentials in the user info and secret query parameters
// (?token=...) of a URL or path.
func MaskURL(s string) string {
	s = urlUserRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := urlUserRe.FindStringSubmatch(m)
		if isTemplateOnly(sub[2]) {
			return m
		}
		return sub[1] + Mask + sub[3]
	})
	i := strings.IndexAny(s, "?")
	if i < 0 {
		return s
	}
	return s[:i] + formSecretRe.ReplaceAllStringFunc(s[i:], func(m string) string {
		sub := formSecretRe.FindStringSubmatch(m)
		if isTemplateOnly(sub[2]) {
			return m
		}
		return sub[1] + Mask
	})
}

func maskScalar(n *yaml.Node) {
	if n.Kind != yaml.ScalarNode || isTemplateOnly(n.Value) {
		return
	}
	n.Value, n.Tag, n.Style = Mask, "!!str", yaml.DoubleQuotedStyle
}

// maskNode masks secrets in a scenario YAML tree in place.
func maskNode(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			maskNode(c)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, v := strings.ToLower(n.Content[i].Value), n.Content[i+1]
			switch {
			case key == "headers" && v.Kind == yaml.MappingNode:
				for j := 0; j+1 < len(v.Content); j += 2 {
					if SensitiveHeader(v.Content[j].Value) {
						maskScalar(v.Content[j+1])
					}
				}
			case v.Kind != yaml.ScalarNode:
				maskNode(v)
			case secretKey(key), key == "username":
				maskScalar(v)
			case key == "body":
				v.Value = MaskBody(v.Value)
			case key == "path" || key == "base_url" || key == "url":
				v.Value = MaskURL(v.Value)
			}
		}
	}
}

// MaskYAML returns the scenario YAML with secrets replaced by Mask:
// header values of Authorization, Cookie, Set-Cookie, X-Api-Key and any
// *token*, *secret*, *password* header, auth token/password/username
// values, JSON/form body fields whose key contains pass, secret, token or
// apikey, and credentials in URLs. {{var}} templates stay. The text is
// re-encoded, so comments keep their place but spacing may change.
func MaskYAML(src string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		return "", fmt.Errorf("scenario is not valid YAML: %w", err)
	}
	maskNode(&doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// UnmaskYAML restores the secrets of orig into edited, a scenario that
// was derived from MaskYAML(orig): every value that still equals its
// masked counterpart in orig (same path) is replaced by the original. It
// fails when a mask remains that has no original to restore, so a
// round-trip through the model can never write "***" over a real secret.
func UnmaskYAML(edited, orig string) (string, error) {
	var ed, or, mo yaml.Node
	if err := yaml.Unmarshal([]byte(edited), &ed); err != nil {
		return "", fmt.Errorf("scenario is not valid YAML: %w", err)
	}
	if strings.Contains(edited, Mask) {
		if err := yaml.Unmarshal([]byte(orig), &or); err != nil {
			return "", err
		}
		if err := yaml.Unmarshal([]byte(orig), &mo); err != nil {
			return "", err
		}
		maskNode(&mo)
		restore(&ed, &or, &mo)
		var left []string
		leftover(&ed, "", &left)
		if len(left) > 0 {
			sort.Strings(left)
			return "", fmt.Errorf("the yaml still contains masked values (%q) at %s: get_test hides secrets, so put the real value or a {{variable}} there, or leave the field exactly as get_test returned it to keep the stored value",
				Mask, strings.Join(left, ", "))
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&ed); err != nil {
			return "", err
		}
		if err := enc.Close(); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
	return edited, nil
}

// restore walks the edited tree next to the original and its masked copy.
func restore(ed, orig, masked *yaml.Node) {
	if ed == nil || orig == nil || masked == nil || ed.Kind != orig.Kind || ed.Kind != masked.Kind {
		return
	}
	switch ed.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for i := range ed.Content {
			if i < len(orig.Content) && i < len(masked.Content) {
				restore(ed.Content[i], orig.Content[i], masked.Content[i])
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(ed.Content); i += 2 {
			restore(ed.Content[i+1], mapChild(orig, ed.Content[i].Value), mapChild(masked, ed.Content[i].Value))
		}
	case yaml.ScalarNode:
		if ed.Value == masked.Value && masked.Value != orig.Value {
			ed.Value, ed.Tag, ed.Style = orig.Value, orig.Tag, orig.Style
		}
	}
}

func mapChild(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// leftover lists the paths of scalars that still hold a mask.
func leftover(n *yaml.Node, path string, out *[]string) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			leftover(c, path, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			leftover(c, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if path != "" {
				p = path + "." + p
			}
			leftover(n.Content[i+1], p, out)
		}
	case yaml.ScalarNode:
		if n.Value == Mask || strings.Contains(n.Value, `"`+Mask+`"`) || strings.Contains(n.Value, "="+Mask) {
			*out = append(*out, path)
		}
	}
}
