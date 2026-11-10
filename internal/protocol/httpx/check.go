package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/protocol/jsonpath"
	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// op is a comparison operator from scenario.Assert.Op.
type op uint8

const (
	opEq op = iota
	opNe
	opLt
	opLte
	opGt
	opGte
	opContains
)

var opNames = [...]string{"eq", "ne", "lt", "lte", "gt", "gte", "contains"}

func (o op) String() string { return opNames[o] }

func parseOp(s string) (op, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "eq", "==":
		return opEq, nil
	case "ne", "!=":
		return opNe, nil
	case "lt", "<":
		return opLt, nil
	case "lte", "<=":
		return opLte, nil
	case "gt", ">":
		return opGt, nil
	case "gte", ">=":
		return opGte, nil
	case "contains":
		return opContains, nil
	}
	return 0, fmt.Errorf("unknown op %q", s)
}

func (o op) numeric() bool { return o != opContains }

// cmpFloat applies a numeric operator.
func cmpFloat(o op, got, want float64) bool {
	switch o {
	case opEq:
		return got == want
	case opNe:
		return got != want
	case opLt:
		return got < want
	case opLte:
		return got <= want
	case opGt:
		return got > want
	case opGte:
		return got >= want
	}
	return false
}

// response is what checks and extracts see. json is decoded lazily, at
// most once per response.
type response struct {
	status  int
	header  http.Header
	body    []byte // possibly truncated to maxBodyRead
	trunc   bool
	latency time.Duration

	jsonDone bool
	jsonDoc  any
	jsonErr  error
}

func (r *response) json() (any, error) {
	if !r.jsonDone {
		r.jsonDone = true
		if r.trunc {
			r.jsonErr = fmt.Errorf("body exceeds %d bytes, not parsed", maxBodyRead)
		} else {
			dec := json.NewDecoder(bytes.NewReader(r.body))
			dec.UseNumber() // keep large integer IDs exact
			if err := dec.Decode(&r.jsonDoc); err != nil {
				r.jsonErr = fmt.Errorf("invalid JSON body: %w", err)
			}
		}
	}
	return r.jsonDoc, r.jsonErr
}

// check is a compiled scenario.Assert.
type check struct {
	typ   scenario.AssertType
	op    op
	raw   string        // Value as written, for messages
	num   float64       // status / numeric json value
	isNum bool          // json: Value parsed as a number
	dur   time.Duration // duration assert
	path  jsonpath.Path // json assert
}

func compileCheck(a scenario.Assert) (check, error) {
	o, err := parseOp(a.Op)
	if err != nil {
		return check{}, err
	}
	c := check{typ: a.Type, op: o, raw: a.Value}
	switch a.Type {
	case scenario.AssertStatus:
		if !o.numeric() {
			return c, fmt.Errorf("op %s not valid for status", o)
		}
		n, err := strconv.Atoi(strings.TrimSpace(a.Value))
		if err != nil {
			return c, fmt.Errorf("status value %q: not an integer", a.Value)
		}
		c.num = float64(n)
	case scenario.AssertDuration:
		if !o.numeric() {
			return c, fmt.Errorf("op %s not valid for duration", o)
		}
		d, err := time.ParseDuration(strings.TrimSpace(a.Value))
		if err != nil {
			return c, fmt.Errorf("duration value %q: %w", a.Value, err)
		}
		c.dur = d
	case scenario.AssertJSON:
		p, err := jsonpath.Compile(a.Expr)
		if err != nil {
			return c, err
		}
		c.path = p
		if f, err := strconv.ParseFloat(strings.TrimSpace(a.Value), 64); err == nil {
			c.num, c.isNum = f, true
		}
		if (o == opLt || o == opLte || o == opGt || o == opGte) && !c.isNum {
			return c, fmt.Errorf("json op %s needs a numeric value, got %q", o, a.Value)
		}
	case scenario.AssertBodyContains:
		// eq/contains => must contain; ne => must not contain.
		switch o {
		case opEq, opContains:
			c.op = opContains
		case opNe:
		default:
			return c, fmt.Errorf("op %s not valid for body_contains (use contains or ne)", o)
		}
		if a.Value == "" {
			return c, fmt.Errorf("body_contains needs a value")
		}
	default:
		return c, fmt.Errorf("unknown assert type %q", a.Type)
	}
	return c, nil
}

func (c *check) needsBody() bool {
	return c.typ == scenario.AssertJSON || c.typ == scenario.AssertBodyContains
}

// run evaluates the check; the error text is the user-facing reason.
func (c *check) run(r *response) error {
	switch c.typ {
	case scenario.AssertStatus:
		if !cmpFloat(c.op, float64(r.status), c.num) {
			return fmt.Errorf("assert status: got %d, want %s %s", r.status, c.op, c.raw)
		}
	case scenario.AssertDuration:
		if !cmpFloat(c.op, float64(r.latency), float64(c.dur)) {
			return fmt.Errorf("assert duration: got %s, want %s %s",
				r.latency.Round(time.Microsecond), c.op, c.raw)
		}
	case scenario.AssertBodyContains:
		has := bytes.Contains(r.body, []byte(c.raw))
		if c.op == opContains && !has {
			return fmt.Errorf("assert body_contains: body does not contain %q", c.raw)
		}
		if c.op == opNe && has {
			return fmt.Errorf("assert body_contains: body contains %q, want ne", c.raw)
		}
	case scenario.AssertJSON:
		doc, err := r.json()
		if err != nil {
			return fmt.Errorf("assert json %s: %w", c.path, err)
		}
		v, ok := c.path.Get(doc)
		if !ok {
			return fmt.Errorf("assert json: path %s not found", c.path)
		}
		if !c.compareJSON(v) {
			return fmt.Errorf("assert json %s: got %s, want %s %s",
				c.path, quoteJSON(v), c.op, c.raw)
		}
	}
	return nil
}

// compareJSON compares a decoded value with the expected Value. Numbers
// compare numerically when Value is numeric; everything else compares by
// its jsonpath.String form.
func (c *check) compareJSON(v any) bool {
	if c.isNum && c.op != opContains {
		if f, ok := toFloat(v); ok {
			return cmpFloat(c.op, f, c.num)
		}
		if c.op != opEq && c.op != opNe {
			return false // ordering against a non-number never passes
		}
	}
	s := jsonpath.String(v)
	switch c.op {
	case opEq:
		return s == c.raw
	case opNe:
		return s != c.raw
	case opContains:
		return strings.Contains(s, c.raw)
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

func quoteJSON(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return jsonpath.String(v)
}

// extractor is a compiled scenario.Extract.
type extractor struct {
	varName string
	from    scenario.ExtractSource
	expr    string
	header  string         // canonical header key
	path    jsonpath.Path  // json
	re      *regexp.Regexp // regex
}

func compileExtract(e scenario.Extract) (extractor, error) {
	x := extractor{varName: e.Var, from: e.From, expr: e.Expr}
	if e.Var == "" {
		return x, fmt.Errorf("extract: var is required")
	}
	switch e.From {
	case scenario.FromJSON:
		p, err := jsonpath.Compile(e.Expr)
		if err != nil {
			return x, fmt.Errorf("extract %s: %w", e.Var, err)
		}
		x.path = p
	case scenario.FromHeader:
		if e.Expr == "" {
			return x, fmt.Errorf("extract %s: header name is required", e.Var)
		}
		x.header = http.CanonicalHeaderKey(e.Expr)
	case scenario.FromRegex:
		re, err := regexp.Compile(e.Expr)
		if err != nil {
			return x, fmt.Errorf("extract %s: %w", e.Var, err)
		}
		x.re = re
	default:
		return x, fmt.Errorf("extract %s: unknown source %q", e.Var, e.From)
	}
	return x, nil
}

func (x *extractor) needsBody() bool { return x.from != scenario.FromHeader }

// run returns the extracted value as a string.
func (x *extractor) run(r *response) (string, error) {
	switch x.from {
	case scenario.FromHeader:
		vs := r.header[x.header]
		if len(vs) == 0 {
			return "", fmt.Errorf("extract %s: header %s not found", x.varName, x.expr)
		}
		return vs[0], nil
	case scenario.FromRegex:
		m := x.re.FindSubmatch(r.body)
		if m == nil {
			return "", fmt.Errorf("extract %s: regex %s did not match", x.varName, x.expr)
		}
		if len(m) > 1 {
			return string(m[1]), nil // first capture group
		}
		return string(m[0]), nil // no group: whole match
	default: // FromJSON
		doc, err := r.json()
		if err != nil {
			return "", fmt.Errorf("extract %s: %w", x.varName, err)
		}
		v, ok := x.path.Get(doc)
		if !ok {
			return "", fmt.Errorf("extract %s: json path %s not found", x.varName, x.path)
		}
		return jsonpath.String(v), nil
	}
}
