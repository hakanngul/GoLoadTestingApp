package httpx

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// TraceBodyLimit caps the response body bytes copied into a Trace.
const TraceBodyLimit = 64 << 10

// maxLocationLen caps the redirect target appended to error messages.
const maxLocationLen = 256

// Trace is a detailed record of one executed step, produced only when
// Options.Trace is set (single trial iterations, never load runs). The
// step itself runs exactly as in a load run; tracing only additionally
// buffers the body and evaluates every assertion and extract for display
// (Err is still the error of the first failing check, as in a run).
type Trace struct {
	Step   string
	Method string
	// URL is the rendered request URL ("" if the request could not be
	// built).
	URL string
	// Host is an explicit Host header ("" = taken from URL).
	Host string
	// ReqHeader holds the rendered request headers as sent (unredacted).
	ReqHeader http.Header
	// ReqBody is the rendered request body (unredacted).
	ReqBody string
	// Sent reports whether the request was sent.
	Sent bool

	Status     int
	RespHeader http.Header
	// Body holds at most TraceBodyLimit bytes of the response body.
	Body []byte
	// BodyBytes is the full body size; BodyTruncated reports that Body
	// holds only a prefix of it.
	BodyBytes     int64
	BodyTruncated bool
	// Location is the resolved redirect target of a 3xx response.
	Location string

	Checks   []CheckTrace
	Extracts []ExtractTrace

	Latency time.Duration
	Err     error // the step's Result.Err
}

// CheckTrace is the outcome of one assertion.
type CheckTrace struct {
	Desc string // e.g. "status eq 200"
	Pass bool
	Msg  string // failure reason
}

// ExtractTrace is the outcome of one extract.
type ExtractTrace struct {
	Var    string
	Source string // json, header or regex
	Expr   string
	Value  string
	OK     bool
	Msg    string // failure reason
}

// desc describes the check for reports, e.g. "status eq 200".
func (c *check) desc() string {
	switch c.typ {
	case scenario.AssertJSON:
		return fmt.Sprintf("json %s %s %s", c.path, c.op, c.raw)
	case scenario.AssertBodyContains:
		return fmt.Sprintf("body_contains %s %q", c.op, c.raw)
	}
	return fmt.Sprintf("%s %s %s", c.typ, c.op, c.raw)
}

// traceResponse fills t's response part and evaluates every check and
// extract on r for display.
func (st *step) traceResponse(t *Trace, r *response, n int64) {
	t.Sent = true
	t.Status = r.status
	t.RespHeader = r.header.Clone()
	t.BodyBytes = n
	t.Body = bytes.Clone(r.body[:min(len(r.body), TraceBodyLimit)])
	t.BodyTruncated = int64(len(t.Body)) < n
	for i := range st.checks {
		c := &st.checks[i]
		ct := CheckTrace{Desc: c.desc(), Pass: true}
		if err := c.run(r); err != nil {
			ct.Pass, ct.Msg = false, err.Error()
		}
		t.Checks = append(t.Checks, ct)
	}
	for i := range st.extracts {
		x := &st.extracts[i]
		et := ExtractTrace{Var: x.varName, Source: string(x.from), Expr: x.expr, OK: true}
		v, err := x.run(r)
		if err != nil {
			et.OK, et.Msg = false, err.Error()
		} else {
			et.Value = v
		}
		t.Extracts = append(t.Extracts, et)
	}
}

// redirectTarget returns the resolved Location of a 3xx response (capped
// for error messages), or "".
func redirectTarget(resp *http.Response) string {
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return ""
	}
	u, err := resp.Location()
	if err != nil {
		return ""
	}
	s := u.String()
	if len(s) > maxLocationLen {
		s = s[:maxLocationLen] + "..."
	}
	return s
}
