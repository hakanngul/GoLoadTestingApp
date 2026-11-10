package scenario

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const wsYAML = `
name: chat
target:
  base_url: https://chat.example.test
load:
  model: closed
  stages: [{duration: 10s, target: 5}]
steps:
  - name: login
    method: post
    path: /login
    extract: [{var: token, from: json, expr: token}]
  - name: chat
    type: WS
    path: /ws?token={{token}}
    headers: {X-Client: load}
    think_time: 1s
    ws:
      hold: 30s
      ping_every: 10s
      ping_message: '{"type":"ping"}'
      messages:
        - send: '{"type":"join","room":"{{room}}"}'
          expect: '"joined"'
          timeout: 5s
        - send: hello
        - expect: welcome
    extract: [{var: member, from: regex, expr: 'id=(\d+)'}]
  - type: ws
    path: wss://other.example.test/feed
    ws: {}
`

func TestParseWS(t *testing.T) {
	s, err := Parse([]byte(wsYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Steps[0].IsWS() || s.Steps[0].Type != "" {
		t.Errorf("http step type = %q, want empty", s.Steps[0].Type)
	}
	chat := s.Steps[1]
	if !chat.IsWS() || chat.Type != StepWS {
		t.Fatalf("type = %q, want ws (normalised)", chat.Type)
	}
	if chat.Method != "" {
		t.Errorf("ws step got method %q", chat.Method)
	}
	ws := chat.WS
	if ws.Hold.Duration != 30*time.Second || ws.PingEvery.Duration != 10*time.Second ||
		ws.PingMessage != `{"type":"ping"}` || len(ws.Messages) != 3 ||
		ws.Messages[0].Timeout.Duration != 5*time.Second || ws.Messages[2].Expect != "welcome" {
		t.Errorf("ws block = %+v", ws)
	}
	if got := s.Steps[2].Name; got != "WS wss://other.example.test/feed" {
		t.Errorf("default ws name = %q", got)
	}

	// Round trip: marshal and parse again to the same thing.
	out, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Parse(out)
	if err != nil {
		t.Fatalf("re-Parse: %v\n%s", err, out)
	}
	if s2.Steps[1].WS.Messages[0].Send != ws.Messages[0].Send || !s2.Steps[1].IsWS() {
		t.Errorf("round trip lost ws data:\n%s", out)
	}
}

// Existing (http-only) scenarios must not gain new keys when marshalled.
func TestHTTPStepsMarshalWithoutWSKeys(t *testing.T) {
	s, err := Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type:", "ws:"} {
		if strings.Contains(string(out), k) {
			t.Errorf("marshalled http scenario contains %q:\n%s", k, out)
		}
	}
}

func TestValidateWSErrors(t *testing.T) {
	d := func(v time.Duration) Duration { return Duration{v} }
	wsValid := func() Step {
		return Step{Name: "chat", Type: StepWS, Path: "/ws", WS: &WSStep{
			Hold:     d(time.Second),
			Messages: []WSMessage{{Send: "x", Expect: "y"}},
		}}
	}
	tests := []struct {
		name   string
		mutate func(st *Step)
		path   string
	}{
		{"unknown type", func(st *Step) { st.Type = "grpc" }, "steps[1].type"},
		{"ws block on http", func(st *Step) { st.Type = StepHTTP }, "steps[1].ws"},
		{"missing ws block", func(st *Step) { st.WS = nil }, "steps[1].ws"},
		{"method", func(st *Step) { st.Method = "GET" }, "steps[1].method"},
		{"body", func(st *Step) { st.Body = "x" }, "steps[1].body"},
		{"assert", func(st *Step) { st.Assert = []Assert{{Type: AssertStatus, Value: "200", Op: "eq"}} }, "steps[1].assert"},
		{"path", func(st *Step) { st.Path = "http://x/ws" }, "steps[1].path"},
		{"think", func(st *Step) { st.ThinkTime = d(-time.Second) }, "steps[1].think_time"},
		{"header", func(st *Step) { st.Headers = map[string]string{" ": "v"} }, "steps[1].headers"},
		{"hold", func(st *Step) { st.WS.Hold = d(-time.Second) }, "steps[1].ws.hold"},
		{"ping_every negative", func(st *Step) { st.WS.PingEvery = d(-time.Second) }, "steps[1].ws.ping_every"},
		{"ping_every without hold", func(st *Step) { st.WS.Hold = d(0); st.WS.PingEvery = d(time.Second) }, "steps[1].ws.ping_every"},
		{"ping_message without ping_every", func(st *Step) { st.WS.PingMessage = "p" }, "steps[1].ws.ping_message"},
		{"empty message", func(st *Step) { st.WS.Messages = append(st.WS.Messages, WSMessage{}) }, "steps[1].ws.messages[1]"},
		{"negative timeout", func(st *Step) { st.WS.Messages[0].Timeout = d(-time.Second) }, "steps[1].ws.messages[0].timeout"},
		{"timeout without expect", func(st *Step) { st.WS.Messages[0] = WSMessage{Send: "x", Timeout: d(time.Second)} }, "steps[1].ws.messages[0].timeout"},
		{"extract from header", func(st *Step) { st.Extract = []Extract{{Var: "v", From: FromHeader, Expr: "X"}} }, "steps[1].extract[0].from"},
		{"extract empty var", func(st *Step) { st.Extract = []Extract{{From: FromJSON, Expr: "a"}} }, "steps[1].extract[0].var"},
		{"extract empty expr", func(st *Step) { st.Extract = []Extract{{Var: "v", From: FromJSON}} }, "steps[1].extract[0].expr"},
		{"extract bad regex", func(st *Step) { st.Extract = []Extract{{Var: "v", From: FromRegex, Expr: "("}} }, "steps[1].extract[0].expr"},
		{"extract regex no group", func(st *Step) { st.Extract = []Extract{{Var: "v", From: FromRegex, Expr: "x"}} }, "steps[1].extract[0].expr"},
		{"extract without expect", func(st *Step) {
			st.WS.Messages = []WSMessage{{Send: "x"}}
			st.Extract = []Extract{{Var: "v", From: FromJSON, Expr: "a"}}
		}, "steps[1].extract"},
		{"empty name", func(st *Step) { st.Name = " " }, "steps[1].name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			st := wsValid()
			tc.mutate(&st)
			s.Steps = append(s.Steps, st)
			err := s.Validate()
			if !hasPath(err, tc.path) {
				t.Fatalf("want error at %s, got %v", tc.path, err)
			}
		})
	}

	// And the unmutated ws step is valid, including ws:// URLs and extract.
	s := valid()
	st := wsValid()
	st.Path = "WSS://h/x"
	st.Extract = []Extract{{Var: "v", From: FromJSON, Expr: "a.b"}}
	s.Steps = append(s.Steps, st)
	if err := s.Validate(); err != nil {
		t.Fatalf("valid ws step rejected: %v", err)
	}
}

func TestParseWSRejectsUnknownKeys(t *testing.T) {
	y := strings.Replace(wsYAML, "hold: 30s", "hold: 30s\n      holdd: 1s", 1)
	if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "holdd") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyDefaultsWSIdempotent(t *testing.T) {
	s, err := Parse([]byte(wsYAML))
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyDefaults()
	if err := s.Validate(); err != nil {
		t.Fatalf("second ApplyDefaults broke validation: %v", err)
	}
	if s.Steps[1].Method != "" {
		t.Errorf("ws method became %q", s.Steps[1].Method)
	}
}
