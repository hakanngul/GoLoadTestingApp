package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// Token limits and timings.
const (
	tokenSecretPrefix = "glt_"
	tokenPrefixLen    = 8 // characters of the secret kept as APIToken.Prefix
	maxTokenNameLen   = 60
	maxTokenDays      = 365

	// tokenCacheTTL bounds how long a token looked up in the store is
	// trusted without asking again (revocation through this server is
	// immediate: DELETE /api/tokens/{id} evicts the entry).
	tokenCacheTTL = 30 * time.Second
	// tokenTouchEvery limits last_used_at writes to one per token and
	// minute.
	tokenTouchEvery = time.Minute
)

// errTokensDisabled answers /api/tokens when no master token is set.
var errTokensDisabled = &httpError{status: http.StatusConflict, msg: "API tokens need serve -token (auth is disabled)"}

// routeScope returns the scope a route needs. pattern is a ServeMux
// pattern ("METHOD /path"). It is deny-by-default in spirit: everything
// that is not explicitly read or run needs write, and /api/tokens needs
// admin. TestRouteScopes pins the result for every registered route.
func routeScope(pattern string) model.TokenScope {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return model.ScopeAdmin
	}
	if path == "/api/tokens" || strings.HasPrefix(path, "/api/tokens/") {
		return model.ScopeAdmin
	}
	if method == http.MethodGet || method == http.MethodHead {
		return model.ScopeRead
	}
	switch pattern {
	case "POST /api/validate", "POST /api/schedules/preview":
		return model.ScopeRead
	case "POST /api/tests/try",
		"POST /api/tests/{id}/runs",
		"POST /api/runs/{id}/stop",
		"POST /api/suites/{id}/runs",
		"POST /api/suite-runs/{id}/stop",
		"POST /api/schedules/{id}/run-now":
		return model.ScopeRun
	}
	return model.ScopeWrite
}

// hashToken returns the hex sha256 of a token secret (what the store keeps).
func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// newTokenSecret returns a fresh secret: "glt_" + 32 random bytes in
// unpadded base64url.
func newTokenSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return tokenSecretPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// tokenAuth resolves bearer secrets to API tokens, with a short cache.
type tokenAuth struct {
	st store.TokenStore

	mu    sync.Mutex
	cache map[string]*cachedToken // by hash
}

type cachedToken struct {
	tok       model.APIToken
	fetched   time.Time
	lastTouch time.Time
}

func newTokenAuth(st store.TokenStore) *tokenAuth {
	return &tokenAuth{st: st, cache: make(map[string]*cachedToken)}
}

// lookup returns the live token for secret. ok is false for unknown or
// expired tokens; err for store failures.
func (a *tokenAuth) lookup(ctx context.Context, secret string, now time.Time) (tok model.APIToken, ok bool, err error) {
	hash := hashToken(secret)
	a.mu.Lock()
	c := a.cache[hash]
	if c != nil && now.Sub(c.fetched) >= tokenCacheTTL {
		delete(a.cache, hash)
		c = nil
	}
	a.mu.Unlock()

	if c == nil {
		t, err := a.st.TokenByHash(ctx, hash)
		if errors.Is(err, store.ErrNotFound) {
			return model.APIToken{}, false, nil
		}
		if err != nil {
			return model.APIToken{}, false, err
		}
		c = &cachedToken{tok: t, fetched: now, lastTouch: derefTime(t.LastUsedAt)}
		a.mu.Lock()
		if prev := a.cache[hash]; prev != nil {
			c = prev
		} else {
			a.cache[hash] = c
		}
		a.mu.Unlock()
	}
	if c.tok.ExpiresAt != nil && !now.Before(*c.tok.ExpiresAt) {
		return model.APIToken{}, false, nil
	}

	a.mu.Lock()
	touch := now.Sub(c.lastTouch) >= tokenTouchEvery
	if touch {
		c.lastTouch = now
	}
	a.mu.Unlock()
	if touch {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		// Best effort: a failed touch only leaves last_used_at stale.
		_ = a.st.TouchToken(tctx, c.tok.ID, now)
		cancel()
	}
	return c.tok, true, nil
}

// forget evicts every cached entry of token id (after DELETE).
func (a *tokenAuth) forget(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for h, c := range a.cache {
		if c.tok.ID == id {
			delete(a.cache, h)
		}
	}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// tokenStore returns the token store behind /api/tokens, or the error to
// answer with.
func (s *server) tokenStore() (store.TokenStore, error) {
	if s.authToken == "" {
		return nil, errTokensDisabled
	}
	if s.tokens == nil {
		return nil, &httpError{status: http.StatusServiceUnavailable, msg: "API tokens are not supported by this store"}
	}
	return s.tokens.st, nil
}

func (s *server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.tokenStore()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	list, err := ts.ListTokens(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

func (s *server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	ts, err := s.tokenStore()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req model.CreateTokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	var problems []string
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > maxTokenNameLen {
		problems = append(problems, fmt.Sprintf("name: 1 to %d characters", maxTokenNameLen))
	}
	if !req.Scope.Valid() {
		problems = append(problems, `scope: one of "read", "run", "write", "admin"`)
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > maxTokenDays {
		problems = append(problems, fmt.Sprintf("expires_in_days: 0 (never) to %d", maxTokenDays))
	}
	if len(problems) > 0 {
		s.writeErr(w, r, &httpError{status: http.StatusBadRequest, msg: "invalid token", details: problems})
		return
	}
	secret, err := newTokenSecret()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	now := time.Now().UTC()
	tok := model.APIToken{Name: req.Name, Scope: req.Scope, Prefix: secret[:tokenPrefixLen], CreatedAt: now}
	if req.ExpiresInDays > 0 {
		exp := now.AddDate(0, 0, req.ExpiresInDays)
		tok.ExpiresAt = &exp
	}
	created, err := ts.CreateToken(r.Context(), tok, hashToken(secret))
	if errors.Is(err, store.ErrConflict) {
		s.writeErr(w, r, &httpError{status: http.StatusConflict, msg: "token name already exists"})
		return
	}
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, model.CreateTokenResponse{Token: created, Secret: secret})
}

func (s *server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	ts, err := s.tokenStore()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	id := r.PathValue("id")
	if err := ts.DeleteToken(r.Context(), id); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.tokens.forget(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe answers GET /api/me with the caller's identity.
func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, ok := model.IdentityFrom(r.Context())
	if !ok {
		id = model.Identity{Kind: "none", Scope: model.ScopeAdmin}
	}
	writeJSON(w, http.StatusOK, id)
}
