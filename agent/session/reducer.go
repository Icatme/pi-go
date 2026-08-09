package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Icatme/pi-go/agent"
)

// State is the deterministic projection of an append-only session log.
// Entries and lane leaves reconstruct the conversation tree.
type State struct {
	LastSeq    uint64
	LaneLeaves map[string]string
	Entries    map[string]Entry
}

// Context is the model-facing conversation state for one lane. Summary is
// kept separate from Messages so callers can choose an explicit projection
// policy when converting it to a provider request.
type Context struct {
	Summary  string
	Messages []agent.Message
}

// Reduce validates and projects log items without mutating the input. Log
// sequence numbers must be global and consecutive, starting at one.
func Reduce(items []LogItem) (State, error) {
	state := State{
		LaneLeaves: map[string]string{MainLane: ""},
		Entries:    make(map[string]Entry),
	}
	usedIDs := make(map[string]struct{})

	for index, item := range items {
		expectedSeq := uint64(index + 1)
		if item.Seq != expectedSeq {
			return State{}, corruptLog("item %d has sequence %d; want %d", index, item.Seq, expectedSeq)
		}
		if payloadCount(item) != 1 {
			return State{}, corruptLog("sequence %d must contain exactly one payload", item.Seq)
		}

		switch item.Kind {
		case LogItemEntry:
			if item.Entry == nil || item.Lane != nil {
				return State{}, corruptLog("sequence %d entry kind does not match its payload", item.Seq)
			}
			if err := reduceEntry(&state, usedIDs, *item.Entry, item.Seq); err != nil {
				return State{}, err
			}
		case LogItemLane:
			if item.Lane == nil || item.Entry != nil {
				return State{}, corruptLog("sequence %d lane kind does not match its payload", item.Seq)
			}
			if err := reduceLane(&state, *item.Lane, item.Seq); err != nil {
				return State{}, err
			}
		default:
			return State{}, corruptLog("sequence %d has unknown kind %q", item.Seq, item.Kind)
		}
		state.LastSeq = item.Seq
	}

	return cloneState(state), nil
}

// Branch returns the root-to-leaf entry path for lane.
func (s State) Branch(lane string) ([]Entry, error) {
	leafID, ok := s.LaneLeaves[lane]
	if !ok {
		return nil, &Error{Code: ErrorInvalidLane, Message: fmt.Sprintf("session lane %q does not exist", lane)}
	}
	if leafID == "" {
		return []Entry{}, nil
	}

	reversed := make([]Entry, 0)
	visited := make(map[string]struct{})
	currentID := leafID
	for currentID != "" {
		if _, ok := visited[currentID]; ok {
			return nil, corruptLog("branch %q contains a cycle at entry %q", lane, currentID)
		}
		visited[currentID] = struct{}{}
		entry, ok := s.Entries[currentID]
		if !ok {
			return nil, corruptLog("branch %q references missing entry %q", lane, currentID)
		}
		reversed = append(reversed, cloneEntry(entry))
		currentID = entry.ParentID
	}

	branch := make([]Entry, len(reversed))
	for index := range reversed {
		branch[len(reversed)-1-index] = reversed[index]
	}
	return branch, nil
}

// Context rebuilds the effective conversation for lane. The newest
// compaction on that branch replaces all earlier messages with its summary and
// retained tail; later message entries are then appended normally.
func (s State) Context(lane string) (Context, error) {
	branch, err := s.Branch(lane)
	if err != nil {
		return Context{}, err
	}
	return contextFromBranch(branch), nil
}

func reduceEntry(state *State, usedIDs map[string]struct{}, entry Entry, seq uint64) error {
	normalized, err := cloneValidated(entry)
	if err != nil {
		return corruptLog("entry at sequence %d is not durable JSON: %v", seq, err)
	}
	entry = normalized
	if entry.Seq != seq {
		return corruptLog("entry %q sequence %d does not match log sequence %d", entry.ID, entry.Seq, seq)
	}
	if !validSessionIdentifier(entry.ID) {
		return corruptLog("sequence %d contains an entry without an id", seq)
	}
	if _, exists := usedIDs[entry.ID]; exists {
		return corruptLog("sequence %d repeats id %q", seq, entry.ID)
	}
	leafID, laneExists := state.LaneLeaves[entry.Lane]
	if !validSessionIdentifier(entry.Lane) {
		return corruptLog("entry %q contains invalid lane %q", entry.ID, entry.Lane)
	}
	if !laneExists {
		return corruptLog("entry %q references missing lane %q", entry.ID, entry.Lane)
	}
	if entry.ParentID != leafID {
		return corruptLog("entry %q parent %q does not match lane %q leaf %q", entry.ID, entry.ParentID, entry.Lane, leafID)
	}
	if entry.ParentID != "" {
		if _, parentExists := state.Entries[entry.ParentID]; !parentExists {
			return corruptLog("entry %q references missing parent %q", entry.ID, entry.ParentID)
		}
	}
	if entry.Timestamp < 0 {
		return corruptLog("entry %q has an invalid timestamp", entry.ID)
	}
	if err := validateEntryPayload(entry); err != nil {
		return err
	}

	usedIDs[entry.ID] = struct{}{}
	state.Entries[entry.ID] = cloneEntry(entry)
	state.LaneLeaves[entry.Lane] = entry.ID
	return nil
}

func reduceLane(state *State, pointer LanePointer, seq uint64) error {
	if pointer.Seq != seq {
		return corruptLog("lane %q sequence %d does not match log sequence %d", pointer.Lane, pointer.Seq, seq)
	}
	if !validSessionIdentifier(pointer.Lane) {
		return corruptLog("sequence %d contains an empty lane name", seq)
	}
	if pointer.LeafID != "" {
		if _, exists := state.Entries[pointer.LeafID]; !exists {
			return corruptLog("lane %q references missing entry %q", pointer.Lane, pointer.LeafID)
		}
	}
	state.LaneLeaves[pointer.Lane] = pointer.LeafID
	return nil
}

func validateEntryPayload(entry Entry) error {
	payloads := 0
	if entry.Message != nil {
		payloads++
	}
	if entry.Compaction != nil {
		payloads++
	}
	if entry.Custom != nil {
		payloads++
	}
	if payloads != 1 {
		return corruptLog("entry %q must contain exactly one payload", entry.ID)
	}
	switch entry.Type {
	case EntryTypeMessage:
		if entry.Message == nil {
			return corruptLog("message entry %q has a mismatched payload", entry.ID)
		}
	case EntryTypeCompaction:
		if entry.Compaction == nil || strings.TrimSpace(entry.Compaction.Summary) == "" {
			return corruptLog("compaction entry %q has an invalid payload", entry.ID)
		}
		if entry.Compaction.TokensBefore < 0 {
			return corruptLog("compaction entry %q has a negative token count", entry.ID)
		}
		if len(entry.Compaction.Details) != 0 && !json.Valid(entry.Compaction.Details) {
			return corruptLog("compaction entry %q has invalid details JSON", entry.ID)
		}
		starts, err := completeTurnStarts(entry.Compaction.RetainedTail)
		if err != nil || len(starts) == 0 {
			return corruptLog("compaction entry %q has an invalid retained tail", entry.ID)
		}
	case EntryTypeCustom:
		if entry.Custom == nil || !validSessionIdentifier(entry.Custom.CustomType) {
			return corruptLog("custom entry %q has an invalid payload", entry.ID)
		}
		if len(entry.Custom.Payload) != 0 && !json.Valid(entry.Custom.Payload) {
			return corruptLog("custom entry %q has invalid payload JSON", entry.ID)
		}
	default:
		return corruptLog("entry %q has unknown type %q", entry.ID, entry.Type)
	}
	return nil
}

func contextFromBranch(branch []Entry) Context {
	context := Context{}
	start := 0
	for index := len(branch) - 1; index >= 0; index-- {
		entry := branch[index]
		if entry.Type != EntryTypeCompaction || entry.Compaction == nil {
			continue
		}
		context.Summary = entry.Compaction.Summary
		context.Messages = cloneMessages(entry.Compaction.RetainedTail)
		start = index + 1
		break
	}
	for _, entry := range branch[start:] {
		if entry.Type == EntryTypeMessage && entry.Message != nil {
			context.Messages = append(context.Messages, cloneMessage(*entry.Message))
		}
	}
	return context
}

func payloadCount(item LogItem) int {
	count := 0
	if item.Entry != nil {
		count++
	}
	if item.Lane != nil {
		count++
	}
	return count
}

func corruptLog(format string, args ...any) error {
	return &Error{Code: ErrorCorruptLog, Message: fmt.Sprintf("corrupt session log: "+format, args...)}
}

func validSessionIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n\x00")
}

func cloneState(state State) State {
	cloned := State{
		LastSeq:    state.LastSeq,
		LaneLeaves: make(map[string]string, len(state.LaneLeaves)),
		Entries:    make(map[string]Entry, len(state.Entries)),
	}
	for lane, leafID := range state.LaneLeaves {
		cloned.LaneLeaves[lane] = leafID
	}
	for id, entry := range state.Entries {
		cloned.Entries[id] = cloneEntry(entry)
	}
	return cloned
}

func cloneEntry(entry Entry) Entry {
	cloned := entry
	if entry.Message != nil {
		message := cloneMessage(*entry.Message)
		cloned.Message = &message
	}
	if entry.Compaction != nil {
		compaction := *entry.Compaction
		compaction.RetainedTail = cloneMessages(entry.Compaction.RetainedTail)
		compaction.Details = cloneRawMessage(entry.Compaction.Details)
		cloned.Compaction = &compaction
	}
	if entry.Custom != nil {
		custom := *entry.Custom
		custom.Payload = cloneRawMessage(entry.Custom.Payload)
		cloned.Custom = &custom
	}
	return cloned
}

func cloneMessages(messages []agent.Message) []agent.Message {
	cloned := make([]agent.Message, len(messages))
	for index := range messages {
		cloned[index] = cloneMessage(messages[index])
	}
	return cloned
}

func cloneMessage(message agent.Message) agent.Message {
	cloned := message
	cloned.Parts = append([]agent.Part(nil), message.Parts...)
	cloned.ToolCalls = make([]agent.ToolCall, len(message.ToolCalls))
	for index, call := range message.ToolCalls {
		clonedCall := call
		clonedCall.Arguments = cloneRawMessage(call.Arguments)
		clonedCall.ParsedArgs = cloneStringAnyMap(call.ParsedArgs)
		cloned.ToolCalls[index] = clonedCall
	}
	if message.ToolResult != nil {
		result := *message.ToolResult
		result.Content = append([]agent.Part(nil), message.ToolResult.Content...)
		result.Details = cloneJSONValue(message.ToolResult.Details)
		cloned.ToolResult = &result
	}
	cloned.Metadata = cloneStringAnyMap(message.Metadata)
	cloned.Payload = cloneStringAnyMap(message.Payload)
	return cloned
}

func cloneStringAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneJSONValue(value)
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index := range typed {
			cloned[index] = cloneJSONValue(typed[index])
		}
		return cloned
	case json.RawMessage:
		return cloneRawMessage(typed)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
