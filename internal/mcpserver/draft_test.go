package mcpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDraftTestThenTryAndCreate(t *testing.T) {
	e := newEnv(t)
	out, text := e.call("draft_test", map[string]any{
		"url": e.target.URL + "/", "goal": "load", "users": 7, "duration": "40s", "extra_paths": []string{"/products"},
	})
	if !strings.HasPrefix(text, "Drafted ") {
		t.Fatalf("summary %q", text)
	}
	y, _ := out["yaml"].(string)
	for _, want := range []string{"base_url: " + e.target.URL, "path: /products", "target: 7"} {
		if !strings.Contains(y, want) {
			t.Errorf("yaml lacks %q:\n%s", want, y)
		}
	}
	tr, _ := e.call("try_test", map[string]any{"yaml": y})
	if ok, _ := tr["ok"].(bool); !ok {
		t.Fatalf("drafted scenario failed try_test: %v", tr)
	}
	created, _ := e.call("create_test", map[string]any{"name": out["name"], "yaml": y})
	if created["id"] == "" {
		t.Fatalf("create_test: %v", created)
	}
}

func TestDraftTestLoginNeedsDataFile(t *testing.T) {
	e := newEnv(t)
	out, _ := e.call("draft_test", map[string]any{
		"url": e.target.URL + "/", "login": map[string]any{"url": "/api/login", "token_path": "access_token"},
	})
	if out["data_file"] != "users.csv" {
		t.Fatalf("data_file = %v", out["data_file"])
	}
	y, _ := out["yaml"].(string)
	if !strings.Contains(y, "{{password}}") || strings.Contains(strings.ToLower(y), "secret") {
		t.Errorf("credentials must be templated:\n%s", y)
	}
	if next, _ := out["next"].(string); !strings.Contains(next, "data_file") {
		t.Errorf("next = %q", next)
	}
}

func TestDraftTestErrors(t *testing.T) {
	e := newEnv(t)
	if msg := e.callErr("draft_test", map[string]any{"url": "ftp://x"}); !strings.Contains(msg, "http") {
		t.Errorf("bad url: %q", msg)
	}
	if msg := e.callErr("draft_test", map[string]any{"url": "https://x.example", "duration": "soon"}); !strings.Contains(msg, "30s") {
		t.Errorf("bad duration: %q", msg)
	}
}

func TestDiscoverLinks(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/products">P</a> <a href="/cart?x=1#top">C</a>
<a href="https://elsewhere.example/">E</a> <a href="/logout">L</a> <link href="/app.css"></body></html>`)
	}))
	t.Cleanup(site.Close)
	e := newEnv(t)
	out, _ := e.call("discover_links", map[string]any{"url": site.URL + "/"})
	links := fmt.Sprint(out["links"])
	for _, want := range []string{site.URL + "/products", site.URL + "/cart?x=1"} {
		if !strings.Contains(links, want) {
			t.Errorf("links %s lack %s", links, want)
		}
	}
	for _, bad := range []string{"elsewhere", "logout", "app.css", "#top"} {
		if strings.Contains(links, bad) {
			t.Errorf("links %s contain %s", links, bad)
		}
	}
	if out["status"] != float64(200) {
		t.Errorf("status = %v", out["status"])
	}
}
