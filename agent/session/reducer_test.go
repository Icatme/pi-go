package session

import (
	"errors"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestReduceRebuildsLanesBranchesAndCompactedContext(t *testing.T) {
	t.Parallel()

	u1 := agent.NewTextMessage(agent.RoleUser, "first")
	a1 := agent.NewTextMessage(agent.RoleAssistant, "answer one")
	u2 := agent.NewTextMessage(agent.RoleUser, "second")
	a2 := agent.NewTextMessage(agent.RoleAssistant, "answer two")
	u3 := agent.NewTextMessage(agent.RoleUser, "third")
	a3 := agent.NewTextMessage(agent.RoleAssistant, "answer three")
	branchUser := agent.NewTextMessage(agent.RoleUser, "alternate")
	log := []LogItem{
		reducerEntryItem(1, MainLane, "u1", "", u1),
		reducerEntryItem(2, MainLane, "a1", "u1", a1),
		reducerLaneItem(3, "alternate", "u1"),
		reducerEntryItem(4, MainLane, "u2", "a1", u2),
		reducerEntryItem(5, MainLane, "a2", "u2", a2),
		reducerCompactionItem(6, MainLane, "compact", "a2", CompactionData{
			Summary:      "first turn summary",
			RetainedTail: []agent.Message{u2, a2},
			TokensBefore: 42,
		}),
		reducerEntryItem(7, MainLane, "u3", "compact", u3),
		reducerEntryItem(8, MainLane, "a3", "u3", a3),
		reducerEntryItem(9, "alternate", "alt-u", "u1", branchUser),
	}

	state, err := Reduce(log)
	if err != nil {
		t.Fatalf("Reduce() error = %v", err)
	}
	if state.LastSeq != 9 {
		t.Fatalf("LastSeq = %d, want 9", state.LastSeq)
	}
	if state.LaneLeaves[MainLane] != "a3" || state.LaneLeaves["alternate"] != "alt-u" {
		t.Fatalf("LaneLeaves = %#v", state.LaneLeaves)
	}

	mainBranch, err := state.Branch(MainLane)
	if err != nil {
		t.Fatalf("Branch(main) error = %v", err)
	}
	assertEntryIDs(t, mainBranch, "u1", "a1", "u2", "a2", "compact", "u3", "a3")
	alternateBranch, err := state.Branch("alternate")
	if err != nil {
		t.Fatalf("Branch(alternate) error = %v", err)
	}
	assertEntryIDs(t, alternateBranch, "u1", "alt-u")

	context, err := state.Context(MainLane)
	if err != nil {
		t.Fatalf("Context(main) error = %v", err)
	}
	if context.Summary != "first turn summary" {
		t.Fatalf("Context summary = %q", context.Summary)
	}
	assertMessageTexts(t, context.Messages, "second", "answer two", "third", "answer three")

	alternateContext, err := state.Context("alternate")
	if err != nil {
		t.Fatalf("Context(alternate) error = %v", err)
	}
	if alternateContext.Summary != "" {
		t.Fatalf("alternate summary = %q, want empty", alternateContext.Summary)
	}
	assertMessageTexts(t, alternateContext.Messages, "first", "alternate")
}

func TestReduceReturnsDetachedStateAndContext(t *testing.T) {
	t.Parallel()

	message := agent.NewTextMessage(agent.RoleUser, "original")
	typedNested := map[string]string{"value": "typed before"}
	message.Metadata = map[string]any{
		"nested": map[string]any{"value": "before"},
		"typed":  typedNested,
	}
	item := reducerEntryItem(1, MainLane, "u1", "", message)
	state, err := Reduce([]LogItem{item})
	if err != nil {
		t.Fatalf("Reduce() error = %v", err)
	}

	item.Entry.Message.Parts[0].Text = "input mutated"
	item.Entry.Message.Metadata["nested"].(map[string]any)["value"] = "input mutated"
	typedNested["value"] = "typed input mutated"
	context, err := state.Context(MainLane)
	if err != nil {
		t.Fatalf("Context() error = %v", err)
	}
	if got := context.Messages[0].Parts[0].Text; got != "original" {
		t.Fatalf("state aliased input text: %q", got)
	}
	if got := context.Messages[0].Metadata["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("state aliased input metadata: %v", got)
	}
	if got := context.Messages[0].Metadata["typed"].(map[string]any)["value"]; got != "typed before" {
		t.Fatalf("state aliased typed input metadata: %v", got)
	}

	context.Messages[0].Parts[0].Text = "context mutated"
	second, err := state.Context(MainLane)
	if err != nil {
		t.Fatalf("second Context() error = %v", err)
	}
	if got := second.Messages[0].Parts[0].Text; got != "original" {
		t.Fatalf("Context() returned aliased state: %q", got)
	}
}

func TestReduceRejectsCorruptLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		log  []LogItem
	}{
		{
			name: "non consecutive sequence",
			log:  []LogItem{reducerEntryItem(2, MainLane, "u1", "", agent.NewTextMessage(agent.RoleUser, "x"))},
		},
		{
			name: "mismatched embedded sequence",
			log: func() []LogItem {
				item := reducerEntryItem(1, MainLane, "u1", "", agent.NewTextMessage(agent.RoleUser, "x"))
				item.Entry.Seq = 2
				return []LogItem{item}
			}(),
		},
		{
			name: "missing lane",
			log:  []LogItem{reducerEntryItem(1, "missing", "u1", "", agent.NewTextMessage(agent.RoleUser, "x"))},
		},
		{
			name: "parent does not match leaf",
			log: []LogItem{
				reducerEntryItem(1, MainLane, "u1", "", agent.NewTextMessage(agent.RoleUser, "x")),
				reducerEntryItem(2, MainLane, "a1", "", agent.NewTextMessage(agent.RoleAssistant, "y")),
			},
		},
		{
			name: "duplicate id",
			log: []LogItem{
				reducerEntryItem(1, MainLane, "same", "", agent.NewTextMessage(agent.RoleUser, "x")),
				reducerEntryItem(2, MainLane, "same", "same", agent.NewTextMessage(agent.RoleAssistant, "y")),
			},
		},
		{
			name: "kind payload mismatch",
			log: []LogItem{{
				Kind: LogItemLane,
				Seq:  1,
				Entry: &Entry{
					Type: EntryTypeMessage, ID: "u1", Seq: 1, Lane: MainLane,
					Message: reducerMessagePtr(agent.NewTextMessage(agent.RoleUser, "x")),
				},
			}},
		},
		{
			name: "lane target missing",
			log:  []LogItem{reducerLaneItem(1, "alternate", "missing")},
		},
		{
			name: "non durable message metadata",
			log: func() []LogItem {
				message := agent.NewTextMessage(agent.RoleUser, "x")
				message.Metadata = map[string]any{"callback": func() {}}
				return []LogItem{reducerEntryItem(1, MainLane, "u1", "", message)}
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Reduce(test.log)
			if !errors.Is(err, &Error{Code: ErrorCorruptLog}) {
				t.Fatalf("Reduce() error = %v, want corrupt_log", err)
			}
		})
	}
}

func TestStateRejectsUnknownLane(t *testing.T) {
	t.Parallel()

	state, err := Reduce(nil)
	if err != nil {
		t.Fatalf("Reduce(nil) error = %v", err)
	}
	if _, err := state.Branch("missing"); !errors.Is(err, &Error{Code: ErrorInvalidLane}) {
		t.Fatalf("Branch(missing) error = %v, want invalid_lane", err)
	}
	if _, err := state.Context("missing"); !errors.Is(err, &Error{Code: ErrorInvalidLane}) {
		t.Fatalf("Context(missing) error = %v, want invalid_lane", err)
	}
}

func reducerEntryItem(seq uint64, lane, id, parent string, message agent.Message) LogItem {
	return LogItem{
		Kind: LogItemEntry,
		Seq:  seq,
		Entry: &Entry{
			Type:      EntryTypeMessage,
			ID:        id,
			Seq:       seq,
			Lane:      lane,
			ParentID:  parent,
			Timestamp: int64(seq),
			Message:   reducerMessagePtr(message),
		},
	}
}

func reducerCompactionItem(seq uint64, lane, id, parent string, data CompactionData) LogItem {
	return LogItem{
		Kind: LogItemEntry,
		Seq:  seq,
		Entry: &Entry{
			Type:       EntryTypeCompaction,
			ID:         id,
			Seq:        seq,
			Lane:       lane,
			ParentID:   parent,
			Timestamp:  int64(seq),
			Compaction: &data,
		},
	}
}

func reducerLaneItem(seq uint64, lane, leaf string) LogItem {
	return LogItem{
		Kind: LogItemLane,
		Seq:  seq,
		Lane: &LanePointer{Seq: seq, Lane: lane, LeafID: leaf},
	}
}

func reducerMessagePtr(message agent.Message) *agent.Message {
	return &message
}

func assertEntryIDs(t *testing.T, entries []Entry, want ...string) {
	t.Helper()
	if len(entries) != len(want) {
		t.Fatalf("entry count = %d, want %d (%#v)", len(entries), len(want), entries)
	}
	for index := range want {
		if entries[index].ID != want[index] {
			t.Fatalf("entry %d id = %q, want %q", index, entries[index].ID, want[index])
		}
	}
}

func assertMessageTexts(t *testing.T, messages []agent.Message, want ...string) {
	t.Helper()
	if len(messages) != len(want) {
		t.Fatalf("message count = %d, want %d (%#v)", len(messages), len(want), messages)
	}
	for index := range want {
		if len(messages[index].Parts) != 1 || messages[index].Parts[0].Text != want[index] {
			t.Fatalf("message %d = %#v, want text %q", index, messages[index], want[index])
		}
	}
}
