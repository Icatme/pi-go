package codemode

import (
	"fmt"
	"sync"
)

// Store is a bounded, caller-owned invocation scope. It has no persistence,
// credentials or global namespace. Do not share it across identities.
type Store struct {
	mu       sync.Mutex
	maxBytes int
	revision uint64
	values   map[string]string
}

func NewStore(maxBytes int) (*Store, error) {
	if maxBytes <= 0 || maxBytes > 64<<20 {
		return nil, fmt.Errorf("codemode: invalid store byte limit")
	}
	return &Store{maxBytes: maxBytes, values: map[string]string{}}, nil
}

func (s *Store) snapshot(maxBytes int) (map[string]string, int, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxBytes <= 0 {
		return nil, 0, 0, fmt.Errorf("uninitialized Store; use NewStore")
	}
	bytes := 0
	for k, v := range s.values {
		bytes += len(k) + len(v)
	}
	if bytes > maxBytes {
		return nil, 0, 0, fmt.Errorf("store snapshot exceeds script byte limit")
	}
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, bytes, s.revision, nil
}

func (s *Store) commit(expected uint64, values map[string]string, bytes int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != expected {
		return &ScriptError{Code: "store_conflict", Message: "store revision changed before commit; script is not replayed"}
	}
	if bytes > s.maxBytes {
		return &ScriptError{Code: "store_limit", Message: "store commit exceeds caller byte limit"}
	}
	// The caller hands over the completed script's private map; no copy of an
	// unbounded or changing user map is made under the commit lock.
	s.values = values
	s.revision++
	return nil
}
