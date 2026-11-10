package trial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"

// target serves the endpoints the tests use and counts requests.
func target(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /moved", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://www.stage.test:443/moved")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /slash", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/slash/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ U, P string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("Set-Cookie", "sid=supersecretvalue; HttpOnly")
		fmt.Fprintf(w, `{"access_token":%q,"user":%q}`, jwt, in.U)
	})
	mux.HandleFunc("GET /items", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+jwt && r.URL.Query().Get("open") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"id":42,"name":"shoe"}]}`)
	})
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q}`, r.PathValue("id"))
	})
	mux.HandleFunc("GET /html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>hello</html>")
	})
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("abcdefghij", 10_000))
	})
	mux.HandleFunc("GET /bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3})
	})
	mux.HandleFunc("GET /cf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "cloudflare")
		w.Header().Set("Cf-Ray", "8a1b2c3d4e5f-FRA")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<title>Attention Required! | Cloudflare</title>")
	})
	mux.HandleFunc("GET /echo", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.RawQuery)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func parse(t *testing.T, base, body string) *scenario.Scenario {
	t.Helper()
	y := fmt.Sprintf("name: t\ntarget:\n  base_url: %s\n  timeout: 5s\nload:\n  stages: [{duration: 1s, target: 1}]\n%s", base, body)
	sc, err := scenario.Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, y)
	}
	return sc
}

func hintsWith(res model.TryResult, sub string) []model.TryHint {
	var out []model.TryHint
	for _, h := range res.Hints {
		if strings.Contains(h.Message, sub) {
			out = append(out, h)
		}
	}
	return out
}

func states(res model.TryResult) string {
	var s []string
	for _, st := range res.Steps {
		s = append(s, st.Name+"="+st.State)
	}
	return strings.Join(s, ",")
}

func TestRedirectToOtherHostSuggestsBaseURL(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, `
steps:
  - name: home
    path: /moved
    follow_redirects: false   # the target host does not exist
    assert: [{type: status, value: "200"}]
  - name: next
    path: /items?open=1
`)
	res := Run(context.Background(), sc, Options{})
	if res.OK || states(res) != "home=failed,next=not_run" {
		t.Fatalf("states = %s", states(res))
	}
	home := res.Steps[0]
	if home.Status != 301 || home.Location != "https://www.stage.test:443/moved" {
		t.Errorf("home = %+v", home)
	}
	if !strings.HasSuffix(home.Error, "(redirects to https://www.stage.test:443/moved)") {
		t.Errorf("error = %q", home.Error)
	}
	hs := hintsWith(res, "redirect")
	if len(hs) != 1 || hs[0].Fix == nil {
		t.Fatalf("hints = %+v", res.Hints)
	}
	if f := hs[0].Fix; f.Kind != "base_url" || f.Value != "https://www.stage.test" {
		t.Errorf("fix = %+v", f)
	}
	if hs[0].Level != "error" || hs[0].Step != "home" {
		t.Errorf("hint = %+v", hs[0])
	}
	// The failed status assertion is explained by the redirect hint only.
	if len(res.Hints) != 1 {
		t.Errorf("hints = %+v", res.Hints)
	}
}

func TestRedirectSamePathSuggestsPath(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, "steps:\n  - {name: s, path: /slash}\n")
	res := Run(context.Background(), sc, Options{})
	// The 301 is followed (to a 404 here); the hint proposes the final
	// path, which would save the extra hop.
	st := res.Steps[0]
	if st.Status != 404 || len(st.Redirects) != 1 || st.Redirects[0].Status != 301 ||
		st.Redirects[0].Location != srv.URL+"/slash/" || st.FinalURL != srv.URL+"/slash/" {
		t.Fatalf("step = %+v", st)
	}
	hs := hintsWith(res, "redirected")
	if len(hs) != 1 || hs[0].Level != "info" || hs[0].Fix == nil ||
		hs[0].Fix.Kind != "step_path" || hs[0].Fix.Value != "/slash/" || hs[0].Fix.Step != 0 {
		t.Fatalf("hints = %+v", res.Hints)
	}

	// follow_redirects: false: the 301 passes (no status assertion) and
	// the hint is informational.
	sc = parse(t, srv.URL, "steps:\n  - {name: s, path: /slash, follow_redirects: false}\n")
	res = Run(context.Background(), sc, Options{})
	if !res.OK || res.Steps[0].Status != 301 {
		t.Fatalf("states = %s", states(res))
	}
	hs = hintsWith(res, "follow_redirects is false")
	if len(hs) != 1 || hs[0].Level != "info" || hs[0].Fix == nil || hs[0].Fix.Value != "/slash/" {
		t.Fatalf("hints = %+v", res.Hints)
	}
}

func TestPassWithExtractAndAssertions(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, `
steps:
  - name: list
    path: /items?open=1
    extract: [{var: id, from: json, expr: "items[0].id"}]
    assert:
      - {type: status, value: "200"}
      - {type: json, expr: "items[0].name", value: shoe}
  - name: one
    path: /items/{{id}}
    assert: [{type: json, expr: id, value: "42"}]
`)
	res := Run(context.Background(), sc, Options{ColumnsKnown: true})
	if !res.OK || states(res) != "list=passed,one=passed" {
		t.Fatalf("states = %s hints = %+v", states(res), res.Hints)
	}
	list, one := res.Steps[0], res.Steps[1]
	if len(list.Extracted) != 1 || list.Extracted[0].Value != "42" || !list.Extracted[0].OK {
		t.Errorf("extracted = %+v", list.Extracted)
	}
	if len(list.Assertions) != 2 || !list.Assertions[0].Passed || !list.Assertions[1].Passed {
		t.Errorf("assertions = %+v", list.Assertions)
	}
	if one.URL != srv.URL+"/items/42" || one.Status != 200 || one.Body != `{"id":"42"}` || one.DurationMs <= 0 {
		t.Errorf("one = %+v", one)
	}
	if len(res.Hints) != 0 {
		t.Errorf("hints = %+v", res.Hints)
	}
	var ct bool
	for _, h := range one.ResponseHeaders {
		ct = ct || (h.Name == "Content-Type" && h.Value == "application/json")
	}
	if !ct {
		t.Errorf("response headers = %+v", one.ResponseHeaders)
	}
}

func TestExtractMiss(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, `
steps:
  - name: page
    path: /html
    extract: [{var: id, from: json, expr: data.id}]
  - name: use
    path: /items/{{id}}
`)
	res := Run(context.Background(), sc, Options{ColumnsKnown: true})
	if states(res) != "page=failed,use=not_run" {
		t.Fatalf("states = %s", states(res))
	}
	x := res.Steps[0].Extracted
	if len(x) != 1 || x[0].OK || x[0].Error == "" {
		t.Errorf("extracted = %+v", x)
	}
	hs := hintsWith(res, "Extract of {{id}}")
	if len(hs) != 1 || !strings.Contains(hs[0].Message, "HTML, not JSON") {
		t.Errorf("hints = %+v", res.Hints)
	}
}

func TestUndefinedVar(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, `
data: {file: users.csv}
steps:
  - name: echo
    path: /echo?u={{user}}&t={{tenant}}
`)
	res := Run(context.Background(), sc, Options{Vars: map[string]string{"user": "alice"}, ColumnsKnown: true})
	if res.Steps[0].URL != srv.URL+"/echo?u=alice&t={{tenant}}" {
		t.Errorf("url = %q", res.Steps[0].URL)
	}
	hs := hintsWith(res, "{{tenant}}")
	if len(hs) != 1 || hs[0].Level != "error" || !strings.Contains(hs[0].Message, "sent literally in the URL") {
		t.Fatalf("hints = %+v", res.Hints)
	}
	if len(hintsWith(res, "{{user}}")) != 0 {
		t.Errorf("user reported: %+v", res.Hints)
	}
	// Without the CSV (unsaved test) the note is mentioned.
	res = Run(context.Background(), sc, Options{DataNote: "not saved"})
	if hs := hintsWith(res, "{{user}}"); len(hs) != 1 || !strings.Contains(hs[0].Message, "CSV data was not available") {
		t.Errorf("hints = %+v", res.Hints)
	}
	if len(hintsWith(res, "No CSV data used: not saved")) != 1 {
		t.Errorf("no data note hint: %+v", res.Hints)
	}
}

func TestAllowlistRejection(t *testing.T) {
	srv, hits := target(t)
	list, err := allowlist.Parse("allowed.test")
	if err != nil {
		t.Fatal(err)
	}
	sc := parse(t, srv.URL, "steps:\n  - {name: s, path: /items}\n")
	res := Run(context.Background(), sc, Options{Allow: list.Check})
	if res.OK || hits.Load() != 0 || states(res) != "s=not_run" {
		t.Fatalf("ok=%v hits=%d states=%s", res.OK, hits.Load(), states(res))
	}
	if hs := hintsWith(res, `Target host "127.0.0.1" is not on this server's allowlist`); len(hs) != 1 {
		t.Fatalf("hints = %+v", res.Hints)
	}

	// Absolute ws:// step URLs are vetted too, before anything is sent.
	open, _ := allowlist.Parse("127.0.0.1")
	sc = parse(t, srv.URL, "steps:\n  - {name: a, path: /items}\n  - {name: w, type: ws, path: 'ws://victim.test/x', ws: {}}\n")
	res = Run(context.Background(), sc, Options{Allow: open.Check})
	if hits.Load() != 0 || states(res) != "a=not_run,w=not_run" {
		t.Fatalf("hits=%d states=%s", hits.Load(), states(res))
	}
	if hs := hintsWith(res, `"victim.test" is not on this server's allowlist`); len(hs) != 1 {
		t.Fatalf("hints = %+v", res.Hints)
	}
}

func TestLoginShownAndSecretsRedacted(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, `
data: {file: users.csv}
auth:
  type: login
  login:
    path: /login
    headers: {Content-Type: application/json}
    body: '{"u":"{{user}}","password":"{{pass}}"}'
    extract: [{var: token, from: json, expr: access_token}]
steps:
  - name: list
    path: /items
    headers: {Cookie: "sid=abcdef123456789", X-Api-Key: "k-1234567890abcdef"}
`)
	res := Run(context.Background(), sc, Options{Vars: map[string]string{"user": "alice", "pass": "hunter2-very-secret"}, ColumnsKnown: true})
	if !res.OK || states(res) != "auth:login=passed,list=passed" {
		t.Fatalf("states = %s hints = %+v", states(res), res.Hints)
	}
	login, list := res.Steps[0], res.Steps[1]
	if login.Kind != "login" || login.Index != -1 || login.Method != "POST" || login.URL != srv.URL+"/login" {
		t.Errorf("login = %+v", login)
	}
	all, _ := json.Marshal(res)
	for _, secret := range []string{jwt, "hunter2-very-secret", "supersecretvalue", "abcdef123456789", "k-1234567890abcdef"} {
		if strings.Contains(string(all), secret) {
			t.Errorf("secret %q leaked in %s", secret, all)
		}
	}
	if !strings.Contains(login.RequestBody, `"u":"alice"`) || !strings.Contains(login.RequestBody, `"password":"hunt… (19 chars)"`) {
		t.Errorf("login body = %s", login.RequestBody)
	}
	if res.Data["pass"] == "hunter2-very-secret" || res.Data["user"] != "alice" {
		t.Errorf("data = %v", res.Data)
	}
	if len(login.Extracted) != 1 || login.Extracted[0].Value != "eyJh… ("+fmt.Sprint(len(jwt))+" chars)" {
		t.Errorf("extracted = %+v", login.Extracted)
	}
	var auth, cookie string
	for _, h := range list.RequestHeaders {
		switch h.Name {
		case "Authorization":
			auth = h.Value
		case "Cookie":
			cookie = h.Value
		}
	}
	if !strings.HasPrefix(auth, "Bearer eyJh…") || !strings.HasPrefix(cookie, "sid=") {
		t.Errorf("auth = %q cookie = %q", auth, cookie)
	}
	var setCookie string
	for _, h := range login.ResponseHeaders {
		if h.Name == "Set-Cookie" {
			setCookie = h.Value
		}
	}
	if setCookie != "sid (values hidden)" {
		t.Errorf("set-cookie = %q", setCookie)
	}
}

func TestBodyCapAndBinary(t *testing.T) {
	srv, _ := target(t)
	sc := parse(t, srv.URL, "steps:\n  - {name: big, path: /big}\n  - {name: bin, path: /bin}\n")
	res := Run(context.Background(), sc, Options{BodyLimit: 1000})
	big, bin := res.Steps[0], res.Steps[1]
	if len(big.Body) != 1000 || !big.BodyTruncated || big.Bytes != 100_000 {
		t.Errorf("big: len=%d trunc=%v bytes=%d", len(big.Body), big.BodyTruncated, big.Bytes)
	}
	if !bin.BodyBinary || bin.Body != "" || bin.Bytes != 8 {
		t.Errorf("bin = %+v", bin)
	}
	// Default limit is 8 KiB.
	res = Run(context.Background(), sc, Options{})
	if n := len(res.Steps[0].Body); n != DefaultBodyLimit {
		t.Errorf("default cap = %d", n)
	}
}

func TestWAFAndTransportHints(t *testing.T) {
	srv, _ := target(t)
	res := Run(context.Background(), parse(t, srv.URL, "steps:\n  - {name: cf, path: /cf}\n"), Options{})
	if hs := hintsWith(res, "WAF"); len(hs) != 1 || !strings.Contains(hs[0].Message, "Cloudflare") {
		t.Errorf("hints = %+v", res.Hints)
	}

	// Connection refused: a closed listener.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	res = Run(context.Background(), parse(t, url, "steps:\n  - {name: s, path: /}\n"), Options{})
	if hs := hintsWith(res, "refused"); len(hs) != 1 || res.Steps[0].Status != 0 {
		t.Errorf("hints = %+v", res.Hints)
	}

	// TLS to a plain HTTP server.
	res = Run(context.Background(), parse(t, strings.Replace(srv.URL, "http:", "https:", 1), "steps:\n  - {name: s, path: /items}\n"), Options{})
	if hs := hintsWith(res, "does not speak TLS"); len(hs) != 1 {
		t.Errorf("hints = %+v", res.Hints)
	}

	// Self-signed certificate.
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	res = Run(context.Background(), parse(t, tlsSrv.URL, "steps:\n  - {name: s, path: /}\n"), Options{})
	if hs := hintsWith(res, "insecure_skip_verify"); len(hs) != 1 {
		t.Errorf("hints = %+v", res.Hints)
	}
}

func TestTimeLimit(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	start := time.Now()
	res := Run(context.Background(), parse(t, slow.URL, "steps:\n  - {name: s, path: /}\n  - {name: t, path: /}\n"), Options{Timeout: 200 * time.Millisecond})
	if time.Since(start) > 3*time.Second {
		t.Fatal("time limit not enforced")
	}
	if res.OK || res.Steps[0].State != model.TryFailed || res.Steps[1].State != model.TryNotRun {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if len(hintsWith(res, "trial was stopped")) != 1 && len(hintsWith(res, "timeout")) == 0 {
		t.Errorf("hints = %+v", res.Hints)
	}
}

func TestStepLimit(t *testing.T) {
	srv, hits := target(t)
	var b strings.Builder
	b.WriteString("steps:\n")
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, "  - {name: s%d, path: '/items?open=1', think_time: 1s}\n", i)
	}
	start := time.Now()
	res := Run(context.Background(), parse(t, srv.URL, b.String()), Options{})
	if len(res.Steps) != DefaultMaxSteps || hits.Load() != DefaultMaxSteps || !res.OK {
		t.Fatalf("steps=%d hits=%d ok=%v", len(res.Steps), hits.Load(), res.OK)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("think times were not skipped")
	}
	if len(hintsWith(res, "first 20 of 25")) != 1 {
		t.Errorf("hints = %+v", res.Hints)
	}
}

func TestRedactHelpers(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"password":"abc","user":"bob"}`, `{"password":"***","user":"bob"}`},
		{`user=bob&password=hunter2&x=1`, `user=bob&password=***&x=1`},
		{`{"access_token": "0123456789abcdef"}`, `{"access_token": "0123… (16 chars)"}`},
		{`token {{token}} unrendered`, `token {{token}} unrendered`},
	}
	for _, c := range cases {
		if got := redactBody(c.in); got != c.want {
			t.Errorf("redactBody(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := redactURL("https://h/x?api_key=0123456789abcdef&q=1"); got != "https://h/x?api_key=0123… (16 chars)&q=1" {
		t.Errorf("redactURL = %q", got)
	}
	if got := mask("Bearer {{token}}"); got != "Bearer {{token}}" {
		t.Errorf("mask kept template = %q", got)
	}
}
