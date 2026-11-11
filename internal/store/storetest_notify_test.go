package store

import (
	"errors"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func notifStore(t *testing.T, s Store) NotificationStore {
	t.Helper()
	ns, ok := s.(NotificationStore)
	if !ok {
		t.Fatalf("%T does not implement NotificationStore", s)
	}
	return ns
}

func testNotificationChannels(t *testing.T, s Store) {
	ns := notifStore(t, s)
	c := ctx(t)

	list, err := ns.ListNotificationChannels(c)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty list = %v, %v; want empty non-nil", list, err)
	}

	in := model.NotificationChannel{
		ID: "ignored", Name: "ops", Kind: model.NotifySlack, URL: "https://hooks.slack.com/services/T0/B0/secret1234",
		Events: []string{model.EventRunFinished, model.EventSuiteFinished}, OnlyFailures: true, Enabled: true,
	}
	got, err := ns.CreateNotificationChannel(c, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !isUUID(got.ID) || got.ID == "ignored" {
		t.Fatalf("ID %q not a fresh UUID", got.ID)
	}
	if got.Name != in.Name || got.Kind != in.Kind || got.URL != in.URL || !got.OnlyFailures || !got.Enabled ||
		len(got.Events) != 2 || got.Events[0] != model.EventRunFinished || got.Events[1] != model.EventSuiteFinished {
		t.Fatalf("created = %+v", got)
	}
	if got.CreatedAt.IsZero() || !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("timestamps = %v / %v", got.CreatedAt, got.UpdatedAt)
	}

	second, err := ns.CreateNotificationChannel(c, model.NotificationChannel{
		Name: "hook", Kind: model.NotifyWebhook, URL: "http://localhost:9/x", Events: []string{model.EventRunFinished},
	})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if second.Enabled || second.OnlyFailures {
		t.Fatalf("flags should default to false: %+v", second)
	}

	list, err = ns.ListNotificationChannels(c)
	if err != nil || len(list) != 2 || list[0].ID != got.ID || list[1].ID != second.ID {
		t.Fatalf("list = %+v, %v; want oldest first", list, err)
	}

	// Mutating a returned slice must not change the store.
	list[0].Events[0] = "mutated"
	again, err := ns.GetNotificationChannel(c, got.ID)
	if err != nil || again.Events[0] != model.EventRunFinished {
		t.Fatalf("Get after mutation = %+v, %v", again, err)
	}

	upd := again
	upd.Name, upd.URL, upd.Events, upd.OnlyFailures, upd.Enabled = "ops2", "https://example.com/hook", []string{model.EventSuiteFinished}, false, false
	upd.CreatedAt = second.CreatedAt // must be ignored
	out, err := ns.UpdateNotificationChannel(c, upd)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Name != "ops2" || out.URL != "https://example.com/hook" || len(out.Events) != 1 || out.OnlyFailures || out.Enabled {
		t.Fatalf("updated = %+v", out)
	}
	if !out.CreatedAt.Equal(got.CreatedAt) || out.UpdatedAt.Before(got.UpdatedAt) {
		t.Fatalf("timestamps after update = %v / %v (created %v)", out.CreatedAt, out.UpdatedAt, got.CreatedAt)
	}

	missing := model.NotificationChannel{ID: "6f1c1a52-4d0e-4a53-9a0b-0a2a8f3b7c11", Name: "x", Kind: model.NotifySlack, URL: "https://x.test/y"}
	if _, err := ns.UpdateNotificationChannel(c, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update unknown = %v, want ErrNotFound", err)
	}
	if _, err := ns.GetNotificationChannel(c, missing.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unknown = %v, want ErrNotFound", err)
	}
	if _, err := ns.GetNotificationChannel(c, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get bad id = %v, want ErrNotFound", err)
	}

	if err := ns.DeleteNotificationChannel(c, got.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := ns.DeleteNotificationChannel(c, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete twice = %v, want ErrNotFound", err)
	}
	if err := ns.DeleteNotificationChannel(c, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete bad id = %v, want ErrNotFound", err)
	}
	list, _ = ns.ListNotificationChannels(c)
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("after delete list = %+v", list)
	}
}
