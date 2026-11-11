package store

import (
	"context"
	"database/sql"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

var _ NotificationStore = (*SQLite)(nil)

// Notification channel queries. The column list is repeated in each
// literal so every query is a constant (see sqliteNotificationCols and
// sqlite_sql_test.go).
const (
	sqliteNotificationCols = `id, name, kind, url, events, only_failures, enabled, created_at, updated_at`

	sqliteGetNotificationSQL = `SELECT id, name, kind, url, events, only_failures, enabled, created_at, updated_at
	FROM notification_channels WHERE id = ?`

	sqliteListNotificationsSQL = `SELECT id, name, kind, url, events, only_failures, enabled, created_at, updated_at
	FROM notification_channels ORDER BY created_at, id`

	sqliteCreateNotificationSQL = `INSERT INTO notification_channels
	(id, name, kind, url, events, only_failures, enabled, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqliteUpdateNotificationSQL = `UPDATE notification_channels SET name = ?, kind = ?, url = ?, events = ?,
	only_failures = ?, enabled = ?, updated_at = ? WHERE id = ?`

	sqliteDeleteNotificationSQL = `DELETE FROM notification_channels WHERE id = ?`
)

// sqliteNotificationRow receives one notification_channels row
// (sqliteNotificationCols).
type sqliteNotificationRow struct {
	c                     model.NotificationChannel
	events                string
	onlyFailures, enabled int64
	created, updated      int64
}

func (r *sqliteNotificationRow) dest() []any {
	c := &r.c
	return []any{&c.ID, &c.Name, &c.Kind, &c.URL, &r.events, &r.onlyFailures, &r.enabled, &r.created, &r.updated}
}

func (r *sqliteNotificationRow) channel() (model.NotificationChannel, error) {
	out := r.c
	events, err := decodeEvents(r.events)
	if err != nil {
		return model.NotificationChannel{}, err
	}
	out.Events = events
	out.OnlyFailures = r.onlyFailures != 0
	out.Enabled = r.enabled != 0
	out.CreatedAt = fromMicros(r.created)
	out.UpdatedAt = fromMicros(r.updated)
	return out, nil
}

func (s *SQLite) getNotification(ctx context.Context, q sqliteQuerier, id string) (model.NotificationChannel, error) {
	var r sqliteNotificationRow
	if err := q.QueryRowContext(ctx, sqliteGetNotificationSQL, id).Scan(r.dest()...); err != nil {
		return model.NotificationChannel{}, sqliteErr(err)
	}
	return r.channel()
}

// ListNotificationChannels returns every channel, oldest created first.
func (s *SQLite) ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error) {
	rows, err := s.r.QueryContext(ctx, sqliteListNotificationsSQL)
	if err != nil {
		return nil, sqliteErr(err)
	}
	defer rows.Close()
	out := []model.NotificationChannel{}
	for rows.Next() {
		var r sqliteNotificationRow
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, sqliteErr(err)
		}
		c, err := r.channel()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, sqliteErr(err)
	}
	return out, nil
}

// GetNotificationChannel returns one channel.
func (s *SQLite) GetNotificationChannel(ctx context.Context, id string) (model.NotificationChannel, error) {
	if !isUUID(id) {
		return model.NotificationChannel{}, ErrNotFound
	}
	return s.getNotification(ctx, s.r, id)
}

// CreateNotificationChannel stores a new channel.
func (s *SQLite) CreateNotificationChannel(ctx context.Context, c model.NotificationChannel) (model.NotificationChannel, error) {
	events, err := encodeEvents(c.Events)
	if err != nil {
		return model.NotificationChannel{}, err
	}
	id, ts := newID(), toMicros(now())
	var out model.NotificationChannel
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqliteCreateNotificationSQL,
			id, c.Name, c.Kind, c.URL, events, boolInt(c.OnlyFailures), boolInt(c.Enabled), ts, ts); err != nil {
			return sqliteErr(err)
		}
		out, err = s.getNotification(ctx, tx, id)
		return err
	})
	if err != nil {
		return model.NotificationChannel{}, err
	}
	return out, nil
}

// UpdateNotificationChannel replaces everything but ID and CreatedAt.
func (s *SQLite) UpdateNotificationChannel(ctx context.Context, c model.NotificationChannel) (model.NotificationChannel, error) {
	if !isUUID(c.ID) {
		return model.NotificationChannel{}, ErrNotFound
	}
	events, err := encodeEvents(c.Events)
	if err != nil {
		return model.NotificationChannel{}, err
	}
	var out model.NotificationChannel
	err = sqliteTx(ctx, s.w, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqliteUpdateNotificationSQL,
			c.Name, c.Kind, c.URL, events, boolInt(c.OnlyFailures), boolInt(c.Enabled), toMicros(now()), c.ID)
		if err != nil {
			return sqliteErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out, err = s.getNotification(ctx, tx, c.ID)
		return err
	})
	if err != nil {
		return model.NotificationChannel{}, err
	}
	return out, nil
}

// DeleteNotificationChannel removes a channel.
func (s *SQLite) DeleteNotificationChannel(ctx context.Context, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	return s.execOne(ctx, sqliteDeleteNotificationSQL, id)
}
