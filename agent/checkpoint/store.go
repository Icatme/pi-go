package checkpoint

import (
	"context"
	"math"
	"sync"
)

// MemoryStore is a process-local, concurrency-safe checkpoint store.
// It deliberately has no filesystem persistence because snapshots can contain secrets.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[CheckpointID]StoredCheckpoint
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[CheckpointID]StoredCheckpoint)}
}

// Load returns an ownership-isolated checkpoint record.
func (s *MemoryStore) Load(ctx context.Context, id CheckpointID) (StoredCheckpoint, error) {
	if err := contextError(ctx); err != nil {
		return StoredCheckpoint{}, err
	}
	s.mu.RLock()
	if err := contextError(ctx); err != nil {
		s.mu.RUnlock()
		return StoredCheckpoint{}, err
	}
	record, ok := s.records[id]
	s.mu.RUnlock()
	if !ok {
		return StoredCheckpoint{}, ErrCheckpointNotFound
	}
	return cloneStoredCheckpoint(record), nil
}

// CompareAndSwap creates or replaces a record when expected matches its revision.
// Revision zero creates a previously absent checkpoint.
func (s *MemoryStore) CompareAndSwap(ctx context.Context, id CheckpointID, expected Revision, payload []byte) (StoredCheckpoint, error) {
	if err := contextError(ctx); err != nil {
		return StoredCheckpoint{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return StoredCheckpoint{}, err
	}

	current, exists := s.records[id]
	actual := Revision(0)
	if exists {
		actual = current.Revision
	}
	if actual != expected || expected == 0 && exists {
		return StoredCheckpoint{}, &RevisionConflictError{CheckpointID: id, Expected: expected, Actual: actual}
	}
	if actual == Revision(math.MaxUint64) {
		return StoredCheckpoint{}, &RevisionConflictError{CheckpointID: id, Expected: expected, Actual: actual}
	}
	record := StoredCheckpoint{
		Revision: actual + 1,
		Payload:  append([]byte(nil), payload...),
	}
	s.records[id] = record
	return cloneStoredCheckpoint(record), nil
}

func cloneStoredCheckpoint(record StoredCheckpoint) StoredCheckpoint {
	return StoredCheckpoint{Revision: record.Revision, Payload: append([]byte(nil), record.Payload...)}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
