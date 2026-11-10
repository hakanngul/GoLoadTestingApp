package protocol

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSessionCookies(t *testing.T) {
	var none *Session // nil sessions are inert
	none.SetCookies(mustURL(t, "http://h.test/"), []*http.Cookie{{Name: "a", Value: "1"}})
	if got := none.CookieHeader(mustURL(t, "http://h.test/"), "x=1"); got != "x=1" {
		t.Errorf("nil session header = %q", got)
	}

	s := NewSession()
	u := mustURL(t, "https://shop.test/cart")
	if got := s.CookieHeader(u, ""); got != "" {
		t.Errorf("empty jar header = %q", got)
	}
	s.StoreResponse(u, http.Header{"Set-Cookie": {"sid=abc; Path=/; Secure", "theme=dark; Path=/"}})
	if got := s.CookieHeader(u, ""); got != "sid=abc; theme=dark" {
		t.Errorf("header = %q", got)
	}
	// Explicit cookies come first and win on a name clash.
	if got := s.CookieHeader(u, "theme=light;  extra = 1"); got != "theme=light;  extra = 1; sid=abc" {
		t.Errorf("merged header = %q", got)
	}
	// Scoped by host and by the Secure attribute.
	if got := s.CookieHeader(mustURL(t, "https://other.test/"), ""); got != "" {
		t.Errorf("other host header = %q", got)
	}
	if got := s.CookieHeader(mustURL(t, "http://shop.test/"), ""); got != "theme=dark" {
		t.Errorf("plain http header = %q", got)
	}
	// WebSocket URLs share the site's cookies: wss like https, ws like http.
	if got := s.CookieHeader(mustURL(t, "wss://shop.test/socket"), ""); got != "sid=abc; theme=dark" {
		t.Errorf("wss header = %q", got)
	}
	if got := s.CookieHeader(mustURL(t, "ws://shop.test/socket"), ""); got != "theme=dark" {
		t.Errorf("ws header = %q", got)
	}
	// A cookie set by a ws handshake is sent to the HTTP endpoints too.
	s.StoreResponse(mustURL(t, "wss://shop.test/socket"), http.Header{"Set-Cookie": {"wsid=9; Path=/"}})
	if got := s.CookieHeader(u, ""); got != "sid=abc; theme=dark; wsid=9" {
		t.Errorf("after ws set: %q", got)
	}
}

func TestSessionRecordAndSeed(t *testing.T) {
	u := mustURL(t, "http://h.test/login")
	rec := NewRecordingSession()
	rec.StoreResponse(u, http.Header{"Set-Cookie": {"session=one; Path=/"}})
	sets := rec.Recorded()
	if len(sets) != 1 || len(sets[0].Cookies) != 1 || NewSession().Recorded() != nil {
		t.Fatalf("recorded = %+v", sets)
	}

	type entry struct{ n int }
	e1, e2 := &entry{1}, &entry{2}
	vu := NewSession()
	vu.Seed(e1, sets)
	if got := vu.CookieHeader(u, ""); got != "session=one" {
		t.Fatalf("seeded header = %q", got)
	}
	// The VU's own later cookies are not overwritten by the same record.
	vu.StoreResponse(u, http.Header{"Set-Cookie": {"session=rotated; Path=/"}})
	vu.Seed(e1, sets)
	if got := vu.CookieHeader(u, ""); got != "session=rotated" {
		t.Errorf("re-seeded with the same record: %q", got)
	}
	// A new record (re-login) replaces them.
	vu.Seed(e2, sets)
	if got := vu.CookieHeader(u, ""); got != "session=one" {
		t.Errorf("seeded with a new record: %q", got)
	}
}

func TestSessionContext(t *testing.T) {
	ctx := context.Background()
	if SessionFrom(ctx) != nil {
		t.Fatal("session in an empty context")
	}
	c1, s1 := EnsureSession(ctx)
	if s1 == nil || SessionFrom(c1) != s1 {
		t.Fatal("EnsureSession did not attach a session")
	}
	if c2, s2 := EnsureSession(c1); s2 != s1 || c2 != c1 {
		t.Error("EnsureSession replaced an attached session")
	}
	// A context derived from the VU's context sees the same session.
	c3, cancel := context.WithCancel(c1)
	defer cancel()
	if SessionFrom(c3) != s1 {
		t.Error("derived context lost the session")
	}
}

// BenchmarkSession measures the jar's share of one request: building the
// Cookie header and scanning the response for Set-Cookie.
func BenchmarkSession(b *testing.B) {
	u := mustURL(b, "https://shop.test/tr/slot")
	plain := http.Header{"Content-Type": {"text/html"}}
	run := func(s *Session, explicit string) func(*testing.B) {
		return func(b *testing.B) {
			ctx := WithSession(context.Background(), s)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sess := SessionFrom(ctx)
				_ = sess.CookieHeader(u, explicit)
				sess.StoreResponse(u, plain)
			}
		}
	}
	b.Run("empty", run(NewSession(), ""))
	two := NewSession()
	two.StoreResponse(u, http.Header{"Set-Cookie": {"sid=0123456789abcdef0123456789abcdef; Path=/", "lang=tr; Path=/"}})
	b.Run("2cookies", run(two, ""))
	b.Run("2cookies+explicit", run(two, "lang=en; ab=1"))
}
