package executor

// Scenario-level authentication (scenario.Auth).
//
// The auth layer wraps whatever executor newPlain builds:
//
//   - Header injection is done once, at New time, by adding the auth
//     header to a copy of every step's headers (HTTP steps and ws
//     handshakes alike) unless the step defines that header itself
//     (case-insensitive). Step headers override target headers, so the
//     auth header also overrides a target-level header of the same name.
//     A constant value (no {{ }}, e.g. a literal bearer token or basic
//     credentials) is baked in and costs nothing per request. A templated
//     value is rendered once per iteration into the reserved variable
//     authHeaderVar and the steps carry "{{_auth_header}}", so each request
//     only does the variable lookup it would do for any other template.
//
//   - Login (type login) runs Auth.Login through a dedicated one-step
//     httpx executor, so extract/assert/timeouts behave exactly like a
//     step. Results are cached per key: "" for scope shared, otherwise a
//     hash of the rendered login request (method, path, body, headers),
//     i.e. one entry per distinct credentials. Concurrent iterations that
//     miss the same key wait for a single login (singleflight).
//
// Reporting and coordinated omission: the iteration that performs a login
// reports it as step "auth:login", measured from `intended` (it is the
// first thing the iteration does); its first real step is then measured
// from its own start. Cache hits report nothing and the first real step
// keeps the `intended` start. Iterations that waited for another
// iteration's login also keep `intended`, so their wait is included in
// the first step's latency. If the awaited login failed, each waiting
// iteration reports its own failed "auth:login" result and ends.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// authHeaderVar is the reserved variable holding the per-iteration auth
// header value when it depends on variables.
const authHeaderVar = "_auth_header"

type authExec struct {
	inner      protocol.Executor
	closeInner func()

	// Per-iteration header value (only when it depends on vars).
	headerTmpl string // login/bearer template; "" if none needed
	basicUser  string // basic templates (basicTmpl only)
	basicPass  string
	basicTmpl  bool

	// Login (type login only).
	login    *httpx.Executor
	shared   bool
	refresh  time.Duration
	keyParts []string // templates whose rendering identifies the user
	keyConst bool     // no key part is templated: one entry for everybody

	mu       sync.Mutex
	cache    map[string]*authEntry
	inflight map[string]*authCall
}

// authEntry is a successful login; vars is never modified after creation.
type authEntry struct {
	vars     map[string]string
	obtained time.Time
}

// authCall is an in-flight login. Its fields are written before done is
// closed and read only after.
type authCall struct {
	done      chan struct{}
	entry     *authEntry
	res       protocol.Result // failed login result
	cancelled bool            // leader's ctx was cancelled; waiters retry
}

func newAuth(sc *scenario.Scenario, opts Options) (protocol.Executor, func(), error) {
	a := sc.Auth
	if a.Type == scenario.AuthLogin && a.Login == nil {
		return nil, nil, errors.New("executor: auth type login needs auth.login")
	}
	header := a.Header
	if header == "" {
		header = scenario.DefaultAuthHeader
	}

	ae := &authExec{}
	stepValue := "" // value injected into step headers
	if a.InjectEnabled() {
		switch a.Type {
		case scenario.AuthBasic:
			if strings.Contains(a.Username, "{{") || strings.Contains(a.Password, "{{") {
				ae.basicTmpl, ae.basicUser, ae.basicPass = true, a.Username, a.Password
				stepValue = "{{" + authHeaderVar + "}}"
			} else {
				stepValue = basicValue(a.Username, a.Password)
			}
		case scenario.AuthLogin, scenario.AuthBearer:
			v := a.ValueTemplate()
			if strings.Contains(v, "{{") {
				ae.headerTmpl = v
				stepValue = "{{" + authHeaderVar + "}}"
			} else {
				stepValue = v
			}
		default:
			return nil, nil, fmt.Errorf("executor: unsupported auth type %q", a.Type)
		}
	}

	isc := *sc
	isc.Auth = nil
	if stepValue != "" {
		isc.Steps = make([]scenario.Step, len(sc.Steps))
		for i, st := range sc.Steps {
			if !hasHeader(st.Headers, header) {
				h := make(map[string]string, len(st.Headers)+1)
				maps.Copy(h, st.Headers)
				h[header] = stepValue
				st.Headers = h
			}
			isc.Steps[i] = st
		}
	}
	inner, closeInner, err := newPlain(&isc, opts)
	if err != nil {
		return nil, nil, err
	}
	ae.inner, ae.closeInner = inner, closeInner

	if a.Type == scenario.AuthLogin {
		ls := *a.Login
		ls.Name = scenario.AuthLoginStep
		ls.ThinkTime = scenario.Duration{}
		if ls.Method == "" {
			ls.Method = scenario.DefaultLoginMethod
		}
		lsc := scenario.Scenario{Name: sc.Name, Target: sc.Target, Steps: []scenario.Step{ls}}
		if ae.login, err = httpx.New(&lsc, opts.HTTP); err != nil {
			closeInner()
			return nil, nil, fmt.Errorf("executor: auth.login: %w", err)
		}
		ae.shared = a.Scope == scenario.AuthShared
		ae.refresh = a.Refresh.Duration
		ae.cache = make(map[string]*authEntry)
		ae.inflight = make(map[string]*authCall)
		if !ae.shared {
			ae.keyParts = loginKeyParts(&ls, sc.Target.Headers)
			ae.keyConst = true
			for _, p := range ae.keyParts {
				if strings.Contains(p, "{{") {
					ae.keyConst = false
					break
				}
			}
		}
	}
	return ae, ae.close, nil
}

func (ae *authExec) close() {
	ae.closeInner()
	if ae.login != nil {
		ae.login.Close()
	}
}

// hasHeader reports whether h defines name, case-insensitively.
func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(strings.TrimSpace(k), name) {
			return true
		}
	}
	return false
}

func basicValue(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// loginKeyParts lists the parts of the login request that identify the
// user: method, path, body and the merged headers in a stable order.
func loginKeyParts(ls *scenario.Step, target map[string]string) []string {
	merged := make(map[string]string, len(target)+len(ls.Headers))
	for k, v := range target {
		merged[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range ls.Headers {
		merged[http.CanonicalHeaderKey(k)] = v
	}
	parts := []string{ls.Method, ls.Path, ls.Body}
	for _, k := range slices.Sorted(maps.Keys(merged)) {
		parts = append(parts, k+": "+merged[k])
	}
	return parts
}

// key returns the cache key for an iteration with vars.
func (ae *authExec) key(vars map[string]string) string {
	if ae.shared || ae.keyConst {
		return ""
	}
	h := sha256.New()
	for _, p := range ae.keyParts {
		h.Write([]byte(scenario.Render(p, vars)))
		h.Write([]byte{0})
	}
	return string(h.Sum(nil))
}

// Iterate implements protocol.Executor.
func (ae *authExec) Iterate(ctx context.Context, vars map[string]string, intended time.Time, sink func(protocol.Result)) {
	if ae.login == nil && ae.headerTmpl == "" && !ae.basicTmpl {
		ae.inner.Iterate(ctx, vars, intended, sink) // constant header: nothing to do
		return
	}
	if vars == nil {
		vars = make(map[string]string, 4)
	}
	if ae.login == nil {
		ae.setHeaderVar(vars)
		ae.inner.Iterate(ctx, vars, intended, sink)
		return
	}

	start := time.Now()
	if !intended.IsZero() && intended.Before(start) {
		start = intended
	}
	key := ae.key(vars)
	ent, loggedIn, ok := ae.acquire(ctx, key, vars, start, sink)
	if !ok {
		return
	}
	if loggedIn {
		// The login was this iteration's first measured request; the
		// first real step measures from its own start.
		intended = time.Time{}
	}
	for k, v := range ent.vars {
		vars[k] = v
	}
	ae.setHeaderVar(vars)
	ae.inner.Iterate(ctx, vars, intended, func(r protocol.Result) {
		if r.Status == http.StatusUnauthorized {
			ae.invalidate(key, ent)
		}
		sink(r)
	})
}

// setHeaderVar renders the per-iteration header value, if one is needed.
func (ae *authExec) setHeaderVar(vars map[string]string) {
	switch {
	case ae.headerTmpl != "":
		vars[authHeaderVar] = scenario.Render(ae.headerTmpl, vars)
	case ae.basicTmpl:
		vars[authHeaderVar] = basicValue(scenario.Render(ae.basicUser, vars), scenario.Render(ae.basicPass, vars))
	}
}

// acquire returns a fresh login entry for key, logging in if needed.
// loggedIn reports whether this call performed (and reported) the login.
// ok is false if the iteration must end (login failed or ctx cancelled).
func (ae *authExec) acquire(ctx context.Context, key string, vars map[string]string, start time.Time, sink func(protocol.Result)) (ent *authEntry, loggedIn, ok bool) {
	for {
		ae.mu.Lock()
		if e := ae.cache[key]; e != nil && (ae.refresh <= 0 || time.Since(e.obtained) < ae.refresh) {
			ae.mu.Unlock()
			return e, false, true
		}
		if c := ae.inflight[key]; c != nil {
			ae.mu.Unlock()
			select {
			case <-c.done:
			case <-ctx.Done():
				return nil, false, false
			}
			switch {
			case c.entry != nil:
				return c.entry, false, true
			case c.cancelled:
				if ctx.Err() != nil {
					return nil, false, false
				}
				continue // the leader gave up; try again ourselves
			}
			sink(protocol.Result{
				Step:    scenario.AuthLoginStep,
				Status:  c.res.Status,
				Latency: time.Since(start),
				Err:     fmt.Errorf("shared login attempt failed: %w", c.res.Err),
			})
			return nil, false, false
		}
		c := &authCall{done: make(chan struct{})}
		ae.inflight[key] = c
		ae.mu.Unlock()

		res, extracted := ae.login.Step(ctx, 0, vars, start)
		res.Step = scenario.AuthLoginStep
		cancelled := ctx.Err() != nil

		ae.mu.Lock()
		delete(ae.inflight, key)
		switch {
		case cancelled:
			c.cancelled = true
		case res.Err == nil:
			e := &authEntry{vars: make(map[string]string, len(extracted)/2), obtained: time.Now()}
			for j := 0; j+1 < len(extracted); j += 2 {
				e.vars[extracted[j]] = extracted[j+1]
			}
			ae.cache[key] = e
			c.entry = e
		default:
			c.res = res
		}
		ae.mu.Unlock()
		close(c.done)

		if cancelled {
			return nil, false, false // not a failure of the target
		}
		sink(res)
		if res.Err != nil {
			return nil, false, false
		}
		return c.entry, true, true
	}
}

// invalidate drops the cached login for key if it is still ent (a newer
// login by another iteration is kept).
func (ae *authExec) invalidate(key string, ent *authEntry) {
	ae.mu.Lock()
	if ae.cache[key] == ent {
		delete(ae.cache, key)
	}
	ae.mu.Unlock()
}
