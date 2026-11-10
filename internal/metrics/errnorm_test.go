package metrics

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

func TestNormalizeErrorMessage(t *testing.T) {
	cases := []struct{ in, want string }{
		// Kept as is.
		{"", ""},
		{"status 503", "status 503"},
		{"status 404", "status 404"},
		{"err 7", "err 7"},
		{"dial tcp: connection refused", "dial tcp: connection refused"},
		{"assert status: got 500, want eq 200", "assert status: got 500, want eq 200"},
		{"read body: timeout after 1s: context deadline exceeded", "read body: timeout after 1s: context deadline exceeded"},
		{"timeout after 1.5s", "timeout after 1.5s"},
		{"tls: failed to verify certificate: x509: certificate signed by unknown authority",
			"tls: failed to verify certificate: x509: certificate signed by unknown authority"},
		{"malformed HTTP response \"HTTP/1.1 200\"", "malformed HTTP response \"HTTP/1.1 200\""},
		{"lookup api.example.com on 127.0.0.53:53: no such host", "lookup api.example.com on 127.0.0.53:53: no such host"},
		{"dial tcp 10.0.0.1:8443: connect: connection refused", "dial tcp 10.0.0.1:8443: connect: connection refused"},
		{"unavailable – ünïcödé", "unavailable – ünïcödé"},
		{"Internal-Server-Error-Occurred-Again", "Internal-Server-Error-Occurred-Again"},
		{"connection-pool-2-exhausted-for-host", "connection-pool-2-exhausted-for-host"},
		{"5sec", "5sec"},

		// The motivating example: query value.
		{`send: timeout after 1s: Get "http://127.0.0.1:18181/search?q=shoe": context deadline exceeded`,
			`send: timeout after 1s: Get "http://127.0.0.1:18181/search?q={v}": context deadline exceeded`},
		// URL path IDs, query keys kept, fragment.
		{`Get "https://api.test/v1/users/42/orders/9001?page=3&sort=asc&flag": EOF`,
			`Get "https://api.test/v1/users/{n}/orders/{n}?page={v}&sort={v}&flag": EOF`},
		{`Post "http://h:8080/items/3f2504e0-4f89-11d3-9a0c-0305e82c3301/x#frag": EOF`,
			`Post "http://h:8080/items/{uuid}/x#{v}": EOF`},
		{"GET http://h/a/5f2b1c9e8d7a6b5c4d3e2f10/b.", "GET http://h/a/{id}/b."},
		{"ws://h/chat/room-123456", "ws://h/chat/room-{n}"},
		// Redirect targets appended to 3xx failures.
		{"assert status: got 301, want eq 200 (redirects to https://www.stageletsbet.de:443/)",
			"assert status: got 301, want eq 200 (redirects to https://www.stageletsbet.de:443/)"},
		{"assert status: got 302, want eq 200 (redirects to https://sso.test/login?next=%2Fa%2F77&state=8f3a9c2e1d)",
			"assert status: got 302, want eq 200 (redirects to https://sso.test/login?next={v}&state={v})"},
		{"assert status: got 302, want eq 200 (redirects to https://h.test/session/5f2b1c9e8d7a6b5c4d3e2f10/start)",
			"assert status: got 302, want eq 200 (redirects to https://h.test/session/{id}/start)"},

		// UUIDs.
		{`assert json $.error: got "item 3F2504E0-4F89-11D3-9A0C-0305E82C3301 not found", want eq ok`,
			`assert json $.error: got "item {uuid} not found", want eq ok`},
		// Hex / base64 IDs.
		{"request deadbeefcafebabedeadbeef failed", "request {id} failed"},
		{"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U expired",
			"token {id}.{id}.{id} expired"},
		{"trace x-request-id-abcdef0123456789", "trace {id}"},
		// Numbers and durations.
		{"order 123456 not found", "order {n} not found"},
		{"user12345 locked", "user{n} locked"},
		{"offset -98765", "offset -{n}"},
		{"value 3.14159", "value {n}"},
		{"assert duration: got 1.234567s, want lt 1s", "assert duration: got {dur}, want lt 1s"},
		{"assert duration: got 812.5ms, want lt 500ms", "assert duration: got {dur}, want lt 500ms"},
		{"assert duration: got 1m2.345s, want lt 1m", "assert duration: got {dur}, want lt 1m"},
		{"took 250µs", "took 250µs"},
		{"took 1250µs", "took {dur}"},
		// Timestamps.
		{"expired at 2026-11-10T12:34:56Z", "expired at {ts}"},
		{"expired at 2026-11-10T12:34:56.123456+02:00 (now)", "expired at {ts} (now)"},
		{"at 2026-11-10 12:34:56.789 +0000 UTC: boom", "at {ts}: boom"},
		{"date 2026-11-10 only", "date {ts} only"},
		{"Date: Mon, 02 Jan 2006 15:04:05 GMT", "Date: Mon, 02 Jan {n} {ts} GMT"},
		// Ephemeral local ports.
		{"read tcp 127.0.0.1:54321->127.0.0.1:8080: read: connection reset by peer",
			"read tcp 127.0.0.1:{port}->127.0.0.1:8080: read: connection reset by peer"},
		{"write tcp [::1]:61234->[::1]:443: write: broken pipe",
			"write tcp [::1]:{port}->[::1]:443: write: broken pipe"},
		{"dial tcp 10.1.2.3:40000->10.0.0.1:443: i/o timeout", "dial tcp 10.1.2.3:{port}->10.0.0.1:443: i/o timeout"},
	}
	for _, c := range cases {
		got := NormalizeErrorMessage(c.in)
		if got != c.want {
			t.Errorf("Normalize(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
		if again := NormalizeErrorMessage(got); again != got {
			t.Errorf("not idempotent: %q -> %q", got, again)
		}
	}
}

// Distinct real errors must not collapse into one group.
func TestNormalizeKeepsDistinctErrorsApart(t *testing.T) {
	distinct := []string{
		"read tcp 127.0.0.1:50000->127.0.0.1:8080: read: connection refused",
		"read tcp 127.0.0.1:50001->127.0.0.1:8080: read: connection reset by peer",
		"read tcp 127.0.0.1:50002->127.0.0.1:9090: read: connection reset by peer",
		"status 503",
		"status 504",
		"assert status: got 500, want eq 200",
		"assert status: got 502, want eq 200",
		`Get "http://h/a/1": EOF`,
		`Get "http://h/b/1": EOF`,
		`Get "http://other/a/1": EOF`,
		"assert duration: got 1.5s, want lt 1s",
		"assert json $.ok: got false, want eq true",
		"assert json $.ok: path not found",
	}
	seen := map[string]string{}
	for _, m := range distinct {
		n := NormalizeErrorMessage(m)
		if prev, ok := seen[n]; ok {
			t.Errorf("%q and %q both normalize to %q", prev, m, n)
		}
		seen[n] = m
	}
}

func TestNormalizeNoAllocWhenUnchanged(t *testing.T) {
	msg := "dial tcp 10.0.0.1:8443: connect: connection refused"
	if a := testing.AllocsPerRun(100, func() { _ = NormalizeErrorMessage(msg) }); a != 0 {
		t.Fatalf("allocs = %v", a)
	}
}

func TestErrorGrouperNormalizesIDs(t *testing.T) {
	g := NewErrorGrouper(0, nil)
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x", i*7919, i, i, i, i*104729)
		msg := fmt.Sprintf(`Get "http://127.0.0.1:%d/items/%d?id=%s": item %s: EOF`, 18000+i%3, 100000+i, id, id)
		g.Record("get", 0, errors.New(msg))
	}
	got := g.Snapshot()
	if len(got) != 3 { // three distinct target ports: the host:port is kept
		t.Fatalf("groups = %d: %+v", len(got), got)
	}
	g = NewErrorGrouper(0, nil)
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("%08x-%04x-4%03x-a%03x-%012x", i*7919, i, i, i, i*104729)
		g.Record("get", 404, fmt.Errorf(`assert json $.error: got "item %s not found (ref %d)", want eq ok`, id, 1e6+i))
	}
	got = g.Snapshot()
	if len(got) != 1 || got[0].Count != 500 {
		t.Fatalf("groups = %+v", got)
	}
	if got[0].Message != `assert json $.error: got "item {uuid} not found (ref {n})", want eq ok` {
		t.Fatalf("message = %q", got[0].Message)
	}
	if want := `assert json $.error: got "item 00000000-0000-4000-a000-000000000000 not found (ref 1000000)", want eq ok`; got[0].Sample != want {
		t.Fatalf("sample = %q", got[0].Sample)
	}
}

var benchMessages = []string{
	`send: timeout after 1s: Get "http://127.0.0.1:18181/search?q=shoe": context deadline exceeded`,
	"read tcp 127.0.0.1:54321->127.0.0.1:8080: read: connection reset by peer",
	`assert json $.error: got "item 3f2504e0-4f89-11d3-9a0c-0305e82c3301 not found", want eq ok`,
	"status 503",
	"dial tcp 10.0.0.1:8443: connect: connection refused",
}

func BenchmarkNormalizeErrorMessage(b *testing.B) {
	for _, m := range benchMessages {
		name := m
		if len(name) > 24 {
			name = name[:24]
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = NormalizeErrorMessage(m)
			}
		})
	}
}

// BenchmarkErrorGrouperRecord measures the whole failure path (normalize,
// lock, map lookup) for an existing group.
func BenchmarkErrorGrouperRecord(b *testing.B) {
	g := NewErrorGrouper(0, nil)
	errs := make([]error, len(benchMessages))
	for i, m := range benchMessages {
		errs[i] = errors.New(m)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			g.Record("s", 0, errs[i%len(errs)])
			i++
		}
	})
}

func TestErrorGrouperMergeKeepsEarliestSample(t *testing.T) {
	g := NewErrorGrouper(0, nil)
	key := "item {uuid} not found"
	g.Merge([]model.ErrorGroup{{Step: "s", Status: 404, Message: key, Sample: "item B not found",
		Count: 2, First: t0.Add(2 * time.Second), Last: t0.Add(3 * time.Second)}})
	g.Merge([]model.ErrorGroup{{Step: "s", Status: 404, Message: key, Sample: "item A not found",
		Count: 1, First: t0.Add(time.Second), Last: t0.Add(time.Second)}})
	g.Merge([]model.ErrorGroup{{Step: "s", Status: 404, Message: key, Sample: "item C not found",
		Count: 4, First: t0.Add(5 * time.Second), Last: t0.Add(6 * time.Second)}})
	// A worker that predates samples sends the raw message only: it is
	// normalized here and kept as the sample when it is the earliest.
	raw := "item 3f2504e0-4f89-11d3-9a0c-0305e82c3301 not found"
	g.Merge([]model.ErrorGroup{{Step: "s", Status: 404, Message: raw, Count: 1, First: t0, Last: t0}})
	got := g.Snapshot()
	if len(got) != 1 || got[0].Count != 8 || got[0].Message != "item {uuid} not found" {
		t.Fatalf("merged = %+v", got)
	}
	if got[0].Sample != raw || !got[0].First.Equal(t0) || !got[0].Last.Equal(t0.Add(6*time.Second)) {
		t.Fatalf("merged = %+v", got[0])
	}

	// Overflow groups merge into the overflow group and keep their sample.
	o := NewErrorGrouper(1, nil)
	o.Record("a", 500, errors.New("first"))
	o.Record("b", 500, errors.New("order 123456 failed"))
	snap := o.Snapshot()
	if len(snap) != 2 || snap[1].Message != model.ErrorGroupOther || snap[1].Sample != "order 123456 failed" {
		t.Fatalf("overflow = %+v", snap)
	}
	g.Merge(snap)
	if got := g.Snapshot(); got[len(got)-1].Message != model.ErrorGroupOther || got[len(got)-1].Sample != "order 123456 failed" {
		t.Fatalf("merged overflow = %+v", got)
	}
}

func TestErrorGrouperSampleIsFirstRawMessage(t *testing.T) {
	g := NewErrorGrouper(0, fixedClock(t0))
	g.Record("s", 0, errors.New("order 111111 failed"))
	g.Record("s", 0, errors.New("order 222222 failed"))
	got := g.Snapshot()
	if len(got) != 1 || got[0].Message != "order {n} failed" || got[0].Sample != "order 111111 failed" || got[0].Count != 2 {
		t.Fatalf("groups = %+v", got)
	}
	// Long messages: key and sample both cut to ErrorMessageMaxRunes.
	long := strings.Repeat("é", 400) + " id 1234567"
	g.Record("long", 0, errors.New(long))
	for _, e := range g.Snapshot() {
		if e.Step == "long" && (utf8.RuneCountInString(e.Sample) != ErrorMessageMaxRunes || utf8.RuneCountInString(e.Message) != ErrorMessageMaxRunes) {
			t.Fatalf("long group = %d/%d runes", utf8.RuneCountInString(e.Message), utf8.RuneCountInString(e.Sample))
		}
	}
}
