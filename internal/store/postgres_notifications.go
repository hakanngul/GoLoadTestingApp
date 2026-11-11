package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ NotificationStore = (*Postgres)(nil)

// Notification channel queries (the column list matches
// pgNotificationRow.dest).
const (
	pgGetNotificationSQL = `SELECT id::text, name, kind, url, events, only_failures, enabled, created_at, updated_at
	FROM notification_channels WHERE id = $1`

	pgListNotificationsSQL = `SELECT id::text, name, kind, url, events, only_failures, enabled, created_at, updated_at
	FROM notification_channels ORDER BY created_at, id`

	pgCreateNotificationSQL = `INSERT INTO notification_channels
	(id, name, kind, url, events, only_failures, enabled, created_at, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`

	pgUpdateNotificationSQL = `UPDATE notification_channels SET name = $2, kind = $3, url = $4, events = $5,
	only_failures = $6, enabled = $7, updated_at = $8 WHERE id = $1`

	pgDeleteNotificationSQL = `DELETE FROM notification_channels WHERE id = $1`
)

type pgNotificationRow struct {
	c      model.NotificationChannel
	events string
}

func (r *pgNotificationRow) dest() []any {
	c := &r.c
	return []any{&c.ID, &c.Name, &c.Kind, &c.URL, &r.events, &c.OnlyFailures, &c.Enabled, &c.CreatedAt, &c.UpdatedAt}
}

func (r *pgNotificationRow) channel() (model.NotificationChannel, error) {
	out := r.c
	events, err := decodeEvents(r.events)
	if err != nil {
		return model.NotificationChannel{}, err
	}
	out.Events = events
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}

func (p *Postgres) getNotification(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id string) (model.NotificationChannel, error) {
	var r pgNotificationRow
	if err := q.QueryRow(ctx, pgGetNotificationSQL, id).Scan(r.dest()...); err != nil {
		return model.NotificationChannel{}, mapErr(err)
	}
	return r.channel()
}

// ListNotificationChannels returns every channel, oldest created first.
func (p *Postgres) ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error) {
	rows, err := p.pool.Query(ctx, pgListNotificationsSQL)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.NotificationChannel{}
	for rows.Next() {
		var r pgNotificationRow
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, mapErr(err)
		}
		c, err := r.channel()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// GetNotificationChannel returns one channel.
func (p *Postgres) GetNotificationChannel(ctx context.Context, id string) (model.NotificationChannel, error) {
	if !isUUID(id) {
		return model.NotificationChannel{}, ErrNotFound
	}
	return p.getNotification(ctx, p.pool, id)
}

// CreateNotificationChannel stores a new channel.
func (p *Postgres) CreateNotificationChannel(ctx context.Context, c model.NotificationChannel) (model.NotificationChannel, error) {
	events, err := encodeEvents(c.Events)
	if err != nil {
		return model.NotificationChannel{}, err
	}
	id := newID()
	var out model.NotificationChannel
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, pgCreateNotificationSQL,
			id, c.Name, c.Kind, c.URL, events, c.OnlyFailures, c.Enabled, now()); err != nil {
			return mapErr(err)
		}
		out, err = p.getNotification(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.NotificationChannel{}, err
	}
	return out, nil
}

// UpdateNotificationChannel replaces everything but ID and CreatedAt.
func (p *Postgres) UpdateNotificationChannel(ctx context.Context, c model.NotificationChannel) (model.NotificationChannel, error) {
	if !isUUID(c.ID) {
		return model.NotificationChannel{}, ErrNotFound
	}
	events, err := encodeEvents(c.Events)
	if err != nil {
		return model.NotificationChannel{}, err
	}
	var out model.NotificationChannel
	err = pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, pgUpdateNotificationSQL,
			c.ID, c.Name, c.Kind, c.URL, events, c.OnlyFailures, c.Enabled, now())
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		out, err = p.getNotification(ctx, tx, c.ID)
		return err
	})
	if err != nil {
		return model.NotificationChannel{}, err
	}
	return out, nil
}

// DeleteNotificationChannel removes a channel.
func (p *Postgres) DeleteNotificationChannel(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := p.pool.Exec(ctx, pgDeleteNotificationSQL, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
