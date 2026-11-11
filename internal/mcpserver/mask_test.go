package mcpserver

import (
	"strings"
	"testing"
)

func TestMaskHeader(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"Authorization", "Bearer abc123", Mask},
		{"authorization", "Basic dXNlcjpwdw==", Mask},
		{"Cookie", "sid=1", Mask},
		{"Set-Cookie", "sid=1; Path=/", Mask},
		{"X-Api-Key", "k-123", Mask},
		{"X-Auth-Token", "t", Mask},
		{"X-Client-Secret", "s", Mask},
		{"X-My-Password", "p", Mask},
		{"Content-Type", "application/json", "application/json"},
		{"Authorization", "Bearer {{token}}", "Bearer {{token}}"},
		{"X-Api-Key", "{{key}}", "{{key}}"},
		{"Cookie", "sid={{sid}}", Mask}, // a partly literal value may leak
		{"Authorization", "", ""},
	}
	for _, c := range cases {
		if got := MaskHeader(c.name, c.value); got != c.want {
			t.Errorf("MaskHeader(%q, %q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

func TestMaskBody(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"user":"bob","password":"hunter2"}`, `{"user":"bob","password":"***"}`},
		{`{"access_token": "abc", "n": 1}`, `{"access_token": "***", "n": 1}`},
		{`{"apiKey":"k","API_KEY":"k2","client_secret":"s"}`, `{"apiKey":"***","API_KEY":"***","client_secret":"***"}`},
		{`{"password":"{{pw}}","token":{{tok}}}`, `{"password":"{{pw}}","token":{{tok}}}`},
		{`{"passcode":12345,"ok":true}`, `{"passcode":"***","ok":true}`},
		{`{"nested":{"refresh_token":"r"}}`, `{"nested":{"refresh_token":"***"}}`},
		{`{"token":"abc`, `{"token":"***"`}, // a truncated value is masked too
		{`username=bob&password=hunter2&x=1`, `username=bob&password=***&x=1`},
		{`grant_type=x&client_secret=s`, `grant_type=x&client_secret=***`},
		{`password={{pw}}`, `password={{pw}}`},
		{`plain text with no secret`, `plain text with no secret`},
		{`{"user":"alice"}`, `{"user":"alice"}`},
	}
	for _, c := range cases {
		if got := MaskBody(c.in); got != c.want {
			t.Errorf("MaskBody(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/api?token=abc&x=1", "/api?token=***&x=1"},
		{"/api?x=1&api_key=k", "/api?x=1&api_key=***"},
		{"/api?token={{t}}", "/api?token={{t}}"},
		{"https://bob:hunter2@example.com/x", "https://bob:***@example.com/x"},
		{"https://example.com/search?q=token", "https://example.com/search?q=token"},
		{"/plain/path", "/plain/path"},
	}
	for _, c := range cases {
		if got := MaskURL(c.in); got != c.want {
			t.Errorf("MaskURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

const secretYAML = `name: shop
target:
  base_url: https://shop.example.test
  headers:
    X-Api-Key: key-123456
    X-Trace: {{trace}}
auth:
  type: login
  login:
    method: POST
    path: /login?token=abc
    headers:
      Content-Type: application/json
    body: '{"username":"{{user}}","password":"hunter2"}'
    extract:
      - {var: tok, from: json, expr: access_token}
  header: Authorization
  value: "Bearer {{tok}}"
load:
  stages: [{duration: 10s, target: 5}]
steps:
  - name: list
    path: /items
    headers:
      Authorization: Bearer live-secret-token
      Cookie: "sid={{sid}}"
      Accept: application/json
  - name: post
    method: POST
    path: /items
    body: '{"name":"x","api_key":"k-1","secret_note":"{{note}}"}'
`

func TestMaskYAML(t *testing.T) {
	// {{trace}} is not valid plain YAML, quote it as a user would.
	src := strings.Replace(secretYAML, "X-Trace: {{trace}}", `X-Trace: "{{trace}}"`, 1)
	got, err := MaskYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"key-123456", "live-secret-token", "hunter2", "k-1", "token=abc", "sid={{sid}}"} {
		if strings.Contains(got, leak) {
			t.Errorf("masked YAML still contains %q:\n%s", leak, got)
		}
	}
	for _, keep := range []string{"{{trace}}", "{{user}}", "Bearer {{tok}}", "{{note}}", "shop.example.test", "application/json", "/items", "name: shop"} {
		if !strings.Contains(got, keep) {
			t.Errorf("masked YAML lost %q:\n%s", keep, got)
		}
	}
	if !strings.Contains(got, `"***"`) {
		t.Errorf("no mask in output:\n%s", got)
	}
}

func TestMaskYAMLUsernameAndAuth(t *testing.T) {
	src := `name: a
target: {base_url: "https://a.example.test"}
auth: {type: basic, username: admin, password: "{{pw}}"}
load: {stages: [{duration: 1s, target: 1}]}
steps: [{path: /}]
`
	got, err := MaskYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "admin") || !strings.Contains(got, "{{pw}}") {
		t.Errorf("auth masking wrong:\n%s", got)
	}
	b := `name: b
target: {base_url: "https://b.example.test"}
auth: {type: bearer, token: sekrit}
load: {stages: [{duration: 1s, target: 1}]}
steps: [{path: /}]
`
	got, _ = MaskYAML(b)
	if strings.Contains(got, "sekrit") {
		t.Errorf("token leaked:\n%s", got)
	}
}

func TestUnmaskYAMLRoundTrip(t *testing.T) {
	src := strings.Replace(secretYAML, "X-Trace: {{trace}}", `X-Trace: "{{trace}}"`, 1)
	masked, err := MaskYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	// The model changes the load and leaves the masked values alone.
	edited := strings.Replace(masked, "target: 5", "target: 7", 1)
	got, err := UnmaskYAML(edited, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"key-123456", "live-secret-token", "hunter2", "target: 7", "{{trace}}"} {
		if !strings.Contains(got, want) {
			t.Errorf("restored YAML lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, Mask) {
		t.Errorf("mask left in restored YAML:\n%s", got)
	}
}

func TestUnmaskYAMLRefusesUnmatched(t *testing.T) {
	orig := `name: a
target: {base_url: "https://a.example.test"}
load: {stages: [{duration: 1s, target: 1}]}
steps: [{path: /}]
`
	edited := `name: a
target: {base_url: "https://a.example.test", headers: {X-Api-Key: "***"}}
load: {stages: [{duration: 1s, target: 1}]}
steps: [{path: /}]
`
	_, err := UnmaskYAML(edited, orig)
	if err == nil || !strings.Contains(err.Error(), "target.headers.X-Api-Key") {
		t.Fatalf("err = %v, want a refusal naming target.headers.X-Api-Key", err)
	}
	// Without a mask the text is passed through untouched.
	if got, err := UnmaskYAML(orig, orig); err != nil || got != orig {
		t.Errorf("plain yaml changed: %v", err)
	}
}
