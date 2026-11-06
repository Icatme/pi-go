package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icatme/pi-go/agent"
	"math"
	"path/filepath"
	"reflect"
	"testing"
)

// Frozen pre-optimization validation/application oracle. Keep independent of
// production validation so differential tests catch changes to acceptance too.
func (s *logState) originalValidateItem(item LogItem) error {
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
		return s.originalValidateEntry(*item.Entry, item.Seq)
	case LogItemLane:
		if item.Lane == nil || item.Entry != nil {
			return sessionError(ErrorCorruptLog, "lane log item has mismatched payload", nil)
		}
		return s.validateLanePointer(*item.Lane, item.Seq)
	default:
		return sessionError(ErrorCorruptLog, fmt.Sprintf("unknown log item kind %q", item.Kind), nil)
	}
}

func (s *logState) originalValidateEntry(entry Entry, seq uint64) error {
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

func (s *logState) originalApply(item LogItem) error {
	if err := s.originalValidateItem(item); err != nil {
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

func (s *logState) originalReadLine(line []byte) error {
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
		return s.originalApply(item)
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
		if err := scratch.originalValidateItem(item); err != nil {
			return err
		}
		seen[item.Entry.ID] = true
		scratch.sequence = item.Seq
		scratch.lanes[lane] = LanePointer{Seq: item.Seq, Lane: lane, LeafID: item.Entry.ID}
	}
	_, err := s.originalApplyBatch(batch.Items)
	return err
}

func (s *logState) originalApplyBatch(items []LogItem) ([]Entry, error) {
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if err := s.originalApply(item); err != nil {
			return nil, err
		}
		entries = append(entries, *item.Entry)
	}
	return mustCloneJSON(entries), nil
}

func replayHeader() Header {
	return Header{Kind: HeaderKindSession, Version: CurrentFormatVersion, ID: "replay", CreatedAt: 1767225600000}
}

func replayFixtures() []LogItem {
	inputs := storedCopyFixtures()
	inputs = append(inputs, NewEntry{Type: EntryTypeMessage, ID: "invalid-utf8-\xff", Message: &agent.Message{Role: agent.RoleUser, Metadata: map[string]any{"bad-\xff": "text-\xfe"}}})
	var items []LogItem
	for _, input := range inputs {
		entry := Entry{Type: input.Type, ID: input.ID, Seq: uint64(len(items) + 1), Lane: MainLane, Timestamp: 1767225600000, Message: input.Message, Compaction: input.Compaction, Custom: input.Custom}
		if len(items) > 0 {
			entry.ParentID = items[len(items)-1].Entry.ID
		}
		items = append(items, LogItem{Kind: LogItemEntry, Seq: entry.Seq, Entry: &entry})
	}
	// The original algorithm normalizes the last invalid UTF-8 ID before retention.
	id := mustCloneJSON(items[len(items)-1]).Entry.ID
	for _, lane := range []string{"branch-\xff", "branch"} {
		seq := uint64(len(items) + 1)
		items = append(items, LogItem{Kind: LogItemLane, Seq: seq, Lane: &LanePointer{Seq: seq, Lane: lane, LeafID: id}})
	}
	return items
}

func TestApplyMatchesOriginalAndDetaches(t *testing.T) {
	for _, decoded := range []bool{false, true} {
		t.Run(fmt.Sprint("decoded=", decoded), func(t *testing.T) {
			want, got := newLogState(replayHeader()), newLogState(replayHeader())
			for index := range replayFixtures() {
				item := replayFixtures()[index]
				if decoded {
					data, err := json.Marshal(item)
					if err != nil {
						t.Fatal(err)
					}
					var next LogItem
					if err := decodeStrictJSON(data, &next); err != nil {
						t.Fatal(err)
					}
					item = next
				}
				if err := want.originalApply(item); err != nil {
					t.Fatal(err)
				}
				if err := got.apply(item); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("state differs at sequence %d", item.Seq)
				}
				mutateStoredCopy(reflect.ValueOf(&item))
				if !reflect.DeepEqual(got, want) {
					t.Fatal("input mutation changed stored state")
				}
			}
		})
	}
}

func TestApplyRejectsLikeOriginalAtomically(t *testing.T) {
	valid := func() LogItem {
		return LogItem{Kind: LogItemEntry, Seq: 1, Entry: &Entry{Type: EntryTypeMessage, ID: "first", Seq: 1, Lane: MainLane, Message: &agent.Message{Role: agent.RoleUser}}}
	}
	cases := map[string]func(*LogItem){
		"sequence":          func(i *LogItem) { i.Seq = 2 },
		"entry-sequence":    func(i *LogItem) { i.Entry.Seq = 2 },
		"kind":              func(i *LogItem) { i.Kind = "unknown" },
		"missing":           func(i *LogItem) { i.Entry = nil },
		"both":              func(i *LogItem) { i.Lane = &LanePointer{} },
		"id":                func(i *LogItem) { i.Entry.ID = "\n" },
		"lane":              func(i *LogItem) { i.Entry.Lane = "missing" },
		"parent":            func(i *LogItem) { i.Entry.ParentID = "missing" },
		"timestamp":         func(i *LogItem) { i.Entry.Timestamp = -1 },
		"entry-type":        func(i *LogItem) { i.Entry.Type = "unknown" },
		"missing-message":   func(i *LogItem) { i.Entry.Message = nil },
		"multiple-payloads": func(i *LogItem) { i.Entry.Custom = &CustomData{CustomType: "data"} },
		"nan":               func(i *LogItem) { i.Entry.Message.Metadata = map[string]any{"n": math.NaN()} },
		"unsupported":       func(i *LogItem) { i.Entry.Message.Payload = map[string]any{"bad": make(chan int)} },
		"bad-number":        func(i *LogItem) { i.Entry.Message.Metadata = map[string]any{"n": json.Number("bad")} },
		"bad-raw":           func(i *LogItem) { i.Entry.Message.Metadata = map[string]any{"n": json.RawMessage("{")} },
		"tool-arguments-decode": func(i *LogItem) {
			i.Entry.Message.ToolCalls = []agent.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"value":"\ud800"}`)}}
		},
		"custom-raw": func(i *LogItem) {
			i.Entry.Type = EntryTypeCustom
			i.Entry.Message = nil
			i.Entry.Custom = &CustomData{CustomType: "data", Payload: json.RawMessage("{")}
		},
		"custom-type": func(i *LogItem) {
			i.Entry.Type = EntryTypeCustom
			i.Entry.Message = nil
			i.Entry.Custom = &CustomData{}
		},
		"compaction": func(i *LogItem) {
			i.Entry.Type = EntryTypeCompaction
			i.Entry.Message = nil
			i.Entry.Compaction = &CompactionData{Summary: "summary"}
		},
		"lane-sequence": func(i *LogItem) { i.Kind = LogItemLane; i.Entry = nil; i.Lane = &LanePointer{Seq: 2, Lane: "branch"} },
		"lane-target": func(i *LogItem) {
			i.Kind = LogItemLane
			i.Entry = nil
			i.Lane = &LanePointer{Seq: 1, Lane: "branch", LeafID: "missing"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			item := valid()
			change(&item)
			want, got := newLogState(replayHeader()), newLogState(replayHeader())
			oldErr, newErr := want.originalApply(item), got.apply(item)
			// Encoder errors name their enclosing Go type differently when an Entry is
			// wrapped in LogItem; the public session category and reason stay identical.
			if oldErr == nil || newErr == nil {
				t.Fatalf("expected rejection: %v / %v", oldErr, newErr)
			}
			var oldSession, newSession *Error
			if !errors.As(oldErr, &oldSession) || !errors.As(newErr, &newSession) || oldSession.Code != newSession.Code || oldSession.Message != newSession.Message {
				t.Fatalf("error contracts differ: %v / %v", oldErr, newErr)
			}
			if !reflect.DeepEqual(got, newLogState(replayHeader())) || !reflect.DeepEqual(want, got) {
				t.Fatal("failed apply mutated state")
			}
		})
	}
}

func TestReplayBatchRejectsLaterInvalidItemAtomically(t *testing.T) {
	items := replayFixtures()[:3]
	for _, corrupt := range []func([]LogItem){
		func(items []LogItem) { items[2].Entry.ParentID = "wrong" },
		func(items []LogItem) { items[2].Entry.ID = items[1].Entry.ID },
		func(items []LogItem) { items[2].Entry.Lane = "unknown" },
		func(items []LogItem) { items[2].Entry.Type = EntryTypeCustom },
	} {
		candidate := mustCloneJSON(items)
		corrupt(candidate)
		line, err := json.Marshal(wireBatch{Kind: "batch", Items: candidate})
		if err != nil {
			t.Fatal(err)
		}
		want, got := newLogState(replayHeader()), newLogState(replayHeader())
		oldErr, newErr := want.originalReadLine(line), got.readLine(line)
		if oldErr == nil || newErr == nil || oldErr.Error() != newErr.Error() {
			t.Fatalf("errors differ: %v / %v", oldErr, newErr)
		}
		if !reflect.DeepEqual(got, newLogState(replayHeader())) || !reflect.DeepEqual(want, got) {
			t.Fatal("invalid batch mutated state")
		}
	}
}

func TestAppendMarshalerFrozenAcrossReadAndReopen(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint("batch=", batch), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "replay.jsonl")
			storage, err := CreateJSONLStorage(path, replayHeader())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = storage.Close() }()
			calls := 0
			input := NewEntry{Type: EntryTypeMessage, ID: "entry", Message: &agent.Message{Role: agent.RoleUser, Metadata: map[string]any{"changing": statusChangingJSON{calls: &calls}}}}
			var returned Entry
			if batch {
				version, _ := storage.state.version(MainLane)
				_, entries, err := storage.CompareAppend(version, []NewEntry{input})
				if err != nil {
					t.Fatal(err)
				}
				returned = entries[0]
			} else {
				returned, err = storage.AppendEntry(MainLane, input)
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls != 1 {
				t.Fatalf("marshaler invoked %d times", calls)
			}
			stored, ok := storage.Entry("entry")
			if !ok || !reflect.DeepEqual(returned, stored) {
				t.Fatal("returned/stored differ")
			}
			before := storage.Log()
			mutateStoredCopy(reflect.ValueOf(&returned))
			mutateStoredCopy(reflect.ValueOf(input.Message))
			if !reflect.DeepEqual(storage.Log(), before) {
				t.Fatal("caller mutation changed storage")
			}
			if err := storage.Close(); err != nil {
				t.Fatal(err)
			}
			storage, err = OpenJSONLStorageStrict(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(storage.Log(), before) {
				t.Fatal("reopen changed log")
			}
		})
	}
}
