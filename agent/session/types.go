// Package session stores agent conversations as append-only lane logs.
package session

import (
	"encoding/json"
	"errors"

	"github.com/Icatme/pi-go/agent"
)

const (
	// CurrentFormatVersion is the only JSONL format version accepted by this package.
	CurrentFormatVersion = 1
	// MainLane is present in every session and initially points at the root.
	MainLane = "main"
)

// HeaderKind identifies a JSONL header.
type HeaderKind string

const (
	// HeaderKindSession identifies a session log header.
	HeaderKindSession HeaderKind = "session"
)

// Header is the immutable first record in a session JSONL file.
type Header struct {
	Kind            HeaderKind `json:"kind"`
	Version         int        `json:"version"`
	ID              string     `json:"id"`
	CreatedAt       int64      `json:"created_at"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
}

// EntryType identifies an entry payload.
type EntryType string

const (
	EntryTypeMessage    EntryType = "message"
	EntryTypeCompaction EntryType = "compaction"
	EntryTypeCustom     EntryType = "custom"
)

// CompactionData replaces an older branch prefix with a summary while retaining
// an explicit, already validated tail of messages.
type CompactionData struct {
	Summary      string          `json:"summary"`
	RetainedTail []agent.Message `json:"retained_tail,omitempty"`
	TokensBefore int64           `json:"tokens_before"`
	Details      json.RawMessage `json:"details,omitempty"`
}

// CustomData stores application data that does not directly participate in the
// model transcript.
type CustomData struct {
	CustomType string          `json:"custom_type"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// NewEntry is the caller-provisioned portion of an Entry. Sequence, lane,
// parent, and timestamp are assigned atomically by Storage.
type NewEntry struct {
	Type       EntryType       `json:"type"`
	ID         string          `json:"id"`
	Message    *agent.Message  `json:"message,omitempty"`
	Compaction *CompactionData `json:"compaction,omitempty"`
	Custom     *CustomData     `json:"custom,omitempty"`
}

// Entry is one node in the append-only session tree.
type Entry struct {
	Type       EntryType       `json:"type"`
	ID         string          `json:"id"`
	Seq        uint64          `json:"seq"`
	Lane       string          `json:"lane"`
	ParentID   string          `json:"parent_id,omitempty"`
	Timestamp  int64           `json:"timestamp"`
	Message    *agent.Message  `json:"message,omitempty"`
	Compaction *CompactionData `json:"compaction,omitempty"`
	Custom     *CustomData     `json:"custom,omitempty"`
}

// LanePointer is the durable head of one named branch. An empty LeafID means
// the root before any entry.
type LanePointer struct {
	Seq    uint64 `json:"seq"`
	Lane   string `json:"lane"`
	LeafID string `json:"leaf_id,omitempty"`
}

// LogItemKind identifies the payload stored on one JSONL mutation line.
type LogItemKind string

const (
	LogItemEntry LogItemKind = "entry"
	LogItemLane  LogItemKind = "lane"
)

// LogItem is one fully provisioned mutation in global sequence order. Exactly
// one payload matching Kind must be present.
type LogItem struct {
	Kind  LogItemKind  `json:"kind"`
	Seq   uint64       `json:"seq"`
	Entry *Entry       `json:"entry,omitempty"`
	Lane  *LanePointer `json:"lane,omitempty"`
}

// Storage is an append-only session log. Read methods return detached values
// that callers may mutate safely.
type Storage interface {
	Header() Header
	Lanes() []LanePointer
	CreateLane(lane, at string) error
	MoveLane(lane, to string) error
	AppendEntry(lane string, entry NewEntry) (Entry, error)
	Entry(id string) (Entry, bool)
	Entries() []Entry
	Log() []LogItem
	Close() error
}

// ErrorCode classifies session storage failures without exposing backend errors.
type ErrorCode string

const (
	ErrorInvalidHeader ErrorCode = "invalid_header"
	ErrorInvalidEntry  ErrorCode = "invalid_entry"
	ErrorInvalidLane   ErrorCode = "invalid_lane"
	ErrorNotFound      ErrorCode = "not_found"
	ErrorAlreadyExists ErrorCode = "already_exists"
	ErrorCorruptLog    ErrorCode = "corrupt_log"
	ErrorStorage       ErrorCode = "storage"
)

// Error is returned for all validated session failures.
type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is reports whether target has the same session error code.
func (e *Error) Is(target error) bool {
	var other *Error
	return errors.As(target, &other) && e != nil && e.Code == other.Code
}
