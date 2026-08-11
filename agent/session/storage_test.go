package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

type storageCase struct {
	name string
	new  func(*testing.T) Storage
}

func storageCases() []storageCase {
	return []storageCase{
		{
			name: "memory",
			new: func(t *testing.T) Storage {
				t.Helper()
				storage, err := NewMemoryStorage(Header{ID: "storage-test"})
				if err != nil {
					t.Fatalf("NewMemoryStorage: %v", err)
				}
				t.Cleanup(func() { _ = storage.Close() })
				return storage
			},
		},
		{
			name: "jsonl",
			new: func(t *testing.T) Storage {
				t.Helper()
				path := filepath.Join(t.TempDir(), "session.jsonl")
				storage, err := CreateJSONLStorage(path, Header{ID: "storage-test"})
				if err != nil {
					t.Fatalf("CreateJSONLStorage: %v", err)
				}
				t.Cleanup(func() { _ = storage.Close() })
				return storage
			},
		},
	}
}

func TestStorageAppendOnlyLaneTreeAndDetachedReads(t *testing.T) {
	for _, testCase := range storageCases() {
		t.Run(testCase.name, func(t *testing.T) {
			storage := testCase.new(t)
			header := storage.Header()
			if header.Kind != HeaderKindSession || header.Version != CurrentFormatVersion || header.ID != "storage-test" || header.CreatedAt <= 0 {
				t.Fatalf("unexpected normalized header: %+v", header)
			}
			if got := storage.Lanes(); len(got) != 1 || got[0] != (LanePointer{Lane: MainLane}) {
				t.Fatalf("unexpected initial lanes: %+v", got)
			}

			message := agent.Message{
				Role:  agent.RoleUser,
				Parts: []agent.Part{{Type: agent.PartTypeText, Text: "root"}},
				Metadata: map[string]any{
					"nested": map[string]any{"value": "original"},
				},
			}
			root, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "root", Message: &message})
			if err != nil {
				t.Fatalf("append root: %v", err)
			}
			if root.Seq != 1 || root.ParentID != "" || root.Lane != MainLane || root.Timestamp <= 0 {
				t.Fatalf("unexpected root entry: %+v", root)
			}
			message.Parts[0].Text = "mutated-input"
			message.Metadata["nested"].(map[string]any)["value"] = "mutated-input"
			root.Message.Parts[0].Text = "mutated-return"

			if err := storage.CreateLane("branch", root.ID); err != nil {
				t.Fatalf("create branch: %v", err)
			}
			mainChild := appendTextEntry(t, storage, MainLane, "main-child", "main")
			branchChild := appendTextEntry(t, storage, "branch", "branch-child", "branch")
			if mainChild.Seq != 3 || mainChild.ParentID != root.ID {
				t.Fatalf("unexpected main child: %+v", mainChild)
			}
			if branchChild.Seq != 4 || branchChild.ParentID != root.ID {
				t.Fatalf("unexpected branch child: %+v", branchChild)
			}
			if err := storage.MoveLane(MainLane, ""); err != nil {
				t.Fatalf("move main to root: %v", err)
			}
			newRoot := appendTextEntry(t, storage, MainLane, "new-root", "new root")
			if newRoot.Seq != 6 || newRoot.ParentID != "" {
				t.Fatalf("unexpected new root: %+v", newRoot)
			}

			storedRoot, ok := storage.Entry(root.ID)
			if !ok {
				t.Fatal("root entry not found")
			}
			if got := storedRoot.Message.Parts[0].Text; got != "root" {
				t.Fatalf("stored message was aliased: %q", got)
			}
			nested := storedRoot.Message.Metadata["nested"].(map[string]any)
			if got := nested["value"]; got != "original" {
				t.Fatalf("stored metadata was aliased: %v", got)
			}
			storedRoot.Message.Parts[0].Text = "mutated-read"
			nested["value"] = "mutated-read"
			again, _ := storage.Entry(root.ID)
			if again.Message.Parts[0].Text != "root" || again.Message.Metadata["nested"].(map[string]any)["value"] != "original" {
				t.Fatalf("Entry did not return a deep clone: %+v", again.Message)
			}

			entries := storage.Entries()
			if len(entries) != 4 {
				t.Fatalf("got %d entries, want 4", len(entries))
			}
			entries[0].Message.Parts[0].Text = "mutated-entries"
			if got, _ := storage.Entry(root.ID); got.Message.Parts[0].Text != "root" {
				t.Fatal("Entries returned aliased state")
			}
			lanes := storage.Lanes()
			if len(lanes) != 2 || lanes[0].Lane != "branch" || lanes[0].LeafID != branchChild.ID || lanes[1].Lane != MainLane || lanes[1].LeafID != newRoot.ID {
				t.Fatalf("unexpected lanes: %+v", lanes)
			}
			lanes[0].LeafID = "mutated"
			if storage.Lanes()[0].LeafID != branchChild.ID {
				t.Fatal("Lanes returned aliased state")
			}

			log := storage.Log()
			if len(log) != 6 {
				t.Fatalf("got %d log items, want 6", len(log))
			}
			for index, item := range log {
				if item.Seq != uint64(index+1) {
					t.Fatalf("log[%d] seq=%d", index, item.Seq)
				}
			}
			log[0].Entry.Message.Parts[0].Text = "mutated-log"
			if storage.Log()[0].Entry.Message.Parts[0].Text != "root" {
				t.Fatal("Log returned aliased state")
			}
		})
	}
}

func TestStorageRejectsInvalidEntriesWithoutConsumingSequence(t *testing.T) {
	for _, testCase := range storageCases() {
		t.Run(testCase.name, func(t *testing.T) {
			storage := testCase.new(t)
			message := agent.Message{Role: agent.RoleUser}
			invalid := []NewEntry{
				{Type: EntryTypeMessage, ID: "missing-message"},
				{Type: EntryTypeMessage, ID: "wrong-payload", Custom: &CustomData{CustomType: "test"}},
				{Type: EntryType("unknown"), ID: "unknown", Message: &message},
				{Type: EntryTypeCustom, ID: "bad-json", Custom: &CustomData{CustomType: "test", Payload: json.RawMessage(`{"broken"`)}},
				{Type: EntryTypeCompaction, ID: "missing-tail", Compaction: &CompactionData{Summary: "summary"}},
				{
					Type: EntryTypeCompaction,
					ID:   "incomplete-tail",
					Compaction: &CompactionData{
						Summary: "summary",
						RetainedTail: []agent.Message{
							agent.NewUserTextMessage("question"),
							{
								Role:      agent.RoleAssistant,
								ToolCalls: []agent.ToolCall{{ID: "call", Name: "tool"}},
							},
						},
					},
				},
			}
			for _, input := range invalid {
				if _, err := storage.AppendEntry(MainLane, input); errorCode(err) != ErrorInvalidEntry {
					t.Fatalf("AppendEntry(%s) error=%v, code=%q", input.ID, err, errorCode(err))
				}
			}
			first := appendTextEntry(t, storage, MainLane, "first", "first")
			if first.Seq != 1 {
				t.Fatalf("invalid writes consumed sequence: first seq=%d", first.Seq)
			}
			if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: first.ID, Message: &message}); errorCode(err) != ErrorAlreadyExists {
				t.Fatalf("duplicate id error=%v, code=%q", err, errorCode(err))
			}
			if err := storage.CreateLane("missing-target", "does-not-exist"); errorCode(err) != ErrorNotFound {
				t.Fatalf("missing target error=%v, code=%q", err, errorCode(err))
			}
			if err := storage.MoveLane("does-not-exist", ""); errorCode(err) != ErrorInvalidLane {
				t.Fatalf("missing lane error=%v, code=%q", err, errorCode(err))
			}
			second := appendTextEntry(t, storage, MainLane, "second", "second")
			if second.Seq != 2 {
				t.Fatalf("rejected mutations consumed sequence: second seq=%d", second.Seq)
			}
		})
	}
}

func TestStorageCompactionAndCustomPayloadsAreDetached(t *testing.T) {
	for _, testCase := range storageCases() {
		t.Run(testCase.name, func(t *testing.T) {
			storage := testCase.new(t)
			retained := []agent.Message{{
				Role:     agent.RoleUser,
				Parts:    []agent.Part{{Type: agent.PartTypeText, Text: "retained"}},
				Metadata: map[string]any{"nested": map[string]any{"value": "original"}},
			}}
			details := json.RawMessage(`{"source":"test"}`)
			compaction, err := storage.AppendEntry(MainLane, NewEntry{
				Type: EntryTypeCompaction,
				ID:   "compaction-1",
				Compaction: &CompactionData{
					Summary:      "summary",
					RetainedTail: retained,
					TokensBefore: 42,
					Details:      details,
				},
			})
			if err != nil {
				t.Fatalf("append compaction: %v", err)
			}
			retained[0].Parts[0].Text = "mutated-input"
			retained[0].Metadata["nested"].(map[string]any)["value"] = "mutated-input"
			details[11] = 'X'
			compaction.Compaction.RetainedTail[0].Parts[0].Text = "mutated-return"
			compaction.Compaction.Details[0] = '['

			payload := json.RawMessage(`{"enabled":true}`)
			custom, err := storage.AppendEntry(MainLane, NewEntry{
				Type:   EntryTypeCustom,
				ID:     "custom-1",
				Custom: &CustomData{CustomType: "test.state", Payload: payload},
			})
			if err != nil {
				t.Fatalf("append custom: %v", err)
			}
			payload[11] = 'X'
			custom.Custom.Payload[0] = '['

			storedCompaction, _ := storage.Entry("compaction-1")
			if storedCompaction.Compaction.RetainedTail[0].Parts[0].Text != "retained" {
				t.Fatal("compaction retained tail was aliased")
			}
			if storedCompaction.Compaction.RetainedTail[0].Metadata["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("compaction retained metadata was aliased")
			}
			if string(storedCompaction.Compaction.Details) != `{"source":"test"}` {
				t.Fatalf("compaction details were aliased: %s", storedCompaction.Compaction.Details)
			}
			storedCustom, _ := storage.Entry("custom-1")
			if string(storedCustom.Custom.Payload) != `{"enabled":true}` {
				t.Fatalf("custom payload was aliased: %s", storedCustom.Custom.Payload)
			}
		})
	}
}

func TestStorageConcurrentAppendsHaveStrictGlobalSequence(t *testing.T) {
	for _, testCase := range storageCases() {
		t.Run(testCase.name, func(t *testing.T) {
			storage := testCase.new(t)
			const count = 48
			var wait sync.WaitGroup
			errorsByIndex := make([]error, count)
			for index := range count {
				wait.Add(1)
				go func() {
					defer wait.Done()
					message := agent.Message{Role: agent.RoleUser, Parts: []agent.Part{{Type: agent.PartTypeText, Text: fmt.Sprintf("message-%d", index)}}}
					_, errorsByIndex[index] = storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: fmt.Sprintf("entry-%d", index), Message: &message})
				}()
			}
			wait.Wait()
			for index, err := range errorsByIndex {
				if err != nil {
					t.Fatalf("append %d: %v", index, err)
				}
			}
			log := storage.Log()
			if len(log) != count {
				t.Fatalf("got %d log items, want %d", len(log), count)
			}
			for index, item := range log {
				if item.Seq != uint64(index+1) || item.Entry == nil || item.Entry.Seq != item.Seq {
					t.Fatalf("invalid log item at %d: %+v", index, item)
				}
			}
		})
	}
}

func TestStorageCloseRejectsFurtherWrites(t *testing.T) {
	for _, testCase := range storageCases() {
		t.Run(testCase.name, func(t *testing.T) {
			storage := testCase.new(t)
			if err := storage.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := storage.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			message := agent.Message{Role: agent.RoleUser}
			if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "closed", Message: &message}); errorCode(err) != ErrorStorage {
				t.Fatalf("append after close error=%v, code=%q", err, errorCode(err))
			}
			if storage.Header().ID != "storage-test" {
				t.Fatal("reads should remain available after Close")
			}
		})
	}
}

func TestNewStorageRejectsUnsafeSessionIDs(t *testing.T) {
	for _, id := range []string{"", "../escape", "leading-", "-leading", "with space", "line\nbreak"} {
		if _, err := NewMemoryStorage(Header{ID: id}); errorCode(err) != ErrorInvalidHeader {
			t.Fatalf("id %q error=%v, code=%q", id, err, errorCode(err))
		}
	}
}

func appendTextEntry(t *testing.T, storage Storage, lane, id, text string) Entry {
	t.Helper()
	message := agent.Message{Role: agent.RoleUser, Parts: []agent.Part{{Type: agent.PartTypeText, Text: text}}}
	entry, err := storage.AppendEntry(lane, NewEntry{Type: EntryTypeMessage, ID: id, Message: &message})
	if err != nil {
		t.Fatalf("append entry %q: %v", id, err)
	}
	return entry
}

func errorCode(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var sessionErr *Error
	if !errors.As(err, &sessionErr) {
		return ""
	}
	return sessionErr.Code
}
