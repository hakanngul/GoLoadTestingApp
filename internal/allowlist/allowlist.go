// Package allowlist decides which targets load may be sent to (Phase 6).
//
// Sending load to a system you do not own or have written permission for
// is a legal and operational risk; the master refuses to start a run whose
// target host is not on the list.
//
// Entries (comma separated):
//
//	api.example.com        exact host
//	*.staging.example.com  any subdomain (not the apex itself)
//	10.0.0.0/8             IP range (targets given as IP literals)
//	127.0.0.1              single IP
//	*                      everything (development only)
//
// Ports and schemes are ignored; matching is case-insensitive.
package allowlist

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// ErrNotAllowed is wrapped by Check for hosts that are not on the list.
var ErrNotAllowed = errors.New("target host is not on the allowlist")

// List is an immutable, parsed allowlist. The zero value allows nothing.
type List struct {
	all      bool
	hosts    map[string]bool
	suffixes []string // ".staging.example.com"
	prefixes []netip.Prefix
	raw      []string
}

// Parse parses a comma-separated list of entries.
func Parse(s string) (*List, error) {
	l := &List{hosts: map[string]bool{}}
	for _, e := range strings.Split(s, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		l.raw = append(l.raw, e)
		switch {
		case e == "*":
			l.all = true
		case strings.HasPrefix(e, "*."):
			if len(e) < 4 || strings.Contains(e[2:], "*") {
				return nil, fmt.Errorf("allowlist: bad wildcard %q", e)
			}
			l.suffixes = append(l.suffixes, e[1:])
		case strings.Contains(e, "/"):
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("allowlist: bad CIDR %q: %w", e, err)
			}
			l.prefixes = append(l.prefixes, p.Masked())
		default:
			if strings.ContainsAny(e, "*:/ ") && net.ParseIP(e) == nil {
				return nil, fmt.Errorf("allowlist: bad entry %q (use a host name, *.domain, IP or CIDR; no ports)", e)
			}
			l.hosts[e] = true
		}
	}
	return l, nil
}

// AllowsAll reports whether the list contains "*".
func (l *List) AllowsAll() bool { return l != nil && l.all }

// Empty reports whether the list has no entries.
func (l *List) Empty() bool { return l == nil || len(l.raw) == 0 }

// String returns the entries joined by commas.
func (l *List) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(l.raw, ",")
}

// Check returns nil if rawURL's host is allowed, otherwise an error
// wrapping ErrNotAllowed.
func (l *List) Check(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: cannot determine host of %q", ErrNotAllowed, rawURL)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if l.allowsHost(host) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotAllowed, host)
}

func (l *List) allowsHost(host string) bool {
	if l == nil {
		return false
	}
	if l.all || l.hosts[host] {
		return true
	}
	for _, s := range l.suffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		for _, p := range l.prefixes {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}
