package store

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// defaultRunLimit is the ListRuns page size used when RunFilter.Limit <= 0.
const defaultRunLimit = 50

// newID returns a random (version 4) UUID in canonical lowercase form.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms (Go 1.24 panics
		// internally instead); keep the guard for older semantics.
		panic("store: crypto/rand: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// isUUID reports whether s is a UUID in canonical lowercase
// 8-4-4-4-12 form, which is the only form the stores hand out. Anything
// else can never match a stored ID, so callers treat it as not found.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// normTime converts t to UTC with microsecond precision, which is what
// PostgreSQL timestamptz keeps. Both stores normalise every timestamp
// they accept so they return identical values.
func normTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Microsecond)
}

// now returns the current time normalised with normTime.
func now() time.Time { return normTime(time.Now()) }
