package data

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"sync/atomic"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// CSV is an in-memory Feeder backed by a CSV file. The first row is the
// header; its cells become the variable names. It is safe for concurrent use.
type CSV struct {
	columns []string
	rows    [][]string
	mode    scenario.DataMode
	next    atomic.Uint64 // calls handed out so far (sequential/unique)
}

// NewCSV loads the whole CSV file at path into memory and returns a Feeder
// that hands out rows according to mode. An empty mode means sequential.
// The concrete type is *CSV (exposing Len and Columns).
//
// It fails if the file is empty, has no data rows, has rows with a
// different number of fields than the header, or has empty/duplicate
// column names.
func NewCSV(path string, mode scenario.DataMode) (Feeder, error) {
	switch mode {
	case "":
		mode = scenario.DataSequential
	case scenario.DataSequential, scenario.DataRandom, scenario.DataUnique:
	default:
		return nil, fmt.Errorf("data: unknown mode %q", mode)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	// FieldsPerRecord == 0 (default): every record must match the header's
	// length, so ragged rows are rejected.

	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("data: %s: file is empty", path)
	}
	if err != nil {
		return nil, fmt.Errorf("data: %s: %w", path, err)
	}
	header[0] = strings.TrimPrefix(header[0], "\uFEFF") // tolerate UTF-8 BOM
	seen := make(map[string]bool, len(header))
	for i, h := range header {
		h = strings.TrimSpace(h)
		if h == "" {
			return nil, fmt.Errorf("data: %s: header column %d is empty", path, i+1)
		}
		if seen[h] {
			return nil, fmt.Errorf("data: %s: duplicate header column %q", path, h)
		}
		seen[h] = true
		header[i] = h
	}

	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("data: %s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("data: %s: no data rows after header", path)
	}
	return &CSV{columns: header, rows: rows, mode: mode}, nil
}

// Next returns the next row as a freshly allocated map that the caller may
// mutate. In unique mode it returns ErrExhausted once every row was used.
func (c *CSV) Next() (map[string]string, error) {
	var idx int
	switch c.mode {
	case scenario.DataRandom:
		idx = rand.IntN(len(c.rows)) // per-P generator, no global lock
	case scenario.DataUnique:
		n := c.next.Add(1) - 1
		if n >= uint64(len(c.rows)) {
			return nil, ErrExhausted
		}
		idx = int(n)
	default: // sequential
		idx = int((c.next.Add(1) - 1) % uint64(len(c.rows)))
	}
	row := c.rows[idx]
	m := make(map[string]string, len(c.columns))
	for i, col := range c.columns {
		m[col] = row[i]
	}
	return m, nil
}

// Len reports the number of data rows (excluding the header).
func (c *CSV) Len() int { return len(c.rows) }

// Columns returns a copy of the column names in file order.
func (c *CSV) Columns() []string { return append([]string(nil), c.columns...) }

var _ Feeder = (*CSV)(nil)

// First returns the first data row as a fresh map (trial runs).
func (c *CSV) First() map[string]string {
	m := make(map[string]string, len(c.columns))
	for i, col := range c.columns {
		m[col] = c.rows[0][i]
	}
	return m
}

// Header reads only the header row of a CSV document, with the same
// normalisation as NewCSV (BOM removed, names trimmed).
func Header(r io.Reader) ([]string, error) {
	header, err := csv.NewReader(r).Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("data: file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	header[0] = strings.TrimPrefix(header[0], "\uFEFF")
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	return header, nil
}
