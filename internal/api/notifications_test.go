package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/store"
)

type fakeSender struct {
	mu   sync.Mutex
	got  []model.NotificationChannel
	fail error
}

func (f *fakeSender) SendTest(_ context.Context, c model.NotificationChannel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, c)
	return f.fail
}

const slackSecret = "https://hooks.slack.com/services/T000/B000/SECRETtoken9876"

func newNotifEnv(t *testing.T) (*env, *store.Memory, *fakeSender) {
	t.Helper()
	st := store.NewMemory()
	rn := newFakeRunner(st)
	snd := &fakeSender{}
	s := newServer(Config{Store: st, Runner: rn, Version: "test", Notifier: snd})
	return &env{h: s.handler(), srv: s, runner: rn}, st, snd
}

func TestNotificationsCRUDMasksURL(t *testing.T) {
	e, st, _ := newNotifEnv(t)

	rec := e.json(t, "GET", "/api/notifications", nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %s", rec.Body.String())
	}

	rec = e.json(t, "POST", "/api/notifications", map[string]any{
		"name": " ops ", "kind": "slack", "url": slackSecret,
		"events": []string{"run_finished", "suite_finished", "run_finished"}, "only_failures": true,
	})
	wantStatus(t, rec, http.StatusCreated)
	if strings.Contains(rec.Body.String(), "SECRET") || strings.Contains(rec.Body.String(), `"url"`) {
		t.Fatalf("create response leaks url: %s", rec.Body.String())
	}
	v := decode[model.NotificationView](t, rec)
	if v.ID == "" || v.Name != "ops" || v.Kind != "slack" || !v.Enabled || !v.OnlyFailures ||
		len(v.Events) != 2 || v.URLMasked != "https://hooks.slack.com/…9876" {
		t.Fatalf("created = %+v", v)
	}
	stored, _ := st.GetNotificationChannel(context.Background(), v.ID)
	if stored.URL != slackSecret {
		t.Fatalf("stored url = %q", stored.URL)
	}

	rec = e.json(t, "GET", "/api/notifications", nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "SECRET") || !strings.Contains(rec.Body.String(), "url_masked") {
		t.Fatalf("list = %s", rec.Body.String())
	}

	// Update with an empty url keeps the stored one; enabled omitted keeps it.
	rec = e.json(t, "PUT", "/api/notifications/"+v.ID, map[string]any{
		"name": "ops2", "kind": "slack", "events": []string{"suite_finished"}, "enabled": false,
	})
	wantStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("update leaks url: %s", rec.Body.String())
	}
	got := decode[model.NotificationView](t, rec)
	if got.Name != "ops2" || got.Enabled || got.OnlyFailures || len(got.Events) != 1 || got.URLMasked != v.URLMasked {
		t.Fatalf("updated = %+v", got)
	}
	stored, _ = st.GetNotificationChannel(context.Background(), v.ID)
	if stored.URL != slackSecret {
		t.Fatalf("url changed by empty-url update: %q", stored.URL)
	}

	// A new url replaces it.
	rec = e.json(t, "PUT", "/api/notifications/"+v.ID, map[string]any{
		"name": "ops2", "kind": "webhook", "url": "https://example.com/hook/abcd", "events": []string{"run_finished"},
	})
	wantStatus(t, rec, http.StatusOK)
	stored, _ = st.GetNotificationChannel(context.Background(), v.ID)
	if stored.URL != "https://example.com/hook/abcd" || stored.Kind != "webhook" || stored.Enabled {
		t.Fatalf("stored after url update = %+v", stored)
	}

	rec = e.json(t, "DELETE", "/api/notifications/"+v.ID, nil)
	wantStatus(t, rec, http.StatusNoContent)
	rec = e.json(t, "DELETE", "/api/notifications/"+v.ID, nil)
	wantStatus(t, rec, http.StatusNotFound)
	rec = e.json(t, "PUT", "/api/notifications/"+v.ID, map[string]any{"name": "x", "kind": "slack", "events": []string{"run_finished"}})
	wantStatus(t, rec, http.StatusNotFound)
}

func TestNotificationsValidation(t *testing.T) {
	e, _, _ := newNotifEnv(t)
	ok := func() map[string]any {
		return map[string]any{"name": "n", "kind": "webhook", "url": "https://example.com/x", "events": []string{"run_finished"}}
	}
	cases := []struct {
		name   string
		mutate func(m map[string]any)
		want   string
	}{
		{"no name", func(m map[string]any) { m["name"] = "  " }, "name: required"},
		{"long name", func(m map[string]any) { m["name"] = strings.Repeat("x", 101) }, "name: must be at most 100"},
		{"bad kind", func(m map[string]any) { m["kind"] = "email" }, "kind:"},
		{"no url", func(m map[string]any) { delete(m, "url") }, "url: required"},
		{"http remote", func(m map[string]any) { m["url"] = "http://example.com/x" }, "url: must use https"},
		{"ftp", func(m map[string]any) { m["url"] = "ftp://example.com/x" }, "url: must start with https://"},
		{"relative", func(m map[string]any) { m["url"] = "/hooks/x" }, "url: must be an absolute URL"},
		{"no events", func(m map[string]any) { m["events"] = []string{} }, "events: at least one"},
		{"bad event", func(m map[string]any) { m["events"] = []string{"run_started"} }, "events: unknown event"},
	}
	for _, c := range cases {
		m := ok()
		c.mutate(m)
		rec := e.json(t, "POST", "/api/notifications", m)
		apiErr := wantAPIError(t, rec, http.StatusBadRequest, "invalid notification channel")
		if !strings.Contains(strings.Join(apiErr.Details, "|"), c.want) {
			t.Errorf("%s: details = %v, want %q", c.name, apiErr.Details, c.want)
		}
	}
	// Validation errors never echo the URL.
	m := ok()
	m["url"] = "http://example.com/SECRETvalue"
	rec := e.json(t, "POST", "/api/notifications", m)
	if strings.Contains(rec.Body.String(), "SECRETvalue") {
		t.Fatalf("validation error echoes the url: %s", rec.Body.String())
	}

	// http is fine for loopback hosts.
	for _, u := range []string{"http://localhost:18202/x", "http://127.0.0.1:18202/x", "http://[::1]:18202/x"} {
		m := ok()
		m["url"] = u
		wantStatus(t, e.json(t, "POST", "/api/notifications", m), http.StatusCreated)
	}
	// Unknown fields and bad JSON are rejected.
	m = ok()
	m["extra"] = 1
	wantStatus(t, e.json(t, "POST", "/api/notifications", m), http.StatusBadRequest)
}

func TestNotificationsTestEndpoint(t *testing.T) {
	e, _, snd := newNotifEnv(t)
	rec := e.json(t, "POST", "/api/notifications", map[string]any{
		"name": "ops", "kind": "slack", "url": slackSecret, "events": []string{"run_finished"}})
	wantStatus(t, rec, http.StatusCreated)
	id := decode[model.NotificationView](t, rec).ID

	rec = e.json(t, "POST", "/api/notifications/"+id+"/test", nil)
	wantStatus(t, rec, http.StatusOK)
	if r := decode[notificationTestResult](t, rec); !r.OK || r.Error != "" {
		t.Fatalf("result = %+v", r)
	}
	if len(snd.got) != 1 || snd.got[0].URL != slackSecret {
		t.Fatalf("sender got %+v (must receive the stored url)", snd.got)
	}

	snd.fail = errors.New("endpoint answered HTTP 404")
	rec = e.json(t, "POST", "/api/notifications/"+id+"/test", nil)
	wantStatus(t, rec, http.StatusOK)
	if r := decode[notificationTestResult](t, rec); r.OK || r.Error != "endpoint answered HTTP 404" {
		t.Fatalf("failed result = %+v", r)
	}

	wantStatus(t, e.json(t, "POST", "/api/notifications/nope/test", nil), http.StatusNotFound)
}

func TestNotifications503(t *testing.T) {
	// fakeStore does not implement store.NotificationStore.
	e := newEnv(t)
	for _, c := range [][2]string{
		{"GET", "/api/notifications"}, {"POST", "/api/notifications"}, {"PUT", "/api/notifications/x"},
		{"DELETE", "/api/notifications/x"}, {"POST", "/api/notifications/x/test"},
	} {
		wantAPIError(t, e.json(t, c[0], c[1], map[string]any{}), http.StatusServiceUnavailable, "notifications are not available")
	}

	// Store without a Notifier: only the test route is unavailable.
	st := store.NewMemory()
	s := newServer(Config{Store: st, Runner: newFakeRunner(st)})
	e2 := &env{h: s.handler(), srv: s}
	ch, _ := st.CreateNotificationChannel(context.Background(), model.NotificationChannel{Name: "n", Kind: "slack", URL: "https://x.test/y", Events: []string{"run_finished"}})
	wantAPIError(t, e2.json(t, "POST", "/api/notifications/"+ch.ID+"/test", nil), http.StatusServiceUnavailable, "sending notifications")
}

func TestValidateWebhookURL(t *testing.T) {
	good := []string{"https://hooks.slack.com/services/a/b/c", "http://localhost/x", "http://LOCALHOST:1/x", "http://127.0.0.1:8/x"}
	bad := []string{"", "https://", "http://localhost.evil.com/x", "http://10.0.0.1/x", "javascript:alert(1)", "https://" + strings.Repeat("a", 2100)}
	for _, u := range good {
		if m := validateWebhookURL(u); m != "" {
			t.Errorf("validateWebhookURL(%q) = %q, want ok", u, m)
		}
	}
	for _, u := range bad {
		if validateWebhookURL(u) == "" {
			t.Errorf("validateWebhookURL(%q) accepted", u)
		}
	}
}
