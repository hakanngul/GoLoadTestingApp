// Package scenario defines the YAML scenario format (frozen in Phase 0).
//
// CONTRACT FILE: field names and YAML keys are shared by engine, protocol,
// metrics and (later) the UI form. Change only with a plan update.
package scenario

import "time"

// Scenario is one load test definition loaded from a single YAML file.
type Scenario struct {
	Name       string      `yaml:"name"`
	Target     Target      `yaml:"target"`
	Load       Load        `yaml:"load"`
	Data       *Data       `yaml:"data,omitempty"`
	Auth       *Auth       `yaml:"auth,omitempty"`
	Steps      []Step      `yaml:"steps"`
	Thresholds []Threshold `yaml:"thresholds,omitempty"`
}

// Target holds connection-level settings shared by every step.
type Target struct {
	BaseURL            string            `yaml:"base_url"`
	Headers            map[string]string `yaml:"headers,omitempty"`
	Timeout            Duration          `yaml:"timeout,omitempty"` // default 30s
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify,omitempty"`
}

// LoadModel selects how load is generated.
type LoadModel string

const (
	// ModelOpen: arrival-rate model. Stage.Target is iterations per second.
	ModelOpen LoadModel = "open"
	// ModelClosed: fixed virtual users looping. Stage.Target is VU count.
	ModelClosed LoadModel = "closed"
)

// Load describes the load profile. Stages run in order; the target ramps
// linearly from the previous stage's target (0 for the first) to this one.
type Load struct {
	Model  LoadModel `yaml:"model"` // default open
	Stages []Stage   `yaml:"stages"`
	// MaxVUs caps concurrent in-flight iterations in the open model.
	// Arrivals beyond the cap are dropped and counted. Default 1000.
	MaxVUs int `yaml:"max_vus,omitempty"`
}

// Stage is one segment of the load profile.
type Stage struct {
	Duration Duration `yaml:"duration"`
	Target   int      `yaml:"target"`
}

// DataMode controls how CSV rows are handed to iterations.
type DataMode string

const (
	DataSequential DataMode = "sequential" // round-robin over rows (default)
	DataRandom     DataMode = "random"
	DataUnique     DataMode = "unique" // each row used at most once; exhausted => stop
)

// Data configures CSV-backed test data. Columns become template variables.
type Data struct {
	File string   `yaml:"file"`
	Mode DataMode `yaml:"mode,omitempty"`
}

// StepType selects the protocol a step speaks.
type StepType string

const (
	// StepHTTP is a single HTTP request (the default when Type is empty).
	StepHTTP StepType = "http"
	// StepWS opens a WebSocket connection, exchanges Step.WS.Messages and
	// optionally holds the connection open (see WSStep).
	StepWS StepType = "ws"
)

// Step is one request in an iteration. Path, header values and Body may
// contain {{var}} placeholders (see Render).
//
// Type is optional and empty means http, so scenarios written before
// WebSocket support parse unchanged. HTTP and WebSocket steps may be mixed
// in one scenario; variables extracted by earlier steps are visible to
// later ones (e.g. log in over HTTP, then open a WebSocket with the token).
type Step struct {
	Name      string            `yaml:"name"`
	Type      StepType          `yaml:"type,omitempty"` // http (default) or ws
	Method    string            `yaml:"method"`         // default GET; must be empty for ws
	Path      string            `yaml:"path"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Body      string            `yaml:"body,omitempty"`
	ThinkTime Duration          `yaml:"think_time,omitempty"` // sleep after the step
	Extract   []Extract         `yaml:"extract,omitempty"`
	Assert    []Assert          `yaml:"assert,omitempty"` // http only
	// WS configures a ws step; required iff Type is ws.
	WS *WSStep `yaml:"ws,omitempty"`
}

// IsWS reports whether the step is a WebSocket step.
func (s *Step) IsWS() bool { return s.Type == StepWS }

// WSStep configures a WebSocket step (Step.Type == "ws").
//
// The connection URL is Step.Path resolved against Target.BaseURL with the
// scheme mapped http->ws and https->wss; an absolute ws:// or wss:// Path
// is used as is. Target and step headers are sent with the handshake.
//
// After the handshake, Messages are processed in order; then the
// connection is held open for Hold (sending PingMessage every PingEvery)
// and closed normally. Extract on a ws step reads (json or regex) from the
// last message that satisfied an Expect.
type WSStep struct {
	// Hold keeps the connection open this long after the message exchange.
	// 0 closes it right after the exchange. Holding time is not latency.
	Hold Duration `yaml:"hold,omitempty"`
	// Messages are sent/awaited in order once after connecting.
	Messages []WSMessage `yaml:"messages,omitempty"`
	// PingEvery, if > 0, sends PingMessage periodically while holding.
	PingEvery Duration `yaml:"ping_every,omitempty"`
	// PingMessage is the text message sent every PingEvery. Empty means a
	// WebSocket protocol ping control frame (which must be answered with a
	// pong within the target timeout).
	PingMessage string `yaml:"ping_message,omitempty"`
}

// WSMessage is one exchange: send a text message and/or wait for a
// received message containing Expect. Send, Expect and PingMessage may
// contain {{var}} placeholders.
type WSMessage struct {
	Send string `yaml:"send,omitempty"`
	// Expect, if set, waits for a received message (text or binary)
	// containing this substring; non-matching messages are skipped.
	Expect string `yaml:"expect,omitempty"`
	// Timeout bounds the wait for Expect. Default: Target.Timeout.
	Timeout Duration `yaml:"timeout,omitempty"`
}

// ExtractSource is where an extracted value comes from.
type ExtractSource string

const (
	FromJSON   ExtractSource = "json"   // Expr: dot path, e.g. data.items[0].id
	FromHeader ExtractSource = "header" // Expr: header name
	FromRegex  ExtractSource = "regex"  // Expr: regex on body, first capture group
)

// Extract stores a value from the response into a variable.
type Extract struct {
	Var  string        `yaml:"var"`
	From ExtractSource `yaml:"from"`
	Expr string        `yaml:"expr"`
}

// AssertType selects what an assertion checks.
type AssertType string

const (
	AssertStatus       AssertType = "status"        // Value: status code
	AssertJSON         AssertType = "json"          // Expr: dot path, compared to Value
	AssertDuration     AssertType = "duration"      // Value: duration, e.g. 200ms
	AssertBodyContains AssertType = "body_contains" // Value: substring
)

// Assert is a per-request check. A failed assert marks the request failed.
type Assert struct {
	Type  AssertType `yaml:"type"`
	Expr  string     `yaml:"expr,omitempty"`
	Op    string     `yaml:"op,omitempty"` // eq (default), ne, lt, lte, gt, gte, contains
	Value string     `yaml:"value"`
}

// Threshold is a run-level pass/fail rule.
//
// Metric is one of:
//
//	latency.p50 latency.p90 latency.p95 latency.p99 latency.mean latency.max  (Value in milliseconds)
//	error_rate  (Value as fraction, 0.01 = 1%)
//	rps         (Value in requests per second)
//
// Op is one of lt, lte, gt, gte.
type Threshold struct {
	Metric string  `yaml:"metric"`
	Op     string  `yaml:"op"`
	Value  float64 `yaml:"value"`
}

// Duration is a time.Duration that unmarshals from YAML strings like "30s".
// UnmarshalYAML / MarshalYAML are implemented in duration.go.
type Duration struct{ time.Duration }

// AuthType selects how iterations authenticate.
type AuthType string

const (
	// AuthLogin performs Auth.Login (an HTTP request) to obtain a token,
	// caches the extracted variables and reuses them across iterations.
	AuthLogin AuthType = "login"
	// AuthBearer sends a fixed token (may be a {{var}} from CSV data).
	AuthBearer AuthType = "bearer"
	// AuthBasic sends HTTP Basic credentials (may be {{var}}s).
	AuthBasic AuthType = "basic"
)

// AuthScope controls how login results are shared (AuthLogin only).
type AuthScope string

const (
	// AuthPerUser (default) caches one login per distinct credentials,
	// i.e. per rendered login request (typically one per CSV row).
	AuthPerUser AuthScope = "per_user"
	// AuthShared logs in once per load generator and shares the result
	// with every iteration (one service account).
	AuthShared AuthScope = "shared"
)

// Auth authenticates iterations without making login part of every
// iteration. Example:
//
//	auth:
//	  type: login
//	  scope: per_user          # or shared
//	  refresh: 10m             # re-login after this age (0 = never)
//	  login:                   # a normal HTTP step; needs >= 1 extract
//	    method: POST
//	    path: /api/login
//	    headers: {Content-Type: application/json}
//	    body: '{"username":"{{username}}","password":"{{password}}"}'
//	    extract:
//	      - {var: token, from: json, expr: data.access_token}
//	  header: Authorization    # default Authorization
//	  value: Bearer {{token}}  # default "Bearer {{token}}" (login/bearer)
//
//	auth: {type: bearer, token: "{{api_key}}"}
//	auth: {type: basic, username: "{{user}}", password: "{{pass}}"}
//
// Semantics:
//   - The header is added to every HTTP step and WebSocket handshake; a
//     step that sets the same header itself wins. Set inject: false to
//     only expose the extracted variables (use them in your own headers).
//   - Extracted login variables are available to every step as {{var}}.
//   - Login requests are reported as step "auth:login" (so failures and
//     latency are visible) and count in run totals; with caching they are
//     a small fraction of traffic.
//   - A failed login fails the iteration (reported as auth:login) and is
//     retried on the next iteration for the same credentials.
//   - When a step answers 401, the cached login for those credentials is
//     dropped and the next iteration logs in again.
//   - Caches are per load generator (each distributed worker logs in on
//     its own).
type Auth struct {
	Type    AuthType  `yaml:"type"`
	Scope   AuthScope `yaml:"scope,omitempty"`   // login only; default per_user
	Refresh Duration  `yaml:"refresh,omitempty"` // login only; 0 = never
	Login   *Step     `yaml:"login,omitempty"`   // login only; HTTP step
	Token   string    `yaml:"token,omitempty"`   // bearer only
	// Username and Password are used by basic auth.
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	// Header and Value define the injected header. Defaults: Authorization
	// and "Bearer {{token}}" (login), "Bearer <token>" (bearer),
	// "Basic base64(user:pass)" (basic; Value must be empty).
	Header string `yaml:"header,omitempty"`
	Value  string `yaml:"value,omitempty"`
	// Inject: nil/true adds the header to every step; false disables it.
	Inject *bool `yaml:"inject,omitempty"`
}
