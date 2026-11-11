package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scheduler"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
	"github.com/hakanngul/GoLoadTestingApp/internal/suite"
)

// wantScopes pins the scope of every route that is not a plain GET. A new
// POST/PUT/PATCH/DELETE route fails TestRouteScopes until it is added here,
// so it cannot silently get a default scope.
var wantScopes = map[string]model.TokenScope{
	"POST /api/validate":          model.ScopeRead,
	"POST /api/schedules/preview": model.ScopeRead,

	"POST /api/tests/try":              model.ScopeRun,
	"POST /api/tests/{id}/runs":        model.ScopeRun,
	"POST /api/runs/{id}/stop":         model.ScopeRun,
	"POST /api/suites/{id}/runs":       model.ScopeRun,
	"POST /api/suite-runs/{id}/stop":   model.ScopeRun,
	"POST /api/schedules/{id}/run-now": model.ScopeRun,

	"POST /api/tests":                     model.ScopeWrite,
	"PUT /api/tests/{id}":                 model.ScopeWrite,
	"DELETE /api/tests/{id}":              model.ScopeWrite,
	"PUT /api/tests/{id}/files/{name}":    model.ScopeWrite,
	"DELETE /api/tests/{id}/files/{name}": model.ScopeWrite,
	"PATCH /api/runs/{id}":                model.ScopeWrite,
	"DELETE /api/runs/{id}":               model.ScopeWrite,
	"PUT /api/tests/{id}/baseline":        model.ScopeWrite,
	"POST /api/schedules":                 model.ScopeWrite,
	"PUT /api/schedules/{id}":             model.ScopeWrite,
	"DELETE /api/schedules/{id}":          model.ScopeWrite,
	"POST /api/suites":                    model.ScopeWrite,
	"PUT /api/suites/{id}":                model.ScopeWrite,
	"DELETE /api/suites/{id}":             model.ScopeWrite,
	"POST /api/notifications":             model.ScopeWrite,
	"PUT /api/notifications/{id}":         model.ScopeWrite,
	"DELETE /api/notifications/{id}":      model.ScopeWrite,
	"POST /api/notifications/{id}/test":   model.ScopeWrite,
	"POST /api/tokens":                    model.ScopeAdmin,
	"DELETE /api/tokens/{id}":             model.ScopeAdmin,
}

func TestRouteScopes(t *testing.T) {
	s := newEnv(t).srv
	if len(s.patterns) < 40 {
		t.Fatalf("only %d routes registered", len(s.patterns))
	}
	seen := map[string]bool{}
	for _, p := range s.patterns {
		seen[p] = true
		got, ok := s.scopes[p]
		if !ok {
			t.Errorf("%s has no scope", p)
			continue
		}
		method, path, _ := strings.Cut(p, " ")
		want, listed := wantScopes[p]
		switch {
		case path == "/api/tokens" && method == "GET":
			want = model.ScopeAdmin
		case method == "GET" || method == "HEAD":
			if listed {
				t.Errorf("%s: GET routes are read-scoped; remove it from wantScopes", p)
			}
			want = model.ScopeRead
		case !listed:
			t.Errorf("%s is not classified: add it to wantScopes (read, run, write or admin)", p)
			continue
		}
		if got != want {
			t.Errorf("%s scope = %q, want %q", p, got, want)
		}
	}
	var stale []string
	for p := range wantScopes {
		if !seen[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("wantScopes lists routes that are not registered: %v", stale)
	}
	if routeScope("TRACE /api/x") != model.ScopeWrite || routeScope("garbage") != model.ScopeAdmin {
		t.Errorf("fallbacks: TRACE -> %q, garbage -> %q", routeScope("TRACE /api/x"), routeScope("garbage"))
	}
}

type tokEnv struct {
	*env
	st  *store.Memory
	sch *scheduler.Scheduler
}

const master = "m4ster"

func newTokEnv(t *testing.T, mutate ...func(*Config)) *tokEnv {
	t.Helper()
	st := store.NewMemory()
	rn := newFakeRunner(st)
	svc := suite.New(suite.Options{Store: st, Runner: rn, Poll: 5 * time.Millisecond})
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	sch := scheduler.New(scheduler.Options{Store: st, Runner: rn, Suites: svc})
	cfg := Config{Store: st, Runner: rn, Version: "test", AuthToken: master, Suites: svc, Scheduler: sch}
	for _, m := range mutate {
		m(&cfg)
	}
	s := newServer(cfg)
	return &tokEnv{env: &env{h: s.handler(), srv: s, runner: rn}, st: st, sch: sch}
}

func (e *tokEnv) as(t *testing.T, secret, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	hdr := []string{"Content-Type", "application/json"}
	if secret != "" {
		hdr = append(hdr, "Authorization", "Bearer "+secret)
	}
	var rdr *strings.Reader
	switch b := body.(type) {
	case nil:
		rdr = strings.NewReader("")
	case string:
		rdr = strings.NewReader(b)
	default:
		rdr = strings.NewReader(mustJSON(t, b))
	}
	return e.do(t, method, path, rdr, hdr...)
}

func (e *tokEnv) mint(t *testing.T, name string, scope model.TokenScope, days int) model.CreateTokenResponse {
	t.Helper()
	rec := e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: name, Scope: scope, ExpiresInDays: days})
	wantStatus(t, rec, http.StatusCreated)
	return decode[model.CreateTokenResponse](t, rec)
}

func TestTokenLifecycle(t *testing.T) {
	e := newTokEnv(t)
	res := e.mint(t, "kiro-hakan", model.ScopeRun, 30)
	if !strings.HasPrefix(res.Secret, "glt_") || len(res.Secret) != 4+43 {
		t.Fatalf("secret = %q", res.Secret)
	}
	if res.Token.Prefix != res.Secret[:8] || res.Token.Name != "kiro-hakan" || res.Token.Scope != model.ScopeRun ||
		res.Token.ExpiresAt == nil || res.Token.ID == "" {
		t.Fatalf("token = %+v", res.Token)
	}
	// Only the hash is stored.
	if _, err := e.st.TokenByHash(context.Background(), res.Secret); err == nil {
		t.Fatal("the secret itself is stored")
	}
	if _, err := e.st.TokenByHash(context.Background(), hashToken(res.Secret)); err != nil {
		t.Fatalf("hash not stored: %v", err)
	}

	// Listing never shows secrets; newest first.
	e.mint(t, "second", model.ScopeRead, 0)
	rec := e.as(t, master, "GET", "/api/tokens", nil)
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), res.Secret) || strings.Contains(rec.Body.String(), "hash") {
		t.Fatalf("list leaks: %s", rec.Body.String())
	}
	list := decode[[]model.APIToken](t, rec)
	if len(list) != 2 || list[0].Name != "second" {
		t.Fatalf("list = %+v", list)
	}

	// Validation and duplicates.
	wantAPIError(t, e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: "ci", Scope: "root"}), 400, "invalid token")
	wantAPIError(t, e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: "", Scope: model.ScopeRead}), 400, "invalid token")
	wantAPIError(t, e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: strings.Repeat("x", 61), Scope: model.ScopeRead}), 400, "invalid token")
	wantAPIError(t, e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: "ok", Scope: model.ScopeRead, ExpiresInDays: 366}), 400, "invalid token")
	wantAPIError(t, e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: "ok", Scope: model.ScopeRead, ExpiresInDays: -1}), 400, "invalid token")
	wantAPIError(t, e.as(t, master, "POST", "/api/tokens", model.CreateTokenRequest{Name: "kiro-hakan", Scope: model.ScopeRead}), 409, "already exists")

	// The token works, and /api/me reports it.
	rec = e.as(t, res.Secret, "GET", "/api/me", nil)
	wantStatus(t, rec, 200)
	if me := decode[model.Identity](t, rec); me.Kind != "token" || me.Name != "kiro-hakan" || me.Scope != model.ScopeRun {
		t.Fatalf("me = %+v", me)
	}
	rec = e.as(t, master, "GET", "/api/me", nil)
	if me := decode[model.Identity](t, rec); me.Kind != "master" || me.Scope != model.ScopeAdmin {
		t.Fatalf("master me = %+v", me)
	}

	// Revoke: takes effect at once, even though the token was cached.
	wantStatus(t, e.as(t, master, "DELETE", "/api/tokens/"+res.Token.ID, nil), 204)
	wantAPIError(t, e.as(t, res.Secret, "GET", "/api/me", nil), 401, "unauthorized")
	wantAPIError(t, e.as(t, master, "DELETE", "/api/tokens/"+res.Token.ID, nil), 404, "not found")
}

func TestTokenScopeEnforcement(t *testing.T) {
	e := newTokEnv(t)
	read := e.mint(t, "r", model.ScopeRead, 0).Secret
	run := e.mint(t, "run", model.ScopeRun, 0).Secret
	write := e.mint(t, "w", model.ScopeWrite, 0).Secret
	admin := e.mint(t, "a", model.ScopeAdmin, 0).Secret

	cases := []struct {
		secret         string
		method, path   string
		wantOK         bool
		needs, hasText string
	}{
		{read, "GET", "/api/tests", true, "", ""},
		{read, "POST", "/api/validate", true, "", ""}, // 400 body is fine: it passed auth
		{read, "POST", "/api/tests", false, "write", "read"},
		{read, "POST", "/api/tests/try", false, "run", "read"},
		{read, "GET", "/api/tokens", false, "admin", "read"},
		{run, "POST", "/api/tests", false, "write", "run"},
		{run, "DELETE", "/api/runs/x", false, "write", "run"},
		{run, "POST", "/api/runs/x/stop", true, "", ""},
		{write, "POST", "/api/tests", true, "", ""},
		{write, "GET", "/api/tokens", false, "admin", "write"},
		{write, "POST", "/api/tokens", false, "admin", "write"},
		{admin, "GET", "/api/tokens", true, "", ""},
	}
	for _, c := range cases {
		rec := e.as(t, c.secret, c.method, c.path, "{}")
		if c.wantOK {
			if rec.Code == 401 || rec.Code == 403 {
				t.Errorf("%s %s with %s token = %d: %s", c.method, c.path, c.hasText, rec.Code, rec.Body.String())
			}
			continue
		}
		want := `this token has scope "` + c.hasText + `"; "` + c.needs + `" is needed`
		if rec.Code != 403 || decode[model.APIError](t, rec).Error != want {
			t.Errorf("%s %s = %d %s; want 403 %q", c.method, c.path, rec.Code, rec.Body.String(), want)
		}
	}

	// Unknown routes still 404 for an authenticated caller, 401 otherwise.
	wantStatus(t, e.as(t, read, "GET", "/api/nope", nil), 404)
	wantStatus(t, e.as(t, "", "GET", "/api/nope", nil), 401)
	// A secret-shaped but unknown token and the wrong prefix are 401.
	wantStatus(t, e.as(t, "glt_unknown", "GET", "/api/tests", nil), 401)
	wantStatus(t, e.as(t, "xyz", "GET", "/api/tests", nil), 401)
}

func TestTokenExpiryAndLastUsed(t *testing.T) {
	e := newTokEnv(t)
	res := e.mint(t, "short", model.ScopeRead, 1)
	wantStatus(t, e.as(t, res.Secret, "GET", "/api/tests", nil), 200)

	// last_used_at is written on first use.
	tok, err := e.st.TokenByHash(context.Background(), hashToken(res.Secret))
	if err != nil || tok.LastUsedAt == nil {
		t.Fatalf("last_used_at not set: %+v, %v", tok, err)
	}
	first := *tok.LastUsedAt
	// ... but at most once a minute.
	wantStatus(t, e.as(t, res.Secret, "GET", "/api/tests", nil), 200)
	tok, _ = e.st.TokenByHash(context.Background(), hashToken(res.Secret))
	if !tok.LastUsedAt.Equal(first) {
		t.Fatalf("last_used_at rewritten within a minute: %v -> %v", first, tok.LastUsedAt)
	}

	// Expiry: a token whose expiry passed is a 401 (a cached copy too).
	past := time.Now().Add(-time.Minute)
	old, err := e.st.CreateToken(context.Background(), model.APIToken{Name: "old", Scope: model.ScopeAdmin,
		Prefix: "glt_oldx", ExpiresAt: &past}, hashToken("glt_old-secret"))
	if err != nil {
		t.Fatal(err)
	}
	_ = old
	wantAPIError(t, e.as(t, "glt_old-secret", "GET", "/api/tests", nil), 401, "unauthorized")

	a := newTokenAuth(e.st)
	soon := time.Now().Add(time.Hour)
	if _, err := e.st.CreateToken(context.Background(), model.APIToken{Name: "soon", Scope: model.ScopeRead,
		Prefix: "glt_soon", ExpiresAt: &soon}, hashToken("glt_soon-secret")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.lookup(context.Background(), "glt_soon-secret", time.Now()); !ok || err != nil {
		t.Fatalf("lookup live = %v, %v", ok, err)
	}
	if _, ok, _ := a.lookup(context.Background(), "glt_soon-secret", time.Now().Add(2*time.Hour)); ok {
		t.Fatal("expired token accepted from cache")
	}
}

func TestTokenSSEAccessToken(t *testing.T) {
	e := newTokEnv(t)
	read := e.mint(t, "viewer", model.ScopeRead, 0).Secret
	run, _ := e.st.CreateRun(context.Background(), model.Run{Status: model.RunPassed, TestID: mustTestID(t, e.st)})
	wantStatus(t, e.do(t, "GET", "/api/runs/"+run.ID+"/stream?access_token="+read, nil), 200)
	wantStatus(t, e.do(t, "GET", "/api/runs/"+run.ID+"/stream?access_token=glt_bad", nil), 401)
	// access_token is only for the stream route.
	wantStatus(t, e.do(t, "GET", "/api/tests?access_token="+read, nil), 401)
}

func mustTestID(t *testing.T, st *store.Memory) string {
	t.Helper()
	tst, err := st.CreateTest(context.Background(), model.Test{Name: "t", YAML: closedSchedYAML})
	if err != nil {
		t.Fatal(err)
	}
	return tst.ID
}

func TestTokensDisabledWithoutAuth(t *testing.T) {
	e := newTokEnv(t, func(c *Config) { c.AuthToken = "" })
	for _, r := range [][2]string{{"GET", "/api/tokens"}, {"POST", "/api/tokens"}, {"DELETE", "/api/tokens/x"}} {
		wantAPIError(t, e.as(t, "", r[0], r[1], `{"name":"a","scope":"read"}`), 409, "API tokens need serve -token (auth is disabled)")
	}
	rec := e.as(t, "", "GET", "/api/me", nil)
	wantStatus(t, rec, 200)
	if me := decode[model.Identity](t, rec); me.Kind != "none" || me.Scope != model.ScopeAdmin {
		t.Fatalf("me = %+v", me)
	}
}

func TestStartedByAudit(t *testing.T) {
	e := newTokEnv(t)
	ctx := context.Background()
	tst, _ := e.st.CreateTest(ctx, model.Test{Name: "shop", YAML: closedSchedYAML})
	runTok := e.mint(t, "kiro-hakan", model.ScopeRun, 0).Secret

	// started_by cannot be spoofed through the body.
	wantStatus(t, e.as(t, runTok, "POST", "/api/tests/"+tst.ID+"/runs", `{"started_by":"admin"}`), 400)
	rec := e.as(t, runTok, "POST", "/api/tests/"+tst.ID+"/runs", nil)
	wantStatus(t, rec, 201)
	if r := decode[model.Run](t, rec); r.StartedBy != "token:kiro-hakan" {
		t.Fatalf("token run started_by = %q", r.StartedBy)
	}
	rec = e.as(t, master, "POST", "/api/tests/"+tst.ID+"/runs", nil)
	wantStatus(t, rec, 201)
	got := decode[model.Run](t, rec)
	if got.StartedBy != "admin" {
		t.Fatalf("master run started_by = %q", got.StartedBy)
	}
	if stored, _ := e.st.GetRun(ctx, got.ID); stored.StartedBy != "admin" {
		t.Fatalf("stored = %+v", stored)
	}

	// Suite run.
	su, err := e.st.CreateSuite(ctx, model.Suite{Name: "smoke", Mode: model.SuiteSequential, Items: []model.SuiteItem{{TestID: tst.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	rec = e.as(t, runTok, "POST", "/api/suites/"+su.ID+"/runs", nil)
	wantStatus(t, rec, 201)
	if sr := decode[model.SuiteRun](t, rec); sr.StartedBy != "token:kiro-hakan" {
		t.Fatalf("suite run started_by = %q", sr.StartedBy)
	}

	// Schedule run-now: the caller, not "schedule:...".
	sc, err := e.st.CreateSchedule(ctx, model.Schedule{ScheduleSpec: model.ScheduleSpec{TestID: tst.ID, Name: "nightly",
		Kind: model.ScheduleRecurring, Cron: "@daily", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	rec = e.as(t, runTok, "POST", "/api/schedules/"+sc.ID+"/run-now", nil)
	wantStatus(t, rec, 201)
	if r := decode[model.Run](t, rec); r.StartedBy != "token:kiro-hakan" || r.ScheduleID != sc.ID {
		t.Fatalf("run-now run = %+v", r)
	}

	// Auth disabled: unknown / empty.
	open := newTokEnv(t, func(c *Config) { c.AuthToken = "" })
	t2, _ := open.st.CreateTest(ctx, model.Test{Name: "shop", YAML: closedSchedYAML})
	rec = open.as(t, "", "POST", "/api/tests/"+t2.ID+"/runs", nil)
	wantStatus(t, rec, 201)
	if r := decode[model.Run](t, rec); r.StartedBy != "" {
		t.Fatalf("no-auth started_by = %q", r.StartedBy)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
