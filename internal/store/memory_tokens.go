package store

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

type memToken struct {
	tok  model.APIToken
	hash string
}

func cloneToken(t model.APIToken) model.APIToken {
	if t.LastUsedAt != nil {
		v := *t.LastUsedAt
		t.LastUsedAt = &v
	}
	if t.ExpiresAt != nil {
		v := *t.ExpiresAt
		t.ExpiresAt = &v
	}
	return t
}

// ListTokens returns every token, newest created first.
func (m *Memory) ListTokens(ctx context.Context) ([]model.APIToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.APIToken, 0, len(m.tokens))
	for _, t := range m.tokens {
		out = append(out, cloneToken(t.tok))
	}
	slices.SortFunc(out, func(a, b model.APIToken) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out, nil
}

// CreateToken stores a new token.
func (m *Memory) CreateToken(ctx context.Context, t model.APIToken, hash string) (model.APIToken, error) {
	if err := ctx.Err(); err != nil {
		return model.APIToken{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.tokens {
		if o.tok.Name == t.Name || o.hash == hash {
			return model.APIToken{}, ErrConflict
		}
	}
	t = cloneToken(t)
	t.ID = newID()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	t.CreatedAt = normTime(t.CreatedAt)
	t.LastUsedAt = nil
	if t.ExpiresAt != nil {
		v := normTime(*t.ExpiresAt)
		t.ExpiresAt = &v
	}
	m.tokens[t.ID] = &memToken{tok: t, hash: hash}
	return cloneToken(t), nil
}

// TokenByHash returns the token whose secret hashes to hash.
func (m *Memory) TokenByHash(ctx context.Context, hash string) (model.APIToken, error) {
	if err := ctx.Err(); err != nil {
		return model.APIToken{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, t := range m.tokens {
		if t.hash == hash {
			return cloneToken(t.tok), nil
		}
	}
	return model.APIToken{}, ErrNotFound
}

// DeleteToken revokes a token.
func (m *Memory) DeleteToken(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[id]; !ok {
		return ErrNotFound
	}
	delete(m.tokens, id)
	return nil
}

// TouchToken sets last_used_at.
func (m *Memory) TouchToken(ctx context.Context, id string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return ErrNotFound
	}
	v := normTime(at)
	t.tok.LastUsedAt = &v
	return nil
}
