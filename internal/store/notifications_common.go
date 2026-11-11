package store

import (
	"encoding/json"
	"fmt"
)

// encodeEvents returns the stored JSON of a channel's event list.
func encodeEvents(events []string) (string, error) {
	if events == nil {
		events = []string{}
	}
	b, err := json.Marshal(events)
	if err != nil {
		return "", fmt.Errorf("store: encode notification events: %w", err)
	}
	return string(b), nil
}

// decodeEvents parses a stored event list (never nil).
func decodeEvents(s string) ([]string, error) {
	out := []string{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode notification events: %w", err)
	}
	return out, nil
}
