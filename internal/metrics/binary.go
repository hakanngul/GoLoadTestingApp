package metrics

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"time"

	hdrhistogram "github.com/HdrHistogram/hdrhistogram-go"
)

// wireVersion identifies the binary snapshot format.
const wireVersion = 1

type wireStats struct {
	Requests, Errors, Bytes int64
	Hist                    []byte // hdrhistogram V2 compressed encoding
}

type wireSnapshot struct {
	Version    int
	Start, End time.Time
	Dropped    int64
	Total      wireStats
	ByStep     map[string]wireStats
}

func encodeStats(s *Stats) (wireStats, error) {
	if s == nil {
		s = NewStats()
	}
	h := s.Hist
	if h == nil {
		h = newHist()
	}
	b, err := h.Encode(hdrhistogram.V2CompressedEncodingCookieBase)
	if err != nil {
		return wireStats{}, err
	}
	return wireStats{Requests: s.Requests, Errors: s.Errors, Bytes: s.Bytes, Hist: b}, nil
}

func decodeStats(w wireStats) (*Stats, error) {
	h, err := hdrhistogram.Decode(w.Hist)
	if err != nil {
		return nil, err
	}
	ref := newHist()
	if h.LowestTrackableValue() != ref.LowestTrackableValue() ||
		h.HighestTrackableValue() != ref.HighestTrackableValue() ||
		h.SignificantFigures() != ref.SignificantFigures() {
		// Normalize foreign geometry so later merges are lossless.
		ref.Merge(h)
		h = ref
	}
	return &Stats{Requests: w.Requests, Errors: w.Errors, Bytes: w.Bytes, Hist: h}, nil
}

// MarshalBinary encodes s for transport between workers and the master
// (distributed mode). Histograms use the HdrHistogram V2 compressed format.
func (s *Snapshot) MarshalBinary() ([]byte, error) {
	w := wireSnapshot{
		Version: wireVersion,
		Start:   s.Start,
		End:     s.End,
		Dropped: s.Dropped,
		ByStep:  make(map[string]wireStats, len(s.ByStep)),
	}
	var err error
	if w.Total, err = encodeStats(s.Total); err != nil {
		return nil, fmt.Errorf("metrics: encode total: %w", err)
	}
	for name, st := range s.ByStep {
		if w.ByStep[name], err = encodeStats(st); err != nil {
			return nil, fmt.Errorf("metrics: encode step %q: %w", name, err)
		}
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&w); err != nil {
		return nil, fmt.Errorf("metrics: encode snapshot: %w", err)
	}
	return buf.Bytes(), nil
}

// UnmarshalBinary replaces s with the snapshot encoded in data by
// MarshalBinary.
func (s *Snapshot) UnmarshalBinary(data []byte) error {
	var w wireSnapshot
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&w); err != nil {
		return fmt.Errorf("metrics: decode snapshot: %w", err)
	}
	if w.Version != wireVersion {
		return fmt.Errorf("metrics: unsupported snapshot version %d", w.Version)
	}
	total, err := decodeStats(w.Total)
	if err != nil {
		return fmt.Errorf("metrics: decode total: %w", err)
	}
	byStep := make(map[string]*Stats, len(w.ByStep))
	for name, ws := range w.ByStep {
		st, err := decodeStats(ws)
		if err != nil {
			return fmt.Errorf("metrics: decode step %q: %w", name, err)
		}
		byStep[name] = st
	}
	*s = Snapshot{Start: w.Start, End: w.End, Total: total, ByStep: byStep, Dropped: w.Dropped}
	return nil
}
