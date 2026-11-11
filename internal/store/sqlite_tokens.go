package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// API token queries (constant literals, see sqlite_sql_test.go).
const (
	sqliteTokenCols = `id, name, scope, prefix, created_at, last_used_at, expires_at`

	sqliteListTokensSQL = `SELECT id, name, scope, prefix, created_at, last_used_at, expires_at
	FROM api_tokens ORDER BY created_at DESC, id DESC`

	sqliteGetTokenSQL = `SELECT id, name, scope, prefix, created_at, last_used_at, expires_at
	FROM api_tokens WHERE id = ?`

	sqliteTokenByHashSQL = `SELECT id, name, scope, prefix, created_at, last_used_at, expires_at
	FROM api_tokens WHERE hash = ?`

	sqliteCreateTokenSQL = `INSERT INTO api_tokens (id, name, scope, prefix, hash, created_at, expires_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)`

	sqliteDeleteTokenSQL = `DELETE FROM api_tokens WHERE id = ?`

	sqliteTouchTokenSQL = `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`
)

func scanSQLiteToken(sc interface{ Scan(...any) error }) (model.APIToken, error) {
	var (
		t                 model.APIToken
		scope             string
		created           int64
		lastUsed, expires sql.NullInt64
	)
	if err := sc.Scan(&t.ID, &t.Name, &scope, &t.Prefix, &created, &lastUsed, &expires); err != nil {
		return model.APIToken{}, sqliteErr(err)
	}
	t.Scope = model.TokenScope(scope)
	t.CreatedAt = fromMicros(created)
	if lastUsed.Valid {
		v := fromMicros(lastUsed.Int64)
		t.LastUsedAt = &v
	}
	if expires.Valid {
		v := fromMicros(expires.Int64)
		t.ExpiresAt = &v
	}
	return t, nil
}

// ListTokens returns every token, newest created first.
func (s *SQLite) ListTokens(ctx context.Context) ([]model.APIToken, error) {
	rows, err := s.r.QueryContext(ctx, sqliteListTokensSQL)
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	out := []model.APIToken{}
	for rows.Next() {
		t, err := scanSQLiteToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, sqliteErr(rows.Err())
}

// CreateToken stores a new token.
func (s *SQLite) CreateToken(ctx context.Context, t model.APIToken, hash string) (model.APIToken, error) {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	id := newID()
	var out model.APIToken
	err := sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqliteCreateTokenSQL, id, t.Name, string(t.Scope), t.Prefix, hash,
			toMicros(t.CreatedAt), optMicros(t.ExpiresAt)); err != nil {
			return sqliteErr(err)
		}
		var err error
		out, err = scanSQLiteToken(tx.QueryRowContext(ctx, sqliteGetTokenSQL, id))
		return err
	})
	return out, err
}

// TokenByHash returns the token whose secret hashes to hash.
func (s *SQLite) TokenByHash(ctx context.Context, hash string) (model.APIToken, error) {
	return scanSQLiteToken(s.r.QueryRowContext(ctx, sqliteTokenByHashSQL, hash))
}

// DeleteToken revokes a token.
func (s *SQLite) DeleteToken(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, sqliteDeleteTokenSQL, id)
}

// TouchToken sets last_used_at.
func (s *SQLite) TouchToken(ctx context.Context, id string, at time.Time) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, sqliteTouchTokenSQL, toMicros(at), id)
}
