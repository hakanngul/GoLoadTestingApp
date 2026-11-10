package trial

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"syscall"

	"github.com/hakanngul/GoLoadTestingApp/internal/allowlist"
	"github.com/hakanngul/GoLoadTestingApp/internal/model"
	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/httpx"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// hints diagnoses a finished trial of sc (the scenario as written).
func hints(sc *scenario.Scenario, entries []*entry, opts Options) []model.TryHint {
	var out []model.TryHint
	add := func(h model.TryHint) { out = append(out, h) }

	// Unreplaced {{vars}} seen in what was actually sent.
	sentVars := map[[2]string]bool{}
	for _, e := range entries {
		t := e.trace
		if t == nil {
			continue
		}
		where := map[string][]string{}
		for _, v := range scenario.TemplateVars(t.URL) {
			where[v] = append(where[v], "URL")
		}
		for _, v := range scenario.TemplateVars(t.Host) {
			where[v] = append(where[v], "Host header")
		}
		for k, vs := range t.ReqHeader {
			for _, v := range scenario.TemplateVars(strings.Join(vs, " ")) {
				where[v] = append(where[v], k+" header")
			}
		}
		for _, v := range scenario.TemplateVars(t.ReqBody) {
			where[v] = append(where[v], "body")
		}
		for _, v := range orderedKeys(where) {
			sentVars[[2]string{e.Name, v}] = true
			msg := fmt.Sprintf("{{%s}} was sent literally in the %s: no CSV column, extract of an earlier step or auth variable defines it.",
				v, strings.Join(dedupe(where[v]), ", "))
			if opts.DataNote != "" {
				msg += " The CSV data was not available for this test request (see above)."
			}
			add(model.TryHint{Level: "error", Step: e.Name, Message: msg})
		}
	}
	// Static check (covers steps that did not run and ws steps).
	for _, is := range sc.CheckVars(slicesKeys(opts.Vars), opts.ColumnsKnown) {
		step := "auth"
		switch {
		case is.Step >= 0 && is.Step < len(sc.Steps):
			step = sc.Steps[is.Step].Name
		case strings.HasPrefix(is.Path, "auth.login"):
			step = scenario.AuthLoginStep
		case strings.HasPrefix(is.Path, "target."):
			step = ""
		}
		if sentVars[[2]string{step, is.Var}] {
			continue
		}
		add(model.TryHint{Level: "warning", Step: step, Message: is.Path + ": " + is.Msg})
	}

	for _, e := range entries {
		for _, h := range stepHints(sc, e) {
			add(h)
		}
	}
	return out
}

// stepHints diagnoses one reported step.
func stepHints(sc *scenario.Scenario, e *entry) []model.TryHint {
	if e.State == model.TryNotRun {
		return nil
	}
	var out []model.TryHint
	level := "error"
	if e.State == model.TryPassed {
		level = "warning"
	}
	t := e.trace
	hdr := http.Header{}
	var body string
	if t != nil && t.RespHeader != nil {
		hdr = t.RespHeader
		body = string(t.Body[:min(len(t.Body), 16<<10)])
	}

	// Transport-level failures (no HTTP response).
	if e.Status == 0 && e.err != nil {
		if t != nil && len(t.Hops) > 0 {
			out = followedHints(sc, e) // the failing request is a redirect hop
		}
		return append(out, transportHint(e))
	}

	if t != nil && len(t.Hops) > 0 {
		out = append(out, followedHints(sc, e)...)
	}
	redirected := false
	switch s := e.Status; {
	case s >= 300 && s <= 399 && e.Location != "":
		redirected = true
		out = append(out, redirectHint(sc, e, level))
	case waf(s, hdr, body) != "":
		out = append(out, model.TryHint{Level: "error", Step: e.Name, Message: fmt.Sprintf(
			"The response (%d) looks like a block by a WAF / bot protection (%s). A load test would be blocked the same way: allow the load generator's IP address and User-Agent (e.g. a WAF skip rule for the test), or test an origin endpoint that bypasses the CDN.",
			s, waf(s, hdr, body))})
	case s == http.StatusTooManyRequests:
		msg := "The target rate-limits requests (429 Too Many Requests); a load test would mostly measure the rate limiter. Ask for the limit to be raised or for the load generator to be exempted."
		if ra := hdr.Get("Retry-After"); ra != "" {
			msg += " Retry-After: " + ra + "."
		}
		out = append(out, model.TryHint{Level: level, Step: e.Name, Message: msg})
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		var msg string
		switch {
		case e.Kind == "login":
			msg = fmt.Sprintf("The login request was rejected with %d: check the credentials, the login path and the body format.", s)
		case sc.Auth != nil:
			msg = fmt.Sprintf("The request was rejected with %d although auth is configured: check the auth header (header/value) and that the token is valid.", s)
		default:
			msg = fmt.Sprintf("The request was rejected with %d: the endpoint needs authentication. Configure auth (bearer, basic or login) or add the required header.", s)
		}
		out = append(out, model.TryHint{Level: level, Step: e.Name, Message: msg})
	case s == http.StatusNotFound:
		out = append(out, model.TryHint{Level: level, Step: e.Name, Message: "Not found (404): check the path and target.base_url."})
	case s == http.StatusMethodNotAllowed:
		out = append(out, model.TryHint{Level: level, Step: e.Name, Message: fmt.Sprintf("Method %s is not allowed for this URL (405).", e.Method)})
	case s >= 500:
		out = append(out, model.TryHint{Level: level, Step: e.Name, Message: fmt.Sprintf("The server answered %d: it (or a proxy in front of it) failed. Check the response body and the target's logs.", s)})
	case s >= 400 && e.State == model.TryFailed:
		out = append(out, model.TryHint{Level: "error", Step: e.Name, Message: fmt.Sprintf("The server answered %d; the response body usually says why (often a missing header or an invalid request body).", s)})
	}

	if t == nil {
		if e.Kind == "ws" && e.err != nil && len(out) == 0 {
			out = append(out, model.TryHint{Level: "error", Step: e.Name, Message: "WebSocket step failed: " + e.Error})
		}
		return out
	}
	for _, c := range t.Checks {
		if c.Pass || redirected { // the redirect hint explains it
			continue
		}
		out = append(out, model.TryHint{Level: "error", Step: e.Name,
			Message: fmt.Sprintf("Assertion %q failed: %s.", c.Desc, c.Msg)})
	}
	for _, x := range t.Extracts {
		if x.OK {
			continue
		}
		msg := fmt.Sprintf("Extract of {{%s}} (%s %s) failed: %s.", x.Var, x.Source, x.Expr, x.Msg)
		ct := strings.ToLower(hdr.Get("Content-Type"))
		if x.Source == "json" && strings.Contains(ct, "html") {
			msg += " The response is HTML, not JSON (an error or login page?)."
		}
		msg += " Later steps that use it would send it literally."
		out = append(out, model.TryHint{Level: "error", Step: e.Name, Message: msg})
	}
	return out
}

// redirectHint explains a final 3xx (a redirect that was not followed)
// and, when possible, proposes a fix.
func redirectHint(sc *scenario.Scenario, e *entry, level string) model.TryHint {
	h := model.TryHint{Level: level, Step: e.Name}
	var blocked *httpx.RedirectError
	switch {
	case errors.As(e.err, &blocked):
		h.Level = "error"
		h.Message = fmt.Sprintf("The response is a %d redirect to %s, which GoLoad did not follow: %s.", e.Status, e.Location, blocked.Reason)
		if errors.Is(e.err, allowlist.ErrNotAllowed) {
			h.Message += fmt.Sprintf(" Host %q is not on this server's allowlist; ask the operator to add it to -allow-targets (GOLOAD_ALLOWED_TARGETS) if you own it or have permission to test it.", hostOf(e.Location))
		}
		return h
	case strings.HasPrefix(e.Error, "redirect loop"):
		h.Level = "error"
		if !sc.Target.CookiesEnabled() {
			h.Message = fmt.Sprintf("The server keeps answering with a %d redirect to a URL that was already requested (%s). It most likely waits for a cookie it set to come back, but cookies are disabled: enable cookies by removing `cookies: false` from target (GoLoad then keeps cookies per virtual user, like a browser).",
				e.Status, e.Location)
		} else {
			h.Message = fmt.Sprintf("The server redirects in a loop (%d to %s, a request that was already made with the same cookies), so the page never loads. Check the URL and whether the site needs a login or a specific header before it stops redirecting.",
				e.Status, e.Location)
		}
		return h
	case strings.HasPrefix(e.Error, "too many redirects"):
		h.Level = "error"
		h.Message = fmt.Sprintf("The redirect chain did not end within target.max_redirects hops (last: %d to %s). Check the URL; raise target.max_redirects only if such a long chain is expected.", e.Status, e.Location)
		return h
	}

	// follow_redirects: false. A passing step asserts the 3xx on purpose.
	if e.State == model.TryPassed {
		h.Level = "info"
	}
	a := adviseRedirect(sc, e, e.trace.URL, e.Location)
	h.Message = fmt.Sprintf("The response is a %d redirect to %s. follow_redirects is false for this step, so a run measures this %d instead of the page behind it.",
		e.Status, e.Location, e.Status) + a.msg
	h.Fix = a.fix
	return h
}

// followedHints comments on the redirects a step followed before its
// final response. Only the first hop is diagnosed: it is the one a
// scenario change can avoid.
func followedHints(sc *scenario.Scenario, e *entry) []model.TryHint {
	hops := e.trace.Hops
	first := hops[0]
	a := adviseRedirect(sc, e, first.URL, first.Location)
	h := model.TryHint{Level: "info", Step: e.Name}
	total := ""
	if len(hops) > 1 {
		total = fmt.Sprintf(" (%d redirects in total)", len(hops))
	}
	switch {
	case !a.parsed:
		h.Message = fmt.Sprintf("The request was redirected (%d) to %s and GoLoad followed it%s; the step's latency covers the whole chain.", first.Status, first.Location, total)
	case a.sameOrigin && a.samePath:
		// The cookie check of the reported site: 302 to itself + cookie.
		if len(first.CookiesSet) > 0 {
			h.Message = fmt.Sprintf("The first request was answered with a %d redirect to the same URL and a cookie (%s); with the cookie sent back the server answered %d. In a run each virtual user passes this cookie check once (closed model) or on every iteration (open model: each iteration is a new visitor).",
				first.Status, strings.Join(first.CookiesSet, ", "), e.Status)
		} else {
			h.Message = fmt.Sprintf("The first request was answered with a %d redirect to the same URL, which GoLoad followed%s.", first.Status, total)
		}
	case !a.sameOrigin:
		// An extra hop on every request is rarely what one wants to measure.
		h.Level = "warning"
		h.Message = fmt.Sprintf("The request was redirected (%d) to %s and GoLoad followed it%s. Every request of a run would make this extra hop and measure both requests.",
			first.Status, first.Location, total) + a.msg
		h.Fix = a.fix
	default: // same origin, another path
		h.Message = fmt.Sprintf("The request was redirected (%d) to %s and GoLoad followed it%s; the step's latency covers the whole chain.", first.Status, first.Location, total)
		switch {
		case a.loginish:
			h.Level = "warning"
			h.Message += a.msg
		case first.Method == http.MethodGet || first.Method == http.MethodHead:
			h.Message += a.msg
			h.Fix = a.fix
		}
	}
	return []model.TryHint{h}
}

// redirAdvice is what adviseRedirect found out about one redirect.
type redirAdvice struct {
	parsed     bool // both URLs parsed; the other fields are valid
	sameOrigin bool
	samePath   bool
	loginish   bool
	msg        string // advice to append to a hint (starts with a space)
	fix        *model.TryFix
}

// adviseRedirect compares a request URL with the redirect target it was
// answered with and proposes how the scenario could avoid the redirect.
func adviseRedirect(sc *scenario.Scenario, e *entry, from, location string) redirAdvice {
	var a redirAdvice
	loc, err1 := url.Parse(location)
	req, err2 := url.Parse(from)
	if err1 != nil || err2 != nil {
		return a
	}
	a.parsed = true
	a.sameOrigin = strings.EqualFold(loc.Scheme, req.Scheme) && strings.EqualFold(hostPort(loc), hostPort(req))
	a.samePath = loc.EscapedPath() == req.EscapedPath() && loc.RawQuery == req.RawQuery
	if (loc.EscapedPath() == "" || loc.EscapedPath() == "/") && (req.EscapedPath() == "" || req.EscapedPath() == "/") && loc.RawQuery == req.RawQuery {
		a.samePath = true
	}
	path := stepPath(sc, e.Index)
	absStep := isAbs(path)
	lp := strings.ToLower(loc.Path)
	a.loginish = strings.Contains(lp, "login") || strings.Contains(lp, "signin") || strings.Contains(lp, "sign-in") || strings.Contains(lp, "/auth")

	switch {
	case a.samePath && !a.sameOrigin:
		newOrigin := loc.Scheme + "://" + hostPort(loc)
		if absStep {
			a.msg = " The URL moved to " + newOrigin + "; use the new URL."
			if !strings.Contains(path, "{{") {
				a.fix = &model.TryFix{Kind: "step_path", Step: e.Index, Value: location, Label: "Use " + location}
			}
			break
		}
		base, err := url.Parse(sc.Target.BaseURL)
		if err != nil {
			break
		}
		newBase := newOrigin + strings.TrimRight(base.EscapedPath(), "/")
		a.msg = " The site moved to another host or scheme: set target.base_url to " + newBase + "."
		a.fix = &model.TryFix{Kind: "base_url", Step: -1, Value: newBase, Label: "Set base_url to " + newBase}
	case a.sameOrigin && !a.samePath:
		if a.loginish {
			a.msg = " It points to a login page: the request is probably missing authentication."
			break
		}
		a.msg = " Use the final path directly."
		if absStep || path == "" || strings.Contains(path, "{{") {
			break
		}
		p := loc.EscapedPath()
		if loc.RawQuery != "" {
			p += "?" + loc.RawQuery
		}
		if base, err := url.Parse(sc.Target.BaseURL); err == nil {
			if pre := strings.TrimRight(base.EscapedPath(), "/"); pre != "" {
				if !strings.HasPrefix(p, pre+"/") && p != pre {
					break // outside the base path; cannot express relatively
				}
				p = strings.TrimPrefix(p, pre)
			}
		}
		if p == "" {
			p = "/"
		}
		a.fix = &model.TryFix{Kind: "step_path", Step: e.Index, Value: p, Label: "Use path " + p}
	case !a.sameOrigin:
		if a.loginish {
			a.msg = " It points to a login page on another host: the request is probably missing authentication (SSO?)."
		} else {
			a.msg = " It points to a different host and path: check that the URL is right."
		}
	}
	return a
}

// stepPath returns the path of steps[i] (auth.login for -1).
func stepPath(sc *scenario.Scenario, i int) string {
	if i < 0 {
		if sc.Auth != nil && sc.Auth.Login != nil {
			return sc.Auth.Login.Path
		}
		return ""
	}
	if i < len(sc.Steps) {
		return sc.Steps[i].Path
	}
	return ""
}

func isAbs(p string) bool {
	_, _, ok := strings.Cut(p, "://")
	return ok && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "{{")
}

// hostPort is the lower-case host with the scheme's default port removed.
func hostPort(u *url.URL) string {
	h, p := strings.ToLower(u.Hostname()), u.Port()
	switch {
	case p == "",
		p == "443" && (u.Scheme == "https" || u.Scheme == "wss"),
		p == "80" && (u.Scheme == "http" || u.Scheme == "ws"):
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return net.JoinHostPort(h, p)
}

// wafMarkers are body fragments of common WAF / bot-protection pages.
var wafMarkers = []struct{ marker, name string }{
	{"Just a moment...", "Cloudflare challenge"},
	{"Attention Required! | Cloudflare", "Cloudflare"},
	{"cf-chl", "Cloudflare challenge"},
	{"challenge-platform", "Cloudflare challenge"},
	{"Request unsuccessful. Incapsula", "Imperva Incapsula"},
	{"_Incapsula_Resource", "Imperva Incapsula"},
	{"The requested URL was rejected", "F5 ASM"},
	{"Sucuri WebSite Firewall", "Sucuri"},
	{"Reference&#32;&#35;", "Akamai"},
	{"AkamaiGHost", "Akamai"},
	{"Access Denied", "access denied page"},
	{"captcha", "CAPTCHA"},
}

// waf names the bot protection a blocked response comes from, or "".
func waf(status int, h http.Header, body string) string {
	switch status {
	case 401, 403, 406, 429, 503:
	default:
		return ""
	}
	server := strings.ToLower(h.Get("Server"))
	switch {
	case h.Get("Cf-Mitigated") != "":
		return "Cloudflare, cf-mitigated: " + h.Get("Cf-Mitigated")
	case strings.Contains(server, "cloudflare") || h.Get("Cf-Ray") != "":
		if status == 429 && !strings.Contains(body, "cf-") {
			return "" // plain rate limit by the origin behind Cloudflare
		}
		return "Cloudflare"
	case strings.Contains(server, "akamai"):
		return "Akamai"
	case h.Get("X-Iinfo") != "" || strings.Contains(server, "incapsula"):
		return "Imperva Incapsula"
	case h.Get("X-Sucuri-Id") != "" || strings.Contains(server, "sucuri"):
		return "Sucuri"
	}
	if status == 429 {
		return ""
	}
	for _, m := range wafMarkers {
		if strings.Contains(body, m.marker) {
			return m.name
		}
	}
	return ""
}

// transportHint explains a request that got no HTTP response.
func transportHint(e *entry) model.TryHint {
	h := model.TryHint{Level: "error", Step: e.Name}
	err := e.err
	host := hostOf(e.URL)
	if e.FinalURL != "" {
		host = hostOf(e.FinalURL) // the request that failed is a redirect hop
	}
	var (
		dnsErr  *net.DNSError
		uaErr   x509.UnknownAuthorityError
		hostErr x509.HostnameError
		certErr x509.CertificateInvalidError
		recErr  tls.RecordHeaderError
		netErr  net.Error
	)
	msg := err.Error()
	switch {
	case errors.Is(err, allowlist.ErrNotAllowed):
		return notAllowedHint(e.Name, e.URL)
	case errors.As(err, &dnsErr):
		h.Message = fmt.Sprintf("DNS lookup of %q failed (%s): the host name does not resolve from the load generator. Check the spelling, internal DNS or VPN.", dnsErr.Name, dnsErr.Err)
	case errors.Is(err, syscall.ECONNREFUSED):
		h.Message = fmt.Sprintf("Connection to %s was refused: nothing listens on that port (wrong port or http/https, or the service is down).", host)
	case errors.As(err, &uaErr), errors.As(err, &certErr):
		h.Message = fmt.Sprintf("The TLS certificate of %s is not trusted (%s). For a test environment with a self-signed certificate set target.insecure_skip_verify: true.", host, msg)
	case errors.As(err, &hostErr):
		h.Message = fmt.Sprintf("The TLS certificate does not match %s (%s): use the host name the certificate was issued for.", host, hostErr.Error())
	case errors.As(err, &recErr), strings.Contains(msg, "HTTP response to HTTPS client"):
		h.Message = fmt.Sprintf("TLS handshake with %s failed: the server does not speak TLS on this port. Use http:// instead of https://, or the right port.", host)
	case strings.Contains(msg, "timeout after"), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		h.Message = fmt.Sprintf("No response from %s within the timeout (target.timeout). The host may be unreachable from the load generator (firewall) or very slow.", host)
	case strings.Contains(msg, "proxyconnect"):
		h.Message = "The HTTP proxy configured on the load generator (HTTPS_PROXY / HTTP_PROXY) refused the connection: " + msg
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		h.Message = fmt.Sprintf("The connection to %s was closed before a response arrived (server, proxy or firewall reset it).", host)
	case strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:"):
		h.Message = fmt.Sprintf("TLS handshake with %s failed: %s.", host, msg)
	default:
		h.Message = "The request failed without a response: " + msg
	}
	return h
}

// notAllowedHint explains an allowlist rejection.
func notAllowedHint(step, rawURL string) model.TryHint {
	return model.TryHint{Level: "error", Step: step, Message: fmt.Sprintf(
		"Target host %q is not on this server's allowlist, so GoLoad does not send requests to it. Ask the operator to add it to -allow-targets (GOLOAD_ALLOWED_TARGETS) if you own the target or have permission to test it.",
		hostOf(rawURL))}
}

func orderedKeys(m map[string][]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func slicesKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0:0]
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	slices.Sort(out)
	return out
}
