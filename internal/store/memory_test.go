package store

import "testing"

func TestMemory(t *testing.T) {
	runStoreSuite(t, func(t *testing.T) Store {
		s := NewMemory()
		t.Cleanup(s.Close)
		return s
	})
}
