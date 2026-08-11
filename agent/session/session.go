package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Icatme/pi-go/agent"
)

var sessionIDFallback atomic.Uint64

// IDGenerator provisions durable entry ids.
type IDGenerator func() string

// Options customizes a Session facade without changing its storage format.
type Options struct {
	IDGenerator IDGenerator
}

// Session adds typed conversation writes over append-only Storage.
type Session struct {
	storage Storage
	nextID  IDGenerator
	idMu    sync.Mutex

	closeOnce sync.Once
	closeErr  error
}

// New validates a storage handle and returns its typed session facade.
func New(storage Storage, options Options) (*Session, error) {
	if storage == nil {
		return nil, sessionError(ErrorStorage, "session storage is nil", nil)
	}
	nextID := options.IDGenerator
	if nextID == nil {
		nextID = newSessionID
	}
	return &Session{storage: storage, nextID: nextID}, nil
}

// Header returns the immutable session metadata.
func (s *Session) Header() Header {
	return s.storage.Header()
}

// Lanes returns detached lane pointers.
func (s *Session) Lanes() []LanePointer {
	return s.storage.Lanes()
}

// CreateLane creates a branch at an existing entry id or at the empty root.
func (s *Session) CreateLane(lane, at string) error {
	return s.storage.CreateLane(lane, at)
}

// MoveLane points an existing lane at an existing entry id or the empty root.
func (s *Session) MoveLane(lane, to string) error {
	return s.storage.MoveLane(lane, to)
}

// AppendMessage appends one runtime message to a lane.
func (s *Session) AppendMessage(lane string, message agent.Message) (Entry, error) {
	id, err := s.allocateID()
	if err != nil {
		return Entry{}, err
	}
	return s.storage.AppendEntry(lane, NewEntry{
		Type:    EntryTypeMessage,
		ID:      id,
		Message: &message,
	})
}

// AppendMessages appends messages in source order and stops at the first error.
func (s *Session) AppendMessages(lane string, messages []agent.Message) ([]Entry, error) {
	entries := make([]Entry, 0, len(messages))
	for _, message := range messages {
		entry, err := s.AppendMessage(lane, message)
		if err != nil {
			return entries, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// AppendCompaction commits a summary and its complete retained turn tail.
func (s *Session) AppendCompaction(lane string, data CompactionData) (Entry, error) {
	id, err := s.allocateID()
	if err != nil {
		return Entry{}, err
	}
	return s.storage.AppendEntry(lane, NewEntry{
		Type:       EntryTypeCompaction,
		ID:         id,
		Compaction: &data,
	})
}

// AppendCustomJSON appends application-owned JSON that does not enter the model
// context unless an outer layer explicitly projects it.
func (s *Session) AppendCustomJSON(lane, customType string, payload json.RawMessage) (Entry, error) {
	id, err := s.allocateID()
	if err != nil {
		return Entry{}, err
	}
	return s.storage.AppendEntry(lane, NewEntry{
		Type: EntryTypeCustom,
		ID:   id,
		Custom: &CustomData{
			CustomType: customType,
			Payload:    payload,
		},
	})
}

// State replays the durable log through the pure reducer.
func (s *Session) State() (State, error) {
	return Reduce(s.storage.Log())
}

// Context rebuilds the effective summary and message tail for a lane.
func (s *Session) Context(lane string) (Context, error) {
	state, err := s.State()
	if err != nil {
		return Context{}, err
	}
	return state.Context(lane)
}

// Close releases the storage handle. It is safe to call repeatedly.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.storage.Close()
	})
	return s.closeErr
}

func (s *Session) allocateID() (string, error) {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	id := s.nextID()
	if id == "" {
		return "", sessionError(ErrorInvalidEntry, "id generator returned an empty id", nil)
	}
	return id, nil
}

func newSessionID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("session-%x-%x", time.Now().UTC().UnixNano(), sessionIDFallback.Add(1))
}

func sessionError(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}
