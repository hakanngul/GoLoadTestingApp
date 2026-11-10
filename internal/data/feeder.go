// Package data feeds CSV rows into iterations.
//
// CONTRACT FILE (interface). CSV implementation lives in csv.go.
package data

import "errors"

// ErrExhausted is returned by Next in unique mode when rows run out.
var ErrExhausted = errors.New("data: rows exhausted")

// Feeder hands out one row (column -> value) per iteration.
// Implementations must be safe for concurrent use.
type Feeder interface {
	Next() (map[string]string, error)
}

// None is a Feeder that always returns an empty row.
type None struct{}

// Next implements Feeder.
func (None) Next() (map[string]string, error) { return map[string]string{}, nil }
