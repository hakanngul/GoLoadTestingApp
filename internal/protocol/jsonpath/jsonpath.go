// Package jsonpath is a tiny dot-path evaluator over values decoded by
// encoding/json (map[string]any, []any, string, float64 / json.Number,
// bool, nil).
//
// Supported syntax:
//
//	a.b.c           object member access
//	items[0].id     array index after a member
//	[2]             array index on the root
//	a[1][0]         chained indexes
//	$.a.b  /  $     optional leading root marker
//
// Filters, wildcards, slices and quoted member names are intentionally not
// supported; scenarios only need to pick out a single value.
package jsonpath

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// segment is one step of a compiled path: either a member key or an index.
type segment struct {
	key   string
	index int
	isIdx bool
}

// Path is a compiled dot path. The zero value (and the path "$" or "")
// selects the document itself. A Path is immutable and safe for concurrent
// use.
type Path struct {
	raw  string
	segs []segment
}

// String returns the path as originally written.
func (p Path) String() string { return p.raw }

// Compile parses a dot path. It returns an error for malformed input such
// as unbalanced brackets, non-numeric or negative indexes, or empty keys.
func Compile(path string) (Path, error) {
	p := Path{raw: path}
	s := strings.TrimSpace(path)
	switch {
	case s == "$" || s == "":
		return p, nil
	case strings.HasPrefix(s, "$."):
		s = s[2:]
	case strings.HasPrefix(s, "$["):
		s = s[1:]
	}
	for len(s) > 0 {
		switch s[0] {
		case '[':
			end := strings.IndexByte(s, ']')
			if end < 0 {
				return Path{}, fmt.Errorf("jsonpath %q: missing ']'", path)
			}
			n, err := strconv.Atoi(strings.TrimSpace(s[1:end]))
			if err != nil || n < 0 {
				return Path{}, fmt.Errorf("jsonpath %q: invalid index %q", path, s[1:end])
			}
			p.segs = append(p.segs, segment{index: n, isIdx: true})
			s = s[end+1:]
			if len(s) > 0 && s[0] == '.' {
				s = s[1:]
				if len(s) == 0 || s[0] == '.' || s[0] == '[' {
					return Path{}, fmt.Errorf("jsonpath %q: empty key", path)
				}
			}
		case '.', ']':
			return Path{}, fmt.Errorf("jsonpath %q: unexpected %q", path, s[0])
		default:
			end := strings.IndexAny(s, ".[]")
			if end < 0 {
				end = len(s)
			}
			if s[end:] != "" && s[end] == ']' {
				return Path{}, fmt.Errorf("jsonpath %q: unexpected ']'", path)
			}
			p.segs = append(p.segs, segment{key: s[:end]})
			s = s[end:]
			if len(s) > 0 && s[0] == '.' {
				s = s[1:]
				if len(s) == 0 || s[0] == '.' || s[0] == '[' {
					return Path{}, fmt.Errorf("jsonpath %q: empty key", path)
				}
			}
		}
	}
	return p, nil
}

// MustCompile is like Compile but panics on error. Intended for tests and
// package-level constants.
func MustCompile(path string) Path {
	p, err := Compile(path)
	if err != nil {
		panic(err)
	}
	return p
}

// Get evaluates the compiled path against doc. ok is false when any
// segment does not exist or has the wrong type (e.g. indexing an object).
// A JSON null that is present yields (nil, true).
func (p Path) Get(doc any) (any, bool) {
	cur := doc
	for _, sg := range p.segs {
		if sg.isIdx {
			arr, ok := cur.([]any)
			if !ok || sg.index >= len(arr) {
				return nil, false
			}
			cur = arr[sg.index]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[sg.key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Get compiles path and evaluates it against doc. A malformed path reports
// (nil, false). Prefer Compile + Path.Get on hot paths.
func Get(doc any, path string) (any, bool) {
	p, err := Compile(path)
	if err != nil {
		return nil, false
	}
	return p.Get(doc)
}

// String formats a decoded JSON value for use as a template variable or in
// comparisons:
//
//   - strings are returned as-is (no quotes)
//   - integral float64 values have no fractional part ("42", not "42.0")
//   - json.Number is returned verbatim (preserves large integer IDs)
//   - bools are "true" / "false", nil is "null"
//   - objects and arrays are compact JSON
func String(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}
