package scenario

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDurationUnmarshal(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr string
	}{
		{in: `d: 30s`, want: 30 * time.Second},
		{in: `d: 1m30s`, want: 90 * time.Second},
		{in: `d: 250ms`, want: 250 * time.Millisecond},
		{in: `d: "2h"`, want: 2 * time.Hour},
		{in: `d: 30`, wantErr: "has no unit"},
		{in: `d: 1.5`, wantErr: "has no unit"},
		{in: `d: "30"`, wantErr: "invalid duration"},
		{in: `d: soon`, wantErr: "invalid duration"},
		{in: `d: [1s]`, wantErr: "must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var v struct {
				D Duration `yaml:"d"`
			}
			err := yaml.Unmarshal([]byte(tt.in), &v)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.D.Duration != tt.want {
				t.Fatalf("got %v, want %v", v.D.Duration, tt.want)
			}
		})
	}
}

func TestDurationMarshalRoundTrip(t *testing.T) {
	in := struct {
		D Duration `yaml:"d"`
	}{D: Duration{90 * time.Second}}
	b, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "d: 1m30s" {
		t.Fatalf("marshal = %q", got)
	}
	var out struct {
		D Duration `yaml:"d"`
	}
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.D != in.D {
		t.Fatalf("round trip = %v, want %v", out.D, in.D)
	}
}
