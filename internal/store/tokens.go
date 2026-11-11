package store

import (
	"context"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// TokenStore is the API-token side of a store. Every Store in this
// package implements it; it is a separate interface so test doubles of
// Store need not. Only the sha256 hash (hex) of a token secret is stored.
type TokenStore interface {
	// ListTokens returns every token, newest created first.
	ListTokens(ctx context.Context) ([]model.APIToken, error)
	// CreateToken stores t (Name, Scope, Prefix, ExpiresAt as given) with
	// a fresh ID, CreatedAt (when zero) and the secret's hash. Duplicate
	// name or hash: ErrConflict. The stored token is returned.
	CreateToken(ctx context.Context, t model.APIToken, hash string) (model.APIToken, error)
	// TokenByHash returns the token whose secret hashes to hash, or
	// ErrNotFound. Expiry is not checked here.
	TokenByHash(ctx context.Context, hash string) (model.APIToken, error)
	// DeleteToken revokes (deletes) a token. Unknown: ErrNotFound.
	DeleteToken(ctx context.Context, id string) error
	// TouchToken sets last_used_at. Unknown: ErrNotFound.
	TouchToken(ctx context.Context, id string, at time.Time) error
}

var (
	_ TokenStore = (*Memory)(nil)
	_ TokenStore = (*SQLite)(nil)
	_ TokenStore = (*Postgres)(nil)
)
