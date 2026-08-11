package session

import "sync"

// MemoryStorage is an in-process implementation of the append-only storage
// contract. It uses the same validation and state transitions as JSONLStorage.
type MemoryStorage struct {
	mu     sync.RWMutex
	state  *logState
	closed bool
}

// NewMemoryStorage creates a session with an empty main lane. Empty kind,
// version, and created_at fields are filled with current defaults.
func NewMemoryStorage(header Header) (*MemoryStorage, error) {
	normalized, err := normalizeNewHeader(header)
	if err != nil {
		return nil, err
	}
	return &MemoryStorage{state: newLogState(normalized)}, nil
}

func (s *MemoryStorage) Header() Header {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.headerClone()
}

func (s *MemoryStorage) Lanes() []LanePointer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.lanesClone()
}

func (s *MemoryStorage) CreateLane(lane, at string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return err
	}
	item, err := s.state.newLaneItem(lane, at, true)
	if err != nil {
		return err
	}
	return s.state.apply(item)
}

func (s *MemoryStorage) MoveLane(lane, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return err
	}
	item, err := s.state.newLaneItem(lane, to, false)
	if err != nil {
		return err
	}
	return s.state.apply(item)
}

func (s *MemoryStorage) AppendEntry(lane string, input NewEntry) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return Entry{}, err
	}
	item, err := s.state.newEntryItem(lane, input)
	if err != nil {
		return Entry{}, err
	}
	if err := s.state.apply(item); err != nil {
		return Entry{}, err
	}
	return mustCloneJSON(*item.Entry), nil
}

func (s *MemoryStorage) Entry(id string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.entryClone(id)
}

func (s *MemoryStorage) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.entriesClone()
}

func (s *MemoryStorage) Log() []LogItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.logClone()
}

// Close prevents subsequent mutations. Read methods remain available.
func (s *MemoryStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *MemoryStorage) requireOpen() error {
	if s.closed {
		return sessionError(ErrorStorage, "session storage is closed", nil)
	}
	return nil
}

var _ Storage = (*MemoryStorage)(nil)
