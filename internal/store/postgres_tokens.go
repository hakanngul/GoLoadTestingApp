package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// API token queries (the column list matches scanPgToken).
const (
	pgListTokensSQL = `SELECT id::text, name, scope, prefix, created_at, last_used_at, expires_at
	FROM api_tokens ORDER BY created_at DESC, id DESC`

	pgGetTokenSQL = `SELECT id::text, name, scope, prefix, created_at, last_used_at, expires_at
	FROM api_tokens WHERE id = $1`

	pgTokenByHashSQL = `SELECT id::text, name, scope, prefix, created_at, last_used_at, expires_at
	FROM api_tokens WHERE hash = $1`

	pgCreateTokenSQL = `INSERT INTO api_tokens (id, name, scope, prefix, hash, created_at, expires_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7)`

	pgDeleteTokenSQL = `DELETE FROM api_tokens WHERE id = $1`

	pgTouchTokenSQL = `UPDATE api_tokens SET last_used_at = $2 WHERE id = $1`
)

func scanPgToken(row pgx.Row) (model.APIToken, error) {
	var (
		t                 model.APIToken
		scope             string
		lastUsed, expires *time.Time
	)
	if err := row.Scan(&t.ID, &t.Name, &scope, &t.Prefix, &t.CreatedAt, &lastUsed, &expires); err != nil {
		return model.APIToken{}, mapErr(err)
	}
	t.Scope = model.TokenScope(scope)
	t.CreatedAt = normTime(t.CreatedAt)
	t.LastUsedAt = pgOptTime(lastUsed)
	t.ExpiresAt = pgOptTime(expires)
	return t, nil
}

// ListTokens returns every token, newest created first.
func (p *Postgres) ListTokens(ctx context.Context) ([]model.APIToken, error) {
	rows, err := p.pool.Query(ctx, pgListTokensSQL)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.APIToken{}
	for rows.Next() {
		t, err := scanPgToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapErr(rows.Err())
}

// CreateToken stores a new token.
func (p *Postgres) CreateToken(ctx context.Context, t model.APIToken, hash string) (model.APIToken, error) {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	id := newID()
	var out model.APIToken
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, pgCreateTokenSQL, id, t.Name, string(t.Scope), t.Prefix, hash,
			normTime(t.CreatedAt), pgOptTime(t.ExpiresAt)); err != nil {
			return mapErr(err)
		}
		var err error
		out, err = scanPgToken(tx.QueryRow(ctx, pgGetTokenSQL, id))
		return err
	})
	return out, err
}

// TokenByHash returns the token whose secret hashes to hash.
func (p *Postgres) TokenByHash(ctx context.Context, hash string) (model.APIToken, error) {
	return scanPgToken(p.pool.QueryRow(ctx, pgTokenByHashSQL, hash))
}

// DeleteToken revokes a token.
func (p *Postgres) DeleteToken(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, pgDeleteTokenSQL, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchToken sets last_used_at.
func (p *Postgres) TouchToken(ctx context.Context, id string, at time.Time) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, pgTouchTokenSQL, id, normTime(at))
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
