package trial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

const cookieSecret = "s3cr3t-cookie-value-0123456789"

// cookieGate answers /tr/slot with a 302 to itself plus a cookie until
// the cookie comes back (the reported site), and /out with a 301 to the
// same path on a second server reached as "localhost" (another host).
func cookieGate(t *testing.T) (srv *httptest.Server, otherURL string, otherHits *atomic.Int64) {
	t.Helper()
	otherHits = new(atomic.Int64)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		fmt.Fprint(w, "moved here")
	}))
	t.Cleanup(other.Close)
	u, err := url.Parse(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	otherURL = "http://localhost:" + u.Port()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tr/slot":
			if _, err := r.Cookie("sid"); err != nil {
				http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookieSecret, Path: "/", HttpOnly: true})
				w.Header().Set("Location", "/tr/slot")
				w.WriteHeader(http.StatusFound)
				return
			}
			fmt.Fprint(w, "slots")
		case "/out":
			http.Redirect(w, r, otherURL+"/out", http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, otherURL, otherHits
}

func TestShowsRedirectHopsAndCookieNames(t *testing.T) {
	srv, _, _ := cookieGate(t)
	sc := parse(t, srv.URL, "steps:\n  - {name: slot, path: /tr/slot, assert: [{type: status, value: '200'}]}\n  - {name: again, path: /tr/slot}\n")
	res := Run(context.Background(), sc, Options{})
	if !res.OK || states(res) != "slot=passed,again=passed" {
		t.Fatalf("states = %s hints = %+v", states(res), res.Hints)
	}
	slot, again := res.Steps[0], res.Steps[1]
	if slot.Status != 200 || slot.Body != "slots" || slot.URL != srv.URL+"/tr/slot" || slot.FinalURL != srv.URL+"/tr/slot" || slot.Location != "" {
		t.Errorf("slot = %+v", slot)
	}
	if len(slot.Redirects) != 1 {
		t.Fatalf("redirects = %+v", slot.Redirects)
	}
	hop := slot.Redirects[0]
	if hop.Status != 302 || hop.Method != "GET" || hop.URL != srv.URL+"/tr/slot" || hop.Location != srv.URL+"/tr/slot" ||
		len(hop.CookiesSent) != 0 || strings.Join(hop.CookiesSet, ",") != "sid" {
		t.Errorf("hop = %+v", hop)
	}
	if strings.Join(slot.CookiesSent, ",") != "sid" || len(slot.CookiesSet) != 0 {
		t.Errorf("slot cookies: sent %v set %v", slot.CookiesSent, slot.CookiesSet)
	}
	// The second step of the iteration already holds the cookie.
	if len(again.Redirects) != 0 || again.Status != 200 || strings.Join(again.CookiesSent, ",") != "sid" || again.FinalURL != "" {
		t.Errorf("again = %+v", again)
	}
	// A self-redirect that resolves with the cookie is just information.
	hs := hintsWith(res, "cookie check")
	if len(res.Hints) != 1 || len(hs) != 1 || hs[0].Level != "info" || hs[0].Step != "slot" || hs[0].Fix != nil ||
		!strings.Contains(hs[0].Message, "(sid)") {
		t.Fatalf("hints = %+v", res.Hints)
	}
	// Cookie values never leave the server.
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), cookieSecret) {
		t.Errorf("the report contains a cookie value:\n%s", b)
	}
}

func TestCookiesDisabledLoopHint(t *testing.T) {
	srv, _, _ := cookieGate(t)
	y := fmt.Sprintf("name: t\ntarget:\n  base_url: %s\n  cookies: false\nload:\n  stages: [{duration: 1s, target: 1}]\nsteps:\n  - {name: slot, path: /tr/slot}\n", srv.URL)
	sc, err := scenario.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	res := Run(context.Background(), sc, Options{})
	if res.OK || res.Steps[0].Status != 302 || !strings.HasPrefix(res.Steps[0].Error, "redirect loop: /tr/slot → /tr/slot") {
		t.Fatalf("step = %+v", res.Steps[0])
	}
	hs := hintsWith(res, "enable cookies")
	if len(res.Hints) != 1 || len(hs) != 1 || hs[0].Level != "error" || !strings.Contains(hs[0].Message, "cookies: false") {
		t.Fatalf("hints = %+v", res.Hints)
	}
}

func TestFollowedRedirectToOtherHostSuggestsBaseURL(t *testing.T) {
	srv, otherURL, otherHits := cookieGate(t)
	sc := parse(t, srv.URL, "steps:\n  - {name: out, path: /out}\n")
	res := Run(context.Background(), sc, Options{})
	st := res.Steps[0]
	if !res.OK || st.Status != 200 || st.Body != "moved here" || st.FinalURL != otherURL+"/out" || len(st.Redirects) != 1 || otherHits.Load() != 1 {
		t.Fatalf("step = %+v hints = %+v", st, res.Hints)
	}
	// Followed, but an extra hop on every request is worth a warning.
	if len(res.Hints) != 1 {
		t.Fatalf("hints = %+v", res.Hints)
	}
	h := res.Hints[0]
	if h.Level != "warning" || h.Fix == nil || h.Fix.Kind != "base_url" || h.Fix.Value != otherURL ||
		!strings.Contains(h.Message, "GoLoad followed it") || !strings.Contains(h.Message, "extra hop") {
		t.Errorf("hint = %+v fix = %+v", h, h.Fix)
	}

	// With an allowlist that lacks the new host the hop is not sent.
	list, err := allowlist.Parse("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	res = Run(context.Background(), sc, Options{Allow: list.Check})
	st = res.Steps[0]
	if res.OK || st.State != model.TryFailed || st.Status != 301 || otherHits.Load() != 1 ||
		st.Error != "redirect to "+otherURL+"/out not allowed (host not in allowlist)" {
		t.Fatalf("blocked: step = %+v (other hits %d)", st, otherHits.Load())
	}
	if hs := hintsWith(res, `Host "localhost" is not on this server's allowlist`); len(res.Hints) != 1 || len(hs) != 1 || hs[0].Level != "error" {
		t.Fatalf("blocked: hints = %+v", res.Hints)
	}
	// Allowlisted: followed again.
	list, _ = allowlist.Parse("127.0.0.1,localhost")
	if res = Run(context.Background(), sc, Options{Allow: list.Check}); !res.OK || otherHits.Load() != 2 {
		t.Fatalf("allowed: %+v", res.Steps[0])
	}
}
