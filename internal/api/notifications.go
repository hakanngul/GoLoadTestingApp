package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/notify"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

// NotificationSender delivers a test message to a channel (implemented by
// *notify.Notifier).
type NotificationSender interface {
	SendTest(ctx context.Context, c model.NotificationChannel) error
}

var _ NotificationSender = (*notify.Notifier)(nil)

// Limits on notification channels.
const (
	maxChannelNameLen = 100
	maxChannelURLLen  = 2048
)

var (
	errNoNotifications = &httpError{status: http.StatusServiceUnavailable, msg: "notifications are not available on this server"}
	errNoSender        = &httpError{status: http.StatusServiceUnavailable, msg: "sending notifications is not available on this server"}
)

// notifStore returns the notification side of the store, or answers 503.
func (s *server) notifStore(w http.ResponseWriter, r *http.Request) (store.NotificationStore, bool) {
	ns, ok := s.store.(store.NotificationStore)
	if !ok {
		s.writeErr(w, r, errNoNotifications)
		return nil, false
	}
	return ns, true
}

// validateNotification checks req and returns the problems as
// "field: message" strings. requireURL is false on update (empty keeps the
// stored URL).
func validateNotification(req model.NotificationRequest, requireURL bool) []string {
	var d []string
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		d = append(d, "name: required")
	case utf8.RuneCountInString(name) > maxChannelNameLen:
		d = append(d, fmt.Sprintf("name: must be at most %d characters", maxChannelNameLen))
	}
	if req.Kind != model.NotifySlack && req.Kind != model.NotifyWebhook {
		d = append(d, fmt.Sprintf("kind: must be %q or %q", model.NotifySlack, model.NotifyWebhook))
	}
	switch {
	case req.URL == "" && !requireURL:
	case req.URL == "":
		d = append(d, "url: required")
	default:
		if msg := validateWebhookURL(req.URL); msg != "" {
			d = append(d, "url: "+msg)
		}
	}
	if len(req.Events) == 0 {
		d = append(d, "events: at least one event is required")
	}
	for _, e := range req.Events {
		if e != model.EventRunFinished && e != model.EventSuiteFinished {
			d = append(d, fmt.Sprintf("events: unknown event %q", e))
			break
		}
	}
	return d
}

// validateWebhookURL returns why raw is not an acceptable webhook URL, or
// "". https is required; http is allowed for localhost only. The URL is
// never echoed back.
func validateWebhookURL(raw string) string {
	if len(raw) > maxChannelURLLen {
		return fmt.Sprintf("must be at most %d characters", maxChannelURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "must be an absolute URL"
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "must use https (http is only allowed for localhost)"
		}
	default:
		return "must start with https://"
	}
	return ""
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// normalizeEvents drops duplicates, keeping order.
func normalizeEvents(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *server) invalidChannel(w http.ResponseWriter, details []string) {
	writeError(w, http.StatusBadRequest, "invalid notification channel", details...)
}

func (s *server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.notifStore(w, r)
	if !ok {
		return
	}
	list, err := ns.ListNotificationChannels(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]model.NotificationView, 0, len(list))
	for _, c := range list {
		out = append(out, notify.View(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.notifStore(w, r)
	if !ok {
		return
	}
	var req model.NotificationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if d := validateNotification(req, true); len(d) > 0 {
		s.invalidChannel(w, d)
		return
	}
	c := model.NotificationChannel{
		Name: strings.TrimSpace(req.Name), Kind: req.Kind, URL: req.URL,
		Events: normalizeEvents(req.Events), OnlyFailures: req.OnlyFailures, Enabled: req.Enabled == nil || *req.Enabled,
	}
	created, err := ns.CreateNotificationChannel(r.Context(), c)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, notify.View(created))
}

func (s *server) handleUpdateNotification(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.notifStore(w, r)
	if !ok {
		return
	}
	var req model.NotificationRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeErr(w, r, err)
		return
	}
	cur, err := ns.GetNotificationChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if d := validateNotification(req, false); len(d) > 0 {
		s.invalidChannel(w, d)
		return
	}
	cur.Name, cur.Kind = strings.TrimSpace(req.Name), req.Kind
	cur.Events, cur.OnlyFailures = normalizeEvents(req.Events), req.OnlyFailures
	if req.URL != "" {
		cur.URL = req.URL
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	updated, err := ns.UpdateNotificationChannel(r.Context(), cur)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, notify.View(updated))
}

func (s *server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.notifStore(w, r)
	if !ok {
		return
	}
	if err := ns.DeleteNotificationChannel(r.Context(), r.PathValue("id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notificationTestResult is the body of POST /api/notifications/{id}/test.
type notificationTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	ns, ok := s.notifStore(w, r)
	if !ok {
		return
	}
	if s.notifier == nil {
		s.writeErr(w, r, errNoSender)
		return
	}
	c, err := ns.GetNotificationChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.notifier.SendTest(r.Context(), c); err != nil {
		// The delivery error never contains the URL (see package notify).
		writeJSON(w, http.StatusOK, notificationTestResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, notificationTestResult{OK: true})
}
