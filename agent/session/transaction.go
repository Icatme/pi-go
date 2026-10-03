package session

import (
	"encoding/json"
	"fmt"
	"slices"
)

// BranchVersion binds a conditional write to one session lane and its revision.
// Revision changes even when a lane is moved away and back to the same head.
type BranchVersion struct {
	SessionID string `json:"session_id"`
	Lane      string `json:"lane"`
	Revision  uint64 `json:"revision"`
	Head      string `json:"head"`
}

// BranchSnapshot is one detached, root-to-leaf view read under the storage lock.
type BranchSnapshot struct {
	Version BranchVersion
	Entries []Entry
}

func (s *logState) version(lane string) (BranchVersion, error) {
	pointer, ok := s.lanes[lane]
	if !ok {
		return BranchVersion{}, sessionError(ErrorInvalidLane, "unknown session lane", nil)
	}
	return BranchVersion{s.header.ID, lane, pointer.Seq, pointer.LeafID}, nil
}

func (s *logState) branch(lane string) (BranchSnapshot, error) {
	version, err := s.version(lane)
	if err != nil {
		return BranchSnapshot{}, err
	}
	var entries []Entry
	for id := version.Head; id != ""; {
		entry, ok := s.entryByID[id]
		if !ok {
			return BranchSnapshot{}, sessionError(ErrorCorruptLog, "missing branch entry", nil)
		}
		entries = append(entries, entry)
		id = entry.ParentID
	}
	slices.Reverse(entries)
	return BranchSnapshot{Version: version, Entries: mustCloneJSON(entries)}, nil
}

// prepareBatch validates against a small scratch lane without copying the log.
func (s *logState) prepareBatch(expected BranchVersion, entries []NewEntry) ([]LogItem, error) {
	actual, err := s.version(expected.Lane)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, sessionError(ErrorConflict, "session branch revision changed", nil)
	}
	if len(entries) == 0 || len(entries) > 1024 {
		return nil, sessionError(ErrorInvalidEntry, "batch must contain 1 to 1024 entries", nil)
	}
	scratch := *s
	scratch.lanes = map[string]LanePointer{expected.Lane: s.lanes[expected.Lane]}
	seen := make(map[string]bool, len(entries))
	items := make([]LogItem, 0, len(entries))
	for _, entry := range entries {
		item, err := scratch.newEntryItem(expected.Lane, entry)
		if err != nil {
			return nil, err
		}
		// Use the validated JSON representation for both duplicate detection
		// and the next parent, exactly as applyBatch will see it.
		id := item.Entry.ID
		if seen[id] {
			return nil, sessionError(ErrorInvalidEntry, "duplicate batch entry id", nil)
		}
		seen[id] = true
		items = append(items, item)
		scratch.sequence = item.Seq
		scratch.lanes[expected.Lane] = LanePointer{Seq: item.Seq, Lane: expected.Lane, LeafID: id}
	}
	return items, nil
}

func (s *logState) applyBatch(items []LogItem) ([]Entry, error) {
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if err := s.apply(item); err != nil {
			return nil, err
		}
		entries = append(entries, *item.Entry)
	}
	return mustCloneJSON(entries), nil
}

type wireBatch struct {
	Kind  string    `json:"kind"`
	Items []LogItem `json:"items"`
}

func (s *logState) readLine(line []byte) error {
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &kind); err != nil {
		return err
	}
	if kind.Kind != "batch" {
		var item LogItem
		if err := decodeStrictJSON(line, &item); err != nil {
			return err
		}
		return s.apply(item)
	}
	var batch wireBatch
	if err := decodeStrictJSON(line, &batch); err != nil {
		return err
	}
	if len(batch.Items) == 0 || len(batch.Items) > 1024 {
		return fmt.Errorf("invalid transaction size")
	}
	// Validate the entire line before applying any item. All items belong to one lane.
	scratch := *s
	scratch.lanes = make(map[string]LanePointer, len(s.lanes))
	for k, v := range s.lanes {
		scratch.lanes[k] = v
	}
	seen := make(map[string]bool)
	var lane string
	for i, item := range batch.Items {
		if item.Kind != LogItemEntry || item.Entry == nil {
			return fmt.Errorf("transaction requires entries")
		}
		if i == 0 {
			lane = item.Entry.Lane
		}
		if item.Entry.Lane != lane || seen[item.Entry.ID] {
			return fmt.Errorf("invalid transaction lane or duplicate id")
		}
		if err := scratch.validateItem(item); err != nil {
			return err
		}
		seen[item.Entry.ID] = true
		scratch.sequence = item.Seq
		scratch.lanes[lane] = LanePointer{Seq: item.Seq, Lane: lane, LeafID: item.Entry.ID}
	}
	_, err := s.applyBatch(batch.Items)
	return err
}

func (s *MemoryStorage) ReadBranch(lane string) (BranchSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireOpen(); err != nil {
		return BranchSnapshot{}, err
	}
	return s.state.branch(lane)
}

func (s *MemoryStorage) CompareAppend(expected BranchVersion, entries []NewEntry) (BranchVersion, []Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return BranchVersion{}, nil, err
	}
	items, err := s.state.prepareBatch(expected, entries)
	if err != nil {
		return BranchVersion{}, nil, err
	}
	result, err := s.state.applyBatch(items)
	version, _ := s.state.version(expected.Lane)
	return version, result, err
}

func (s *JSONLStorage) ReadBranch(lane string) (BranchSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.requireWritable(); err != nil {
		return BranchSnapshot{}, err
	}
	return s.state.branch(lane)
}

func (s *JSONLStorage) CompareAppend(expected BranchVersion, entries []NewEntry) (BranchVersion, []Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return BranchVersion{}, nil, err
	}
	items, err := s.state.prepareBatch(expected, entries)
	if err != nil {
		return BranchVersion{}, nil, err
	}
	if err := s.writeSynced(wireBatch{Kind: "batch", Items: items}); err != nil {
		s.failed = err
		return BranchVersion{}, nil, err
	}
	result, err := s.state.applyBatch(items)
	if err != nil {
		s.failed = err
		return BranchVersion{}, nil, sessionError(ErrorStorage, "synced transaction could not be applied", err)
	}
	version, _ := s.state.version(expected.Lane)
	return version, result, nil
}

func (s *Session) ReadBranch(lane string) (BranchSnapshot, error) { return s.storage.ReadBranch(lane) }

// CompareAppend assigns missing IDs and atomically commits a whole branch batch.
func (s *Session) CompareAppend(expected BranchVersion, entries []NewEntry) (BranchVersion, []Entry, error) {
	entries = slices.Clone(entries)
	for i := range entries {
		if entries[i].ID == "" {
			id, err := s.allocateID()
			if err != nil {
				return BranchVersion{}, nil, err
			}
			entries[i].ID = id
		}
	}
	return s.storage.CompareAppend(expected, entries)
}
