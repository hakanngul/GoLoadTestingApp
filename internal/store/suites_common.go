package store

import (
	"encoding/json"
	"fmt"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// defaultSuiteRunLimit is the SuiteRunFilter limit when none is given.
const defaultSuiteRunLimit = 50

func suiteRunLimit(f SuiteRunFilter) int {
	if f.Limit <= 0 {
		return defaultSuiteRunLimit
	}
	return f.Limit
}

// encodeSuiteItems returns the stored JSON of suite items (TestID and
// Workers only: names are resolved on read).
func encodeSuiteItems(items []model.SuiteItem) (string, error) {
	type stored struct {
		TestID  string `json:"test_id"`
		Workers int    `json:"workers"`
	}
	out := make([]stored, 0, len(items))
	for _, it := range items {
		out = append(out, stored{TestID: it.TestID, Workers: it.Workers})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("store: encode suite items: %w", err)
	}
	return string(b), nil
}

func decodeSuiteItems(s string) ([]model.SuiteItem, error) {
	out := []model.SuiteItem{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode suite items: %w", err)
	}
	for i := range out {
		out[i].TestName = ""
	}
	return out, nil
}

func encodeSuiteRunItems(items []model.SuiteRunItem) (string, error) {
	if items == nil {
		items = []model.SuiteRunItem{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("store: encode suite run items: %w", err)
	}
	return string(b), nil
}

func decodeSuiteRunItems(s string) ([]model.SuiteRunItem, error) {
	out := []model.SuiteRunItem{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode suite run items: %w", err)
	}
	if out == nil {
		out = []model.SuiteRunItem{}
	}
	return out, nil
}

// cloneSuiteRun deep-copies a suite run (via its JSON shape).
func cloneSuiteRun(r model.SuiteRun) model.SuiteRun {
	r.FinishedAt = cloneTimePtr(r.FinishedAt)
	r.StartedAt = normTime(r.StartedAt)
	s, err := encodeSuiteRunItems(r.Items)
	if err == nil {
		r.Items, err = decodeSuiteRunItems(s)
	}
	if err != nil {
		r.Items = []model.SuiteRunItem{}
	}
	return r
}
