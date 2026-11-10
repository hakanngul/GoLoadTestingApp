package metrics

import "strings"

// NormalizeErrorMessage replaces request-specific parts of an error
// message with placeholders so that failures differing only in an ID,
// a URL value, a timestamp or an ephemeral port fall into one error group.
// It is a hand-written scanner (no regexps): it returns s itself, without
// allocating, when nothing is replaced, and allocates once otherwise. It is
// idempotent. The rules, applied to whole tokens (never inside a word):
//
//   - UUIDs (8-4-4-4-12 hex) become {uuid}.
//   - RFC 3339 / "2006-01-02 15:04:05[.999] [+0000] [UTC]" timestamps and
//     dates become {ts}; clock times hh:mm:ss[.fff] become {ts}.
//   - Tokens of 16+ characters from [A-Za-z0-9_+=-] that are all hex, or
//     contain an alphanumeric run of 8+ characters mixing letters and digits
//     (hex IDs, base64 tokens, JWT parts) become {id}.
//   - Numbers with 4+ digits (fraction included) become {n}; durations with
//     4+ digits ("got 1.234567s") become {dur}. Shorter numbers such as
//     "status 503", "after 1s" or "1.5s" are kept. Inside other tokens,
//     runs of 4+ digits become {n} ("user12345" -> "user{n}").
//   - In URLs (scheme://), the host and port are kept; path segments that
//     are all digits become {n} (any length), other segments get the token
//     rules; query values become {v} (keys kept).
//   - A port followed by "->" (the local, ephemeral side of a Go net error
//     such as "read tcp 127.0.0.1:54321->10.0.0.1:443") becomes {port};
//     other host:port ports are kept as they are.
//
// Words are never changed, so different failures ("connection refused" vs
// "connection reset by peer", "status 503" vs "status 504") stay apart.
func NormalizeErrorMessage(s string) string {
	n := normalizer{s: s}
	n.scan(0, len(s))
	if !n.changed {
		return s
	}
	n.b.WriteString(s[n.last:])
	return n.b.String()
}

type normalizer struct {
	s       string
	b       strings.Builder // used once something is replaced
	changed bool
	last    int // s[:last] has been copied (or replaced) into b
}

// repl replaces s[i:j] by r. Calls must come in increasing, disjoint order.
func (n *normalizer) repl(i, j int, r string) {
	if !n.changed {
		n.changed = true
		n.b.Grow(len(n.s) + 8)
	}
	n.b.WriteString(n.s[n.last:i])
	n.b.WriteString(r)
	n.last = j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }
func isHex(c byte) bool   { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }

// isTok reports whether c can be part of a token (base64url-ish IDs).
func isTok(c byte) bool { return isAlnum(c) || c == '_' || c == '-' || c == '+' || c == '=' }

// digits returns the end of the run of ASCII digits starting at i (< hi).
func digits(s string, i, hi int) int {
	for i < hi && isDigit(s[i]) {
		i++
	}
	return i
}

// wordEnd reports whether position j (< hi or == hi) ends a word.
func wordEnd(s string, j, hi int) bool { return j >= hi || !(isAlnum(s[j]) || s[j] == '_') }

// scan normalizes s[lo:hi].
func (n *normalizer) scan(lo, hi int) {
	s := n.s
	i := lo
	for i < hi {
		c := s[i]
		if c == ':' && i > lo && (isAlnum(s[i-1]) || s[i-1] == ']') {
			// host:port. Keep the port, unless it is the local side of a
			// connection (followed by "->").
			if j := digits(s, i+1, hi); j > i+1 && j-i-1 <= 5 && wordEnd(s, j, hi) {
				if j+1 < hi && s[j] == '-' && s[j+1] == '>' {
					n.repl(i+1, j, "{port}")
				}
				i = j
				continue
			}
			i++
			continue
		}
		if !isAlnum(c) || (i > lo && (isAlnum(s[i-1]) || s[i-1] == '_')) {
			i++
			continue
		}
		// Start of a token.
		if isAlpha(c) {
			if j := matchURL(s, i, hi); j > 0 {
				n.url(i, j)
				i = j
				continue
			}
		}
		if isHex(c) {
			if j := matchUUID(s, i, hi); j > 0 {
				n.repl(i, j, "{uuid}")
				i = j
				continue
			}
		}
		if isDigit(c) {
			if j := matchTimestamp(s, i, hi); j > 0 {
				n.repl(i, j, "{ts}")
				i = j
				continue
			}
			if j := matchClock(s, i, hi); j > 0 {
				n.repl(i, j, "{ts}")
				i = j
				continue
			}
			if j := matchIPv4(s, i, hi); j > 0 {
				i = j // kept; a following :port is handled above
				continue
			}
			if j, nd, unit := matchNumber(s, i, hi); j > 0 {
				if nd >= 4 {
					if unit {
						n.repl(i, j, "{dur}")
					} else {
						n.repl(i, j, "{n}")
					}
				}
				i = j
				continue
			}
		}
		j := i + 1
		for j < hi && isTok(s[j]) {
			j++
		}
		n.token(i, j)
		i = j
	}
}

// token applies the ID and digit-run rules to the token s[i:j].
func (n *normalizer) token(i, j int) {
	s := n.s
	if j-i >= 16 && idLike(s[i:j]) {
		n.repl(i, j, "{id}")
		return
	}
	for k := i; k < j; {
		if !isDigit(s[k]) {
			k++
			continue
		}
		e := digits(s, k, j)
		if e-k >= 4 {
			n.repl(k, e, "{n}")
		}
		k = e
	}
}

// idLike reports whether a 16+ char token looks like a generated ID: all
// hex, or containing an alphanumeric run of 8+ characters with both
// letters and digits.
func idLike(t string) bool {
	allHex := true
	runLen, runDigit, runAlpha := 0, false, false
	for k := 0; k < len(t); k++ {
		c := t[k]
		if !isHex(c) {
			allHex = false
		}
		if isAlnum(c) {
			runLen++
			runDigit = runDigit || isDigit(c)
			runAlpha = runAlpha || isAlpha(c)
			if runLen >= 8 && runDigit && runAlpha {
				return true
			}
		} else {
			runLen, runDigit, runAlpha = 0, false, false
		}
	}
	return allHex
}

// matchUUID matches 8-4-4-4-12 hex digits at i, returning the end or 0.
func matchUUID(s string, i, hi int) int {
	const n = 36
	if hi-i < n {
		return 0
	}
	for k := 0; k < n; k++ {
		c := s[i+k]
		switch k {
		case 8, 13, 18, 23:
			if c != '-' {
				return 0
			}
		default:
			if !isHex(c) {
				return 0
			}
		}
	}
	if !wordEnd(s, i+n, hi) {
		return 0
	}
	return i + n
}

// fixedDigits reports whether s[i:i+k] (within hi) are all digits.
func fixedDigits(s string, i, k, hi int) bool {
	if i+k > hi {
		return false
	}
	for x := i; x < i+k; x++ {
		if !isDigit(s[x]) {
			return false
		}
	}
	return true
}

// matchTimestamp matches YYYY-MM-DD with an optional time part
// ([T ]hh:mm[:ss][.frac]), zone (Z, ±hh:mm, ±hhmm, " ±hhmm") and zone
// name (" UTC"), returning the end or 0.
func matchTimestamp(s string, i, hi int) int {
	if !(fixedDigits(s, i, 4, hi) && i+10 <= hi && s[i+4] == '-' && fixedDigits(s, i+5, 2, hi) &&
		s[i+7] == '-' && fixedDigits(s, i+8, 2, hi)) {
		return 0
	}
	j := i + 10
	if j+5 < hi && (s[j] == 'T' || s[j] == ' ') && fixedDigits(s, j+1, 2, hi) && s[j+3] == ':' && fixedDigits(s, j+4, 2, hi) {
		j += 6
		if j+2 < hi && s[j] == ':' && fixedDigits(s, j+1, 2, hi) {
			j += 3
		}
		if j+1 < hi && (s[j] == '.' || s[j] == ',') && isDigit(s[j+1]) {
			j = digits(s, j+1, hi)
		}
		switch {
		case j < hi && s[j] == 'Z':
			j++
		case j+5 <= hi && (s[j] == '+' || s[j] == '-') && fixedDigits(s, j+1, 2, hi):
			if j+6 <= hi && s[j+3] == ':' && fixedDigits(s, j+4, 2, hi) {
				j += 6
			} else if fixedDigits(s, j+3, 2, hi) {
				j += 5
			}
		case j+6 <= hi && s[j] == ' ' && (s[j+1] == '+' || s[j+1] == '-') && fixedDigits(s, j+2, 4, hi):
			j += 6
		}
		// Zone abbreviation (" UTC", " CEST").
		if j+1 < hi && s[j] == ' ' && s[j+1] >= 'A' && s[j+1] <= 'Z' {
			k := j + 1
			for k < hi && s[k] >= 'A' && s[k] <= 'Z' {
				k++
			}
			if k-j-1 >= 2 && k-j-1 <= 5 && wordEnd(s, k, hi) {
				j = k
			}
		}
	}
	if !wordEnd(s, j, hi) {
		return 0
	}
	return j
}

// matchClock matches h:mm:ss or hh:mm:ss with an optional fraction.
func matchClock(s string, i, hi int) int {
	j := digits(s, i, hi)
	if d := j - i; d < 1 || d > 2 {
		return 0
	}
	for k := 0; k < 2; k++ {
		if !(j+3 <= hi && s[j] == ':' && fixedDigits(s, j+1, 2, hi)) {
			return 0
		}
		j += 3
	}
	if j+1 < hi && s[j] == '.' && isDigit(s[j+1]) {
		j = digits(s, j+1, hi)
	}
	if !wordEnd(s, j, hi) {
		return 0
	}
	return j
}

// matchIPv4 matches a dotted-quad address, returning the end or 0.
func matchIPv4(s string, i, hi int) int {
	j := i
	for part := 0; part < 4; part++ {
		if part > 0 {
			if j >= hi || s[j] != '.' {
				return 0
			}
			j++
		}
		e := digits(s, j, hi)
		if d := e - j; d < 1 || d > 3 {
			return 0
		}
		j = e
	}
	if !wordEnd(s, j, hi) || (j+1 < hi && s[j] == '.' && isDigit(s[j+1])) {
		return 0
	}
	return j
}

// matchNumber matches a decimal number, optionally a Go duration
// ("1.5s", "1m30.25s", "250µs"), ending at a word boundary. It returns
// the end (0 if no match), the number of digits and whether it had a unit.
func matchNumber(s string, i, hi int) (end, nd int, unit bool) {
	j := i
	for {
		e := digits(s, j, hi)
		if e == j {
			return 0, 0, false
		}
		nd += e - j
		j = e
		if j+1 < hi && s[j] == '.' && isDigit(s[j+1]) {
			e = digits(s, j+1, hi)
			nd += e - j - 1
			j = e
		}
		u := durationUnit(s, j, hi)
		if u == 0 {
			break
		}
		unit = true
		j += u
		if j >= hi || !isDigit(s[j]) {
			break
		}
	}
	if !wordEnd(s, j, hi) {
		return 0, 0, false
	}
	return j, nd, unit
}

// durationUnit returns the byte length of a Go duration unit at j, or 0.
func durationUnit(s string, j, hi int) int {
	if j >= hi {
		return 0
	}
	switch s[j] {
	case 'n', 'u', 'm':
		if j+1 < hi && s[j+1] == 's' {
			return 2
		}
		if s[j] == 'm' {
			return 1
		}
	case 0xC2: // µ
		if j+2 < hi && s[j+1] == 0xB5 && s[j+2] == 's' {
			return 3
		}
	case 's', 'h':
		return 1
	}
	return 0
}

// matchURL matches scheme://... at i, returning the end (exclusive, with
// trailing punctuation left out) or 0.
func matchURL(s string, i, hi int) int {
	j := i
	for j < hi && (isAlnum(s[j]) || s[j] == '+' || s[j] == '.' || s[j] == '-') {
		j++
	}
	if j-i < 2 || j+3 > hi || s[j] != ':' || s[j+1] != '/' || s[j+2] != '/' {
		return 0
	}
	j += 3
	for j < hi {
		c := s[j]
		if c <= ' ' || c == '"' || c == '\'' || c == '<' || c == '>' || c == '`' || c == 0x7f {
			break
		}
		j++
	}
	for j > i && (s[j-1] == '.' || s[j-1] == ',' || s[j-1] == ':' || s[j-1] == ';' || s[j-1] == ')') {
		j--
	}
	return j
}

// url normalizes the URL s[i:j]: host kept, numeric path segments -> {n},
// other segments get the token rules, query values -> {v}.
func (n *normalizer) url(i, j int) {
	s := n.s
	k := i
	for s[k] != ':' {
		k++
	}
	k += 3
	for k < j && s[k] != '/' && s[k] != '?' && s[k] != '#' {
		k++
	}
	// Path.
	for k < j && s[k] == '/' {
		a := k + 1
		b := a
		for b < j && s[b] != '/' && s[b] != '?' && s[b] != '#' {
			b++
		}
		if b > a {
			if digits(s, a, b) == b {
				n.repl(a, b, "{n}")
			} else {
				n.scan(a, b)
			}
		}
		k = b
	}
	// Query: key=value pairs.
	if k < j && s[k] == '?' {
		k++
		for k < j && s[k] != '#' {
			b := k
			for b < j && s[b] != '&' && s[b] != '#' {
				b++
			}
			for x := k; x < b; x++ {
				if s[x] == '=' {
					if x+1 < b && s[x+1:b] != "{v}" {
						n.repl(x+1, b, "{v}")
					}
					break
				}
			}
			k = b
			if k < j && s[k] == '&' {
				k++
			}
		}
	}
	// Fragment.
	if k < j && s[k] == '#' && k+1 < j && s[k+1:j] != "{v}" {
		n.repl(k+1, j, "{v}")
	}
}
