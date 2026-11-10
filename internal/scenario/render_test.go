package scenario

import "testing"

func TestRender(t *testing.T) {
	vars := map[string]string{"user": "alice", "id": "42", "empty": ""}
	tests := []struct {
		name, in, want string
	}{
		{"no placeholders", "/health", "/health"},
		{"single", "/users/{{id}}", "/users/42"},
		{"multiple", "{{user}}:{{id}}", "alice:42"},
		{"spaces trimmed", "/u/{{ user }}", "/u/alice"},
		{"empty value", "x{{empty}}y", "xy"},
		{"unknown kept", "/u/{{nope}}/{{id}}", "/u/{{nope}}/42"},
		{"unclosed kept", "/u/{{id", "/u/{{id"},
		{"unclosed after valid", "{{id}} and {{user", "42 and {{user"},
		{"lone closing braces", "a}}b", "a}}b"},
		{"empty key", "{{}}", "{{}}"},
		{"json body", `{"u":"{{user}}"}`, `{"u":"alice"}`},
		{"adjacent", "{{id}}{{id}}", "4242"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Render(tt.in, vars); got != tt.want {
				t.Fatalf("Render(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
