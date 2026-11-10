package cluster

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"

	"github.com/hakanngul/GoLoadTestingApp/internal/scenario"
)

// share returns worker index's part of total when total is split over count
// workers: total/count, plus one for the lowest total%count indices. The
// shares of all workers always sum to total exactly.
func share(total, index, count int) int {
	if count <= 1 {
		return total
	}
	if total <= 0 {
		return 0
	}
	s := total / count
	if index < total%count {
		s++
	}
	return s
}

// ShareLoad returns the part of load l that worker index (0-based) of count
// workers should generate.
//
// Every stage target (iterations/s in the open model, VUs in the closed
// model) is split with integer shares whose sum over all workers equals the
// original target exactly; the remainder goes to the lowest indices. Since
// stages ramp linearly between their endpoints, the per-worker profiles also
// sum to the original profile at every instant. Stage durations and the
// model are unchanged. MaxVUs (the open model's in-flight cap) is split the
// same way but never drops below 1.
//
// Limitation: with tiny targets some workers get 0 for a stage (e.g. 1 rps
// over 3 workers gives 1, 0, 0). That is intended: the total stays exact,
// and a worker with a 0 target simply idles through that stage.
//
// count <= 1 (or an out-of-range index) returns an unchanged copy.
func ShareLoad(l scenario.Load, index, count int) scenario.Load {
	out := l
	out.Stages = append([]scenario.Stage(nil), l.Stages...)
	if count <= 1 || index < 0 || index >= count {
		return out
	}
	for i := range out.Stages {
		out.Stages[i].Target = share(out.Stages[i].Target, index, count)
	}
	if l.MaxVUs > 0 {
		out.MaxVUs = max(1, share(l.MaxVUs, index, count))
	}
	return out
}

// PartitionCSV keeps the header of a CSV document and only the data rows
// whose 0-based row index satisfies rowIndex % count == index, so that the
// partitions of all workers are disjoint and together hold every row. It is
// used for the unique data mode; the sequential and random modes give every
// worker all rows. It returns the partition and its number of data rows
// (which may be 0 when there are fewer rows than workers).
//
// Rows are only re-encoded, not validated; data.NewCSV validates the result.
func PartitionCSV(content []byte, index, count int) ([]byte, int, error) {
	if count <= 0 || index < 0 || index >= count {
		return nil, 0, fmt.Errorf("cluster: invalid partition %d/%d", index, count)
	}
	r := csv.NewReader(bytes.NewReader(content))
	r.FieldsPerRecord = -1 // data.NewCSV reports ragged rows with context
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, 0, errors.New("cluster: data file is empty")
	}
	if err != nil {
		return nil, 0, fmt.Errorf("cluster: data file: %w", err)
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, 0, err
	}
	rows := 0
	for i := 0; ; i++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("cluster: data file: %w", err)
		}
		if i%count != index {
			continue
		}
		if err := w.Write(rec); err != nil {
			return nil, 0, err
		}
		rows++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), rows, nil
}
