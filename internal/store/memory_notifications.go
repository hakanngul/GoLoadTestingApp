package store

import (
	"cmp"
	"context"
	"slices"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ NotificationStore = (*Memory)(nil)

func cloneChannel(c model.NotificationChannel) model.NotificationChannel {
	c.Events = slices.Clone(c.Events)
	if c.Events == nil {
		c.Events = []string{}
	}
	return c
}

// ListNotificationChannels returns every channel, oldest created first.
func (m *Memory) ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.NotificationChannel, 0, len(m.notifs))
	for _, c := range m.notifs {
		out = append(out, cloneChannel(*c))
	}
	slices.SortFunc(out, func(a, b model.NotificationChannel) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nil
}

// GetNotificationChannel returns one channel.
func (m *Memory) GetNotificationChannel(ctx context.Context, id string) (model.NotificationChannel, error) {
	if err := ctx.Err(); err != nil {
		return model.NotificationChannel{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.notifs[id]
	if !ok {
		return model.NotificationChannel{}, ErrNotFound
	}
	return cloneChannel(*c), nil
}

// CreateNotificationChannel stores a new channel.
func (m *Memory) CreateNotificationChannel(ctx context.Context, c model.NotificationChannel) (model.NotificationChannel, error) {
	if err := ctx.Err(); err != nil {
		return model.NotificationChannel{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ts := now()
	c.ID, c.CreatedAt, c.UpdatedAt = newID(), ts, ts
	stored := cloneChannel(c)
	m.notifs[c.ID] = &stored
	return cloneChannel(stored), nil
}

// UpdateNotificationChannel replaces everything but ID and CreatedAt.
func (m *Memory) UpdateNotificationChannel(ctx context.Context, c model.NotificationChannel) (model.NotificationChannel, error) {
	if err := ctx.Err(); err != nil {
		return model.NotificationChannel{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.notifs[c.ID]
	if !ok {
		return model.NotificationChannel{}, ErrNotFound
	}
	c.CreatedAt = cur.CreatedAt
	c.UpdatedAt = now()
	stored := cloneChannel(c)
	*cur = stored
	return cloneChannel(stored), nil
}

// DeleteNotificationChannel removes a channel.
func (m *Memory) DeleteNotificationChannel(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.notifs[id]; !ok {
		return ErrNotFound
	}
	delete(m.notifs, id)
	return nil
}
