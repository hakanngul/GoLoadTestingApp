package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func TestDoAuthJSONAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(model.APIError{Error: "unauthorized"})
			return
		}
		switch r.URL.Path {
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"ct": r.Header.Get("Content-Type"), "body": string(b)})
		case "/bad":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(model.APIError{Error: "invalid", Details: []string{"a: b"}})
		default:
			http.Error(w, "plain failure", http.StatusTeapot)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL+"/", "tok")

	var out map[string]string
	if err := c.Do(ctx, http.MethodPost, "/echo", map[string]int{"a": 1}, &out); err != nil || out["ct"] != "application/json" || out["body"] != `{"a":1}` {
		t.Fatalf("Do = %v %v", out, err)
	}
	if err := c.DoRaw(ctx, http.MethodPut, "/echo", "text/csv", []byte("a,b\n"), &out); err != nil || out["ct"] != "text/csv" || out["body"] != "a,b\n" {
		t.Fatalf("DoRaw = %v %v", out, err)
	}

	var ae *APIError
	err := c.Do(ctx, http.MethodGet, "/bad", nil, nil)
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "invalid" || len(ae.Details) != 1 || ae.Transient() {
		t.Errorf("bad = %#v", err)
	}
	err = c.Do(ctx, http.MethodGet, "/other", nil, nil)
	if !errors.As(err, &ae) || ae.Status != http.StatusTeapot || !strings.Contains(ae.Message, "plain failure") {
		t.Errorf("plain = %#v", err)
	}
	c.Token = "wrong"
	err = c.Do(ctx, http.MethodGet, "/echo", nil, nil)
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Errorf("401 = %#v", err)
	}
}

func TestGetRetriesTransientOnly(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if gets.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		posts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	c.Backoff = time.Millisecond

	var out struct{ OK bool }
	if err := c.Do(context.Background(), http.MethodGet, "/", nil, &out); err != nil || !out.OK || gets.Load() != 3 {
		t.Errorf("GET: %v ok=%v attempts=%d", err, out.OK, gets.Load())
	}
	if err := c.Do(context.Background(), http.MethodPost, "/", map[string]int{}, nil); err == nil || posts.Load() != 1 {
		t.Errorf("POST must not be retried: %v attempts=%d", err, posts.Load())
	}
	c.Retries = 0
	gets.Store(0)
	if err := c.Do(context.Background(), http.MethodGet, "/", nil, nil); err == nil || gets.Load() != 1 {
		t.Errorf("Retries=0: %v attempts=%d", err, gets.Load())
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := New(url, "tok")
	c.Retries = 0
	var ae *APIError
	if err := c.Do(context.Background(), http.MethodGet, "/", nil, nil); !errors.As(err, &ae) || ae.Status != 0 || !ae.Transient() {
		t.Errorf("err = %#v", err)
	}
}

func TestResolveRef(t *testing.T) {
	items := []Ref{{ID: "id-1", Name: "checkout"}, {ID: "id-2", Name: "Checkout Flow"}, {ID: "id-3", Name: "login"}, {ID: "id-4", Name: "dup"}, {ID: "id-5", Name: "dup"}}
	if r, err := ResolveRef("test", "id-3", items); err != nil || r.Name != "login" {
		t.Errorf("by id = %v %v", r, err)
	}
	if r, err := ResolveRef("test", "checkout", items); err != nil || r.ID != "id-1" {
		t.Errorf("by name = %v %v", r, err)
	}
	_, err := ResolveRef("test", "check", items)
	if err == nil || !strings.Contains(err.Error(), `no test named or with id "check"`) || !strings.Contains(err.Error(), `"checkout", "Checkout Flow"`) {
		t.Errorf("suggestions = %v", err)
	}
	_, err = ResolveRef("suite", "zzz", items)
	if err == nil || !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), `"login"`) {
		t.Errorf("fallback to all names = %v", err)
	}
	_, err = ResolveRef("test", "dup", items)
	if err == nil || !strings.Contains(err.Error(), "id-4, id-5") {
		t.Errorf("ambiguous = %v", err)
	}
	if _, err := ResolveRef("test", "x", nil); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("empty = %v", err)
	}
}
