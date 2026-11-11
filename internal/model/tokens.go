package model

import (
	"context"
	"time"
)

// TokenScope is the permission level of an API token. Scopes are ordered:
// each includes the ones before it.
type TokenScope string

const (
	// ScopeRead: GET/HEAD everything, POST /api/validate and
	// POST /api/schedules/preview.
	ScopeRead TokenScope = "read"
	// ScopeRun: read + starting and stopping runs (tests/try, test runs,
	// suite runs, schedule run-now).
	ScopeRun TokenScope = "run"
	// ScopeWrite: run + every other mutation except /api/tokens.
	ScopeWrite TokenScope = "write"
	// ScopeAdmin: everything, including /api/tokens.
	ScopeAdmin TokenScope = "admin"
)

// Rank orders scopes (read < run < write < admin); unknown scopes rank 0.
func (s TokenScope) Rank() int {
	switch s {
	case ScopeRead:
		return 1
	case ScopeRun:
		return 2
	case ScopeWrite:
		return 3
	case ScopeAdmin:
		return 4
	}
	return 0
}

// Valid reports whether s is one of the four scopes.
func (s TokenScope) Valid() bool { return s.Rank() > 0 }

// Includes reports whether s grants everything need grants.
func (s TokenScope) Includes(need TokenScope) bool { return need.Valid() && s.Rank() >= need.Rank() }

// APIToken describes a scoped API token. The secret itself is never
// stored or returned again after creation.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"` // unique, 1..60 chars
	Scope      TokenScope `json:"scope"`
	Prefix     string     `json:"prefix"` // first 8 chars of the secret
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// CreateTokenRequest is the body of POST /api/tokens.
type CreateTokenRequest struct {
	Name          string     `json:"name"`
	Scope         TokenScope `json:"scope"`
	ExpiresInDays int        `json:"expires_in_days,omitempty"` // 0 = never, max 365
}

// CreateTokenResponse is the answer of POST /api/tokens. Secret is shown
// once.
type CreateTokenResponse struct {
	Token  APIToken `json:"token"`
	Secret string   `json:"secret"`
}

// Identity is who a request is authenticated as (GET /api/me).
type Identity struct {
	// Kind is "none" (auth disabled), "master" or "token".
	Kind  string     `json:"kind"`
	Name  string     `json:"name"`
	Scope TokenScope `json:"scope"`
}

// StartedBy is the Run.StartedBy value for the identity: "admin" for the
// master token, "token:<name>" for an API token, "" when auth is disabled.
func (i Identity) StartedBy() string {
	switch i.Kind {
	case "master":
		return "admin"
	case "token":
		return "token:" + i.Name
	}
	return ""
}

type identityKey struct{}

// WithIdentity returns ctx carrying id (set by the API auth middleware).
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity stored by WithIdentity.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// StartedByFrom returns the StartedBy value of the identity in ctx ("" if
// there is none).
func StartedByFrom(ctx context.Context) string {
	id, _ := IdentityFrom(ctx)
	return id.StartedBy()
}
