package jsonpath

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string, useNumber bool) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	if useNumber {
		dec.UseNumber()
	}
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestGet(t *testing.T) {
	doc := decode(t, `{
		"a": {"b": {"c": "deep"}},
		"items": [{"id": 7}, {"id": 8, "tags": ["x", "y"]}],
		"n": null,
		"grid": [[1, 2], [3, 4]]
	}`, false)
	root := decode(t, `[10, 20, 30]`, false)

	tests := []struct {
		doc    any
		path   string
		want   string
		wantOK bool
	}{
		{doc, "a.b.c", "deep", true},
		{doc, "$.a.b.c", "deep", true},
		{doc, "items[0].id", "7", true},
		{doc, "items[1].tags[1]", "y", true},
		{doc, "grid[1][0]", "3", true},
		{doc, "n", "null", true},
		{doc, "a.b", `{"c":"deep"}`, true},
		{doc, "items[2].id", "", false},
		{doc, "a.x", "", false},
		{doc, "a.b.c.d", "", false},
		{doc, "a[0]", "", false},
		{doc, "items.id", "", false},
		{doc, "items[", "", false},
		{doc, "items[-1]", "", false},
		{doc, "a..b", "", false},
		{root, "[2]", "30", true},
		{root, "$[0]", "10", true},
		{root, "$", "[10,20,30]", true},
		{root, "", "[10,20,30]", true},
	}
	for _, tt := range tests {
		got, ok := Get(tt.doc, tt.path)
		if ok != tt.wantOK {
			t.Errorf("Get(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			continue
		}
		if ok && String(got) != tt.want {
			t.Errorf("Get(%q) = %q, want %q", tt.path, String(got), tt.want)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []string{"a..b", "a.", "items[", "items[x]", "items[-1]", ".a", "a]", "a.[0]", "[0]."}
	for _, p := range bad {
		if _, err := Compile(p); err == nil {
			t.Errorf("Compile(%q) = nil error, want error", p)
		}
	}
	good := []string{"a", "$.a", "$", "", "[0]", "a[0].b", "a[0][1]", "$[3].x"}
	for _, p := range good {
		if _, err := Compile(p); err != nil {
			t.Errorf("Compile(%q) = %v", p, err)
		}
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"hi", "hi"},
		{float64(42), "42"},
		{float64(-3), "-3"},
		{3.25, "3.25"},
		{float64(1e21), "1000000000000000000000"},
		{json.Number("12345678901234567890"), "12345678901234567890"},
		{true, "true"},
		{false, "false"},
		{nil, "null"},
		{map[string]any{"b": 1.0, "a": "x"}, `{"a":"x","b":1}`},
		{[]any{1.0, "two", nil}, `[1,"two",null]`},
	}
	for _, tt := range tests {
		if got := String(tt.in); got != tt.want {
			t.Errorf("String(%#v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUseNumberDocument(t *testing.T) {
	doc := decode(t, `{"id": 9007199254740993}`, true)
	v, ok := Get(doc, "id")
	if !ok || String(v) != "9007199254740993" {
		t.Fatalf("got %v %v", v, ok)
	}
}
