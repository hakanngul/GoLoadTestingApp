package protocol

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
)

// Session is the state one virtual user keeps between requests: its
// cookie jar. Like a Locust user (requests.Session) or a JMeter thread
// with a Cookie Manager, every virtual user has its own.
//
// Who owns a Session:
//   - closed model: the engine creates one per VU goroutine and attaches
//     it to the context of every iteration that VU runs, so cookies live
//     as long as the VU;
//   - open model: every iteration is a new visitor, so no Session is
//     attached and the executor creates a fresh one per iteration
//     (EnsureSession);
//   - a caller that attaches nothing gets the open-model behaviour.
//
// A Session is used by one iteration at a time; its jar is nevertheless
// safe for concurrent use. The jar is created lazily on the first stored
// cookie, so targets that never set cookies cost nothing.
type Session struct {
	jar *cookiejar.Jar

	// log, when non-nil, records every SetCookies call so that the
	// cookies can be replayed into other sessions (cached auth logins).
	log *[]CookieSet

	// seed identifies the last cookie record replayed into this session
	// (see Seed); nil if none.
	seed any
}

// CookieSet is the cookies one response stored, with the URL they came
// from (which decides their domain and path).
type CookieSet struct {
	URL     *url.URL
	Cookies []*http.Cookie
}

// NewSession returns an empty session.
func NewSession() *Session { return &Session{} }

// NewRecordingSession returns a session that also records what is stored
// in it; Recorded returns the record.
func NewRecordingSession() *Session { return &Session{log: new([]CookieSet)} }

// Recorded returns the cookies stored so far in a recording session, in
// order. The result must not be modified.
func (s *Session) Recorded() []CookieSet {
	if s == nil || s.log == nil {
		return nil
	}
	return *s.log
}

// cookieURL maps ws(s) URLs to http(s): cookies are shared between a
// site's HTTP and WebSocket endpoints, and the jar only knows http(s).
func cookieURL(u *url.URL) *url.URL {
	switch u.Scheme {
	case "ws":
		c := *u
		c.Scheme = "http"
		return &c
	case "wss":
		c := *u
		c.Scheme = "https"
		return &c
	}
	return u
}

// Cookies returns the cookies to send to u (http, https, ws or wss).
func (s *Session) Cookies(u *url.URL) []*http.Cookie {
	if s == nil || s.jar == nil {
		return nil
	}
	return s.jar.Cookies(cookieURL(u))
}

// SetCookies stores cookies received from u.
func (s *Session) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if s == nil || len(cookies) == 0 {
		return
	}
	u = cookieURL(u)
	if s.jar == nil {
		// No public suffix list: a load generator talks to the targets it
		// was pointed at, so cross-site "supercookies" are not a concern.
		s.jar, _ = cookiejar.New(nil) // never fails
	}
	s.jar.SetCookies(u, cookies)
	if s.log != nil {
		*s.log = append(*s.log, CookieSet{URL: u, Cookies: cookies})
	}
}

// StoreResponse stores the Set-Cookie headers of a response to u. It is
// free when the response sets no cookie.
func (s *Session) StoreResponse(u *url.URL, h http.Header) {
	if s == nil || len(h["Set-Cookie"]) == 0 {
		return
	}
	s.SetCookies(u, (&http.Response{Header: h}).Cookies())
}

// Seed replays recorded cookies into the session unless the same record
// (identified by key, compared with ==) was the last one replayed. Auth
// uses it to give every virtual user the cookies of a cached login.
func (s *Session) Seed(key any, sets []CookieSet) {
	if s == nil || s.seed == key {
		return
	}
	s.seed = key
	for _, cs := range sets {
		s.SetCookies(cs.URL, cs.Cookies)
	}
}

// CookieHeader returns the Cookie request header for u: the session's
// cookies merged with explicit, a Cookie header value the scenario set
// itself ("" if none). On a name clash the explicit cookie wins.
func (s *Session) CookieHeader(u *url.URL, explicit string) string {
	cs := s.Cookies(u)
	if len(cs) == 0 {
		return explicit
	}
	var b strings.Builder
	b.WriteString(explicit)
	for _, c := range cs {
		if explicit != "" && hasCookie(explicit, c.Name) {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.Name)
		b.WriteByte('=')
		b.WriteString(c.Value)
	}
	return b.String()
}

// hasCookie reports whether a Cookie header value defines name.
func hasCookie(header, name string) bool {
	for header != "" {
		var part string
		part, header, _ = strings.Cut(header, ";")
		n, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

type sessionKey struct{}

// WithSession returns a context carrying s; executors use it for every
// request made with that context.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFrom returns the session attached to ctx, or nil.
func SessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey{}).(*Session)
	return s
}

// EnsureSession returns the session attached to ctx; if there is none it
// attaches a fresh one (a new visitor) and returns the derived context.
func EnsureSession(ctx context.Context) (context.Context, *Session) {
	if s := SessionFrom(ctx); s != nil {
		return ctx, s
	}
	s := NewSession()
	return WithSession(ctx, s), s
}

// userAgent is the default User-Agent header value.
var userAgent atomic.Pointer[string]

// SetVersion sets the version reported in the default User-Agent
// ("GoLoad/<version>"). The binary calls it once at start-up.
func SetVersion(v string) {
	ua := "GoLoad"
	if v = strings.TrimSpace(v); v != "" {
		ua += "/" + v
	}
	userAgent.Store(&ua)
}

// UserAgent returns the default User-Agent of requests whose scenario
// does not set one: "GoLoad/<version>", or "GoLoad" if the version is
// unknown.
func UserAgent() string {
	if p := userAgent.Load(); p != nil {
		return *p
	}
	return "GoLoad"
}
