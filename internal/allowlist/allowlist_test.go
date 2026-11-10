package allowlist

import (
	"errors"
	"testing"
)

func TestCheck(t *testing.T) {
	l, err := Parse("api.example.com, *.staging.example.com,10.0.0.0/8, 127.0.0.1 ,::1")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://api.example.com/x", true},
		{"https://API.example.com:8443", true},
		{"http://api.example.com.", true},
		{"https://evil-api.example.com", false},
		{"https://a.staging.example.com", true},
		{"https://a.b.staging.example.com", true},
		{"https://staging.example.com", false}, // apex not covered by *.
		{"https://xstaging.example.com", false},
		{"http://10.2.3.4:8080", true},
		{"http://11.0.0.1", false},
		{"http://127.0.0.1:18080", true},
		{"http://[::1]:80", true},
		{"ws://api.example.com/socket", true},
		{"http://example.org", false},
		{"not a url", false},
	}
	for _, c := range cases {
		err := l.Check(c.url)
		if got := err == nil; got != c.want {
			t.Errorf("Check(%q) = %v, want allowed=%v", c.url, err, c.want)
		}
		if err != nil && !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Check(%q) error does not wrap ErrNotAllowed: %v", c.url, err)
		}
	}
}

func TestStarAndEmpty(t *testing.T) {
	all, _ := Parse("*")
	if !all.AllowsAll() || all.Check("https://anything.test") != nil {
		t.Error("* should allow everything")
	}
	empty, _ := Parse(" , ")
	if !empty.Empty() || empty.Check("https://a.test") == nil {
		t.Error("empty list must allow nothing")
	}
	var nilList *List
	if nilList.Check("https://a.test") == nil {
		t.Error("nil list must allow nothing")
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{"*.", "*.a*.b", "10.0.0.0/33", "host:8080", "http://x"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
}
