package session

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Icatme/pi-go/agent"
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

type logState struct {
	header    Header
	sequence  uint64
	lanes     map[string]LanePointer
	entries   []Entry
	entryByID map[string]Entry
	usedIDs   map[string]struct{}
	log       []LogItem
}

func normalizeNewHeader(header Header) (Header, error) {
	if header.Kind == "" {
		header.Kind = HeaderKindSession
	}
	if header.Version == 0 {
		header.Version = CurrentFormatVersion
	}
	if header.CreatedAt == 0 {
		header.CreatedAt = time.Now().UTC().UnixMilli()
	}
	if err := validateHeader(header); err != nil {
		return Header{}, err
	}
	return header, nil
}

func validateHeader(header Header) error {
	if header.Kind != HeaderKindSession {
		return sessionError(ErrorInvalidHeader, "session header has invalid kind", nil)
	}
	if header.Version != CurrentFormatVersion {
		return sessionError(ErrorInvalidHeader, fmt.Sprintf("unsupported session format version %d", header.Version), nil)
	}
	if !sessionIDPattern.MatchString(header.ID) {
		return sessionError(ErrorInvalidHeader, "session id must start and end with an alphanumeric character and contain only alphanumerics, '.', '_', or '-'", nil)
	}
	if header.CreatedAt < 0 {
		return sessionError(ErrorInvalidHeader, "session header has invalid created_at", nil)
	}
	if header.ParentSessionID != "" {
		if !sessionIDPattern.MatchString(header.ParentSessionID) {
			return sessionError(ErrorInvalidHeader, "parent session id is invalid", nil)
		}
	}
	return nil
}

func newLogState(header Header) *logState {
	return &logState{
		header:    header,
		lanes:     map[string]LanePointer{MainLane: {Lane: MainLane}},
		entryByID: make(map[string]Entry),
		usedIDs:   make(map[string]struct{}),
	}
}

func (s *logState) nextSequence() uint64 {
	return s.sequence + 1
}

func (s *logState) headerClone() Header {
	return s.header
}

func (s *logState) lanesClone() []LanePointer {
	lanes := make([]LanePointer, 0, len(s.lanes))
	for _, pointer := range s.lanes {
		lanes = append(lanes, pointer)
	}
	sortLanePointers(lanes)
	return lanes
}

func (s *logState) entryClone(id string) (Entry, bool) {
	entry, ok := s.entryByID[id]
	if !ok {
		return Entry{}, false
	}
	return mustCloneJSON(entry), true
}

func (s *logState) entriesClone() []Entry {
	return mustCloneJSON(s.entries)
}

func (s *logState) logClone() []LogItem {
	return mustCloneJSON(s.log)
}

func (s *logState) newLaneItem(lane, leafID string, create bool) (LogItem, error) {
	if err := validateLaneName(lane); err != nil {
		return LogItem{}, err
	}
	_, exists := s.lanes[lane]
	if create && exists {
		return LogItem{}, sessionError(ErrorAlreadyExists, fmt.Sprintf("lane %q already exists", lane), nil)
	}
	if !create && !exists {
		return LogItem{}, sessionError(ErrorInvalidLane, fmt.Sprintf("lane %q does not exist", lane), nil)
	}
	if leafID != "" {
		if _, ok := s.entryByID[leafID]; !ok {
			return LogItem{}, sessionError(ErrorNotFound, fmt.Sprintf("entry %q does not exist", leafID), nil)
		}
	}
	pointer := LanePointer{Seq: s.nextSequence(), Lane: lane, LeafID: leafID}
	return LogItem{Kind: LogItemLane, Seq: pointer.Seq, Lane: &pointer}, nil
}

func (s *logState) newEntryItem(lane string, input NewEntry) (LogItem, error) {
	pointer, ok := s.lanes[lane]
	if !ok {
		return LogItem{}, sessionError(ErrorInvalidLane, fmt.Sprintf("lane %q does not exist", lane), nil)
	}
	cloned, err := cloneValidated(input)
	if err != nil {
		return LogItem{}, sessionError(ErrorInvalidEntry, "entry payload is not valid JSON", err)
	}
	entry := Entry{
		Type:       cloned.Type,
		ID:         cloned.ID,
		Seq:        s.nextSequence(),
		Lane:       lane,
		ParentID:   pointer.LeafID,
		Timestamp:  time.Now().UTC().UnixMilli(),
		Message:    cloned.Message,
		Compaction: cloned.Compaction,
		Custom:     cloned.Custom,
	}
	item := LogItem{Kind: LogItemEntry, Seq: entry.Seq, Entry: &entry}
	if err := s.validateItem(item); err != nil {
		return LogItem{}, err
	}
	return item, nil
}

func (s *logState) validateItem(item LogItem) error {
	if item.Seq == 0 || item.Seq != s.nextSequence() {
		return sessionError(ErrorCorruptLog, fmt.Sprintf("expected seq %d, got %d", s.nextSequence(), item.Seq), nil)
	}
	payloads := boolInt(item.Entry != nil) + boolInt(item.Lane != nil)
	if payloads != 1 {
		return sessionError(ErrorCorruptLog, "log item must contain exactly one payload", nil)
	}
	switch item.Kind {
	case LogItemEntry:
		if item.Entry == nil || item.Lane != nil {
			return sessionError(ErrorCorruptLog, "entry log item has mismatched payload", nil)
		}
		return s.validateEntry(*item.Entry, item.Seq)
	case LogItemLane:
		if item.Lane == nil || item.Entry != nil {
			return sessionError(ErrorCorruptLog, "lane log item has mismatched payload", nil)
		}
		return s.validateLanePointer(*item.Lane, item.Seq)
	default:
		return sessionError(ErrorCorruptLog, fmt.Sprintf("unknown log item kind %q", item.Kind), nil)
	}
}

func (s *logState) validateEntry(entry Entry, seq uint64) error {
	if entry.Seq != seq {
		return sessionError(ErrorCorruptLog, "entry seq does not match log seq", nil)
	}
	if err := validateIdentifier("entry id", entry.ID); err != nil {
		return sessionError(ErrorInvalidEntry, err.Error(), nil)
	}
	if _, exists := s.usedIDs[entry.ID]; exists {
		return sessionError(ErrorAlreadyExists, fmt.Sprintf("id %q already exists", entry.ID), nil)
	}
	if err := validateLaneName(entry.Lane); err != nil {
		return err
	}
	pointer, ok := s.lanes[entry.Lane]
	if !ok {
		return sessionError(ErrorInvalidLane, fmt.Sprintf("lane %q does not exist", entry.Lane), nil)
	}
	if entry.ParentID != pointer.LeafID {
		return sessionError(ErrorCorruptLog, fmt.Sprintf("entry %q parent %q is not lane %q leaf %q", entry.ID, entry.ParentID, entry.Lane, pointer.LeafID), nil)
	}
	if entry.Timestamp < 0 {
		return sessionError(ErrorInvalidEntry, "entry timestamp is invalid", nil)
	}
	if err := validateStoredEntryPayload(entry.Type, entry.Message, entry.Compaction, entry.Custom); err != nil {
		return err
	}
	if _, err := cloneValidated(entry); err != nil {
		return sessionError(ErrorInvalidEntry, "entry payload is not valid JSON", err)
	}
	return nil
}

func validateStoredEntryPayload(entryType EntryType, message *agent.Message, compaction *CompactionData, custom *CustomData) error {
	payloads := boolInt(message != nil) + boolInt(compaction != nil) + boolInt(custom != nil)
	if payloads != 1 {
		return sessionError(ErrorInvalidEntry, "entry must contain exactly one payload", nil)
	}
	switch entryType {
	case EntryTypeMessage:
		if message == nil || compaction != nil || custom != nil {
			return sessionError(ErrorInvalidEntry, "message entry has mismatched payload", nil)
		}
	case EntryTypeCompaction:
		if compaction == nil || message != nil || custom != nil {
			return sessionError(ErrorInvalidEntry, "compaction entry has mismatched payload", nil)
		}
		if strings.TrimSpace(compaction.Summary) == "" {
			return sessionError(ErrorInvalidEntry, "compaction summary is empty", nil)
		}
		if compaction.TokensBefore < 0 {
			return sessionError(ErrorInvalidEntry, "compaction tokens_before is invalid", nil)
		}
		if err := validateRawJSON("compaction details", compaction.Details); err != nil {
			return err
		}
		starts, err := completeTurnStarts(compaction.RetainedTail)
		if err != nil || len(starts) == 0 {
			return sessionError(ErrorInvalidEntry, "compaction retained_tail is not a complete user turn", err)
		}
	case EntryTypeCustom:
		if custom == nil || message != nil || compaction != nil {
			return sessionError(ErrorInvalidEntry, "custom entry has mismatched payload", nil)
		}
		if err := validateIdentifier("custom type", custom.CustomType); err != nil {
			return sessionError(ErrorInvalidEntry, err.Error(), nil)
		}
		if err := validateRawJSON("custom payload", custom.Payload); err != nil {
			return err
		}
	default:
		return sessionError(ErrorInvalidEntry, fmt.Sprintf("unknown entry type %q", entryType), nil)
	}
	return nil
}

func (s *logState) validateLanePointer(pointer LanePointer, seq uint64) error {
	if pointer.Seq != seq {
		return sessionError(ErrorCorruptLog, "lane pointer seq does not match log seq", nil)
	}
	if err := validateLaneName(pointer.Lane); err != nil {
		return err
	}
	if pointer.LeafID != "" {
		if _, ok := s.entryByID[pointer.LeafID]; !ok {
			return sessionError(ErrorCorruptLog, fmt.Sprintf("lane %q points to unknown entry %q", pointer.Lane, pointer.LeafID), nil)
		}
	}
	return nil
}

func (s *logState) apply(item LogItem) error {
	if err := s.validateItem(item); err != nil {
		return err
	}
	cloned := mustCloneJSON(item)
	switch cloned.Kind {
	case LogItemEntry:
		entry := *cloned.Entry
		s.entries = append(s.entries, entry)
		s.entryByID[entry.ID] = entry
		s.usedIDs[entry.ID] = struct{}{}
		s.lanes[entry.Lane] = LanePointer{Seq: entry.Seq, Lane: entry.Lane, LeafID: entry.ID}
	case LogItemLane:
		pointer := *cloned.Lane
		s.lanes[pointer.Lane] = pointer
	}
	s.sequence = cloned.Seq
	s.log = append(s.log, cloned)
	return nil
}

func validateIdentifier(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains an invalid character", field)
	}
	return nil
}

func validateLaneName(lane string) error {
	if err := validateIdentifier("lane", lane); err != nil {
		return sessionError(ErrorInvalidLane, err.Error(), nil)
	}
	return nil
}

func validateRawJSON(field string, value json.RawMessage) error {
	if len(value) != 0 && !json.Valid(value) {
		return sessionError(ErrorInvalidEntry, fmt.Sprintf("%s is not valid JSON", field), nil)
	}
	return nil
}

func cloneValidated[T any](value T) (T, error) {
	var cloned T
	encoded, err := json.Marshal(value)
	if err != nil {
		return cloned, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&cloned); err != nil {
		return cloned, err
	}
	return cloned, nil
}

func mustCloneJSON[T any](value T) T {
	cloned, err := cloneValidated(value)
	if err != nil {
		panic(fmt.Sprintf("session: invalid internal JSON value: %v", err))
	}
	return cloned
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sortLanePointers(pointers []LanePointer) {
	for i := 1; i < len(pointers); i++ {
		for j := i; j > 0 && pointers[j].Lane < pointers[j-1].Lane; j-- {
			pointers[j], pointers[j-1] = pointers[j-1], pointers[j]
		}
	}
}
