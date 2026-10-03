package codemode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/internal/jsontext"
)

// Store is a bounded, caller-owned invocation scope. It has no persistence,
// credentials or global namespace. Do not share it across identities.
type Store struct {
	mu       sync.Mutex
	maxBytes int
	revision uint64
	values   map[string]string
}

// RestoreStore validates a data-only snapshot before any script can read it.
// It restores neither JS stacks nor tool capabilities. Empty input is a new store.
func RestoreStore(maxBytes int, raw json.RawMessage) (*Store, error) {
	s, err := NewStore(maxBytes)
	if err != nil || len(raw) == 0 {
		return s, err
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("codemode: saved store exceeds byte limit")
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return nil, err
	}
	var saved struct {
		Version int             `json:"version"`
		Values  json.RawMessage `json:"values"`
	}
	outer := json.NewDecoder(bytes.NewReader(raw))
	outer.DisallowUnknownFields()
	if err := outer.Decode(&saved); err != nil || saved.Version != 1 {
		return nil, fmt.Errorf("codemode: invalid store snapshot version or envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(saved.Values))
	start, _ := decoder.Token()
	if start != json.Delim('{') {
		return nil, fmt.Errorf("codemode: saved store must be an object")
	}
	total := 0
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || key == "" || len(key) > 256 || !utf8.ValidString(key) {
			return nil, fmt.Errorf("codemode: invalid saved store key")
		}
		if _, exists := s.values[key]; exists {
			return nil, fmt.Errorf("codemode: duplicate saved store key")
		}
		if len(s.values) >= 256 {
			return nil, fmt.Errorf("codemode: saved store key limit exceeded")
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if err := ValidateJSON([]byte(value)); err != nil {
			return nil, err
		}
		units := 0
		for _, r := range value {
			units++
			if r > 0xffff {
				units++
			}
		}
		if units > 256<<10 {
			return nil, fmt.Errorf("codemode: saved store value exceeds UTF-16 limit")
		}
		total += len(key) + len(value)
		if total > maxBytes {
			return nil, fmt.Errorf("codemode: saved store exceeds byte limit")
		}
		s.values[key] = value
	}
	return s, nil
}

// Export returns a versioned, detached data snapshot. The host stages this value and commits
// only after the enclosing tool's final validation; Export itself does no I/O.
func (s *Store) Export() (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Keep each original JSON lexeme as a string. Outer JSON escaping must not
	// change JS precision or the script's UTF-16 value-size accounting on restore.
	raw, err := json.Marshal(struct {
		Version int               `json:"version"`
		Values  map[string]string `json:"values"`
	}{Version: 1, Values: s.values})
	if err != nil {
		return nil, err
	}
	if len(raw) > s.maxBytes {
		return nil, fmt.Errorf("codemode: serialized store exceeds byte limit")
	}
	return raw, nil
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
