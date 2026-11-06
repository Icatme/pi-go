package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestStoredLogCopyMatchesJSONAndIsDetached(t *testing.T) {
	for _, backend := range []string{"memory", "jsonl"} {
		for _, profile := range []string{"mixed", "custom", "lane", "custom-then-message"} {
			t.Run(backend+"/"+profile, func(t *testing.T) {
				var storage Storage
				path := filepath.Join(t.TempDir(), "copy.jsonl")
				var err error
				if backend == "memory" {
					storage, err = NewMemoryStorage(Header{ID: "copy"})
				} else {
					storage, err = CreateJSONLStorage(path, Header{ID: "copy"})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = storage.Close() }()
				checkStoredLogCopy(t, storage)
				fixtures := storedCopyFixtures()
				var entries []NewEntry
				for _, entry := range fixtures {
					if profile == "mixed" || (profile == "custom" || profile == "custom-then-message") && entry.Type == EntryTypeCustom {
						entries = append(entries, entry)
					}
				}
				if profile == "custom-then-message" {
					entries = append(entries, fixtures[0])
				}
				for i, entry := range entries {
					if _, err := storage.AppendEntry(MainLane, entry); err != nil {
						t.Fatalf("append %d: %v", i, err)
					}
					checkStoredLogCopy(t, storage)
				}
				leaf := ""
				if len(entries) > 0 {
					leaf = entries[0].ID
				}
				if err := storage.CreateLane("branch", leaf); err != nil {
					t.Fatal(err)
				}
				checkStoredLogCopy(t, storage)
				if err := storage.MoveLane("branch", ""); err != nil {
					t.Fatal(err)
				}
				checkStoredLogCopy(t, storage)
				before := storage.Log()
				if backend == "jsonl" {
					if err := storage.Close(); err != nil {
						t.Fatal(err)
					}
					storage, err = OpenJSONLStorageStrict(path)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, storage.Log()) {
						t.Fatal("reopen changed log values")
					}
					checkStoredLogCopy(t, storage)
				}
			})
		}
	}
}

func checkStoredLogCopy(t *testing.T, storage Storage) {
	t.Helper()
	var state *logState
	switch s := storage.(type) {
	case *MemoryStorage:
		state = s.state
	case *JSONLStorage:
		state = s.state
	}
	// The previous implementation is the oracle, including omitempty, raw bytes,
	// interface number types, timestamps, and nil versus empty collections.
	want := mustCloneJSON(state.log)
	got := storage.Log()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("copy differs from JSON oracle:\ngot %#v\nwant %#v", got, want)
	}
	if len(got) == 0 {
		return
	}
	mutateStoredCopy(reflect.ValueOf(got))
	if !reflect.DeepEqual(storage.Log(), want) {
		t.Fatal("mutating a returned log changed storage")
	}
	if !reflect.DeepEqual(state.log, want) {
		t.Fatal("mutating a returned log changed internal values")
	}
}

// Mutate every populated exported field in the fixtures, including pointers,
// nested interface maps/slices, and raw JSON bytes.
func mutateStoredCopy(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	if v.Type() == reflect.TypeOf(time.Time{}) {
		if v.CanSet() {
			v.Set(reflect.ValueOf(time.Time{}))
		}
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			c := reflect.New(v.Elem().Type()).Elem()
			c.Set(v.Elem())
			mutateStoredCopy(c)
			if v.CanSet() {
				v.Set(c)
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			mutateStoredCopy(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				mutateStoredCopy(v.Field(i))
			}
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			c := reflect.New(v.Type().Elem()).Elem()
			c.Set(v.MapIndex(key))
			mutateStoredCopy(c)
			v.SetMapIndex(key, c)
		}
		if !v.IsNil() && v.Type().Key().Kind() == reflect.String {
			v.SetMapIndex(reflect.ValueOf("mutated"), reflect.Zero(v.Type().Elem()))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			mutateStoredCopy(v.Index(i))
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString("mutated")
		}
	case reflect.Bool:
		if v.CanSet() {
			v.SetBool(!v.Bool())
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.CanSet() {
			v.SetInt(7)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.CanSet() {
			v.SetUint(7)
		}
	}
}

func storedCopyFixtures() []NewEntry {
	section := "instructions"
	raw := json.RawMessage(` { "n":9007199254740993123456789, "s":"<>&", "a": [null, {}] } `)
	nested := func(i int) map[string]any {
		return map[string]any{
			"nested": []any{map[string]any{"number": json.Number("9007199254740993123456789"), "decimal": json.Number("1.2300e+45"), "empty": []any{}, "nil": nil}, []any{i, true, "text"}},
			"raw":    raw, "bytes": []byte("bytes"),
			"typed": map[string][]uint64{"values": {9007199254740993, uint64(i)}, "empty": {}, "nil": nil},
		}
	}
	var entries []NewEntry
	for i := 0; i < 24; i++ {
		message := agent.Message{Role: agent.RoleAssistant, Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 123, time.FixedZone("offset", 3600))}
		switch i % 6 {
		case 0:
			message.System = &agent.SystemMessagePayload{Sections: map[string]*string{"set": &section, "remove": nil}, ToolsAdded: []agent.ToolDeclaration{{Name: "tool", Parameters: nested(i), OutputSchema: nested(i), ConstrainedSampling: &pigo.ToolConstrainedSampling{Type: "grammar", Mode: "require", Syntax: "lark", Definition: "start: /.+/"}}}, ToolsRemoved: []agent.ToolReference{{Name: "old"}}}
		case 1:
			message.Parts = []agent.Part{{Type: agent.PartTypeText, Text: "text"}}
			message.ToolCalls = []agent.ToolCall{{ID: "call", Name: "tool", Arguments: raw, ParsedArgs: nested(i)}, {ID: "empty", Name: "tool", ParsedArgs: map[string]any{}}, {ID: "nil", Name: "tool"}}
		case 2:
			message.ToolResult = &agent.ToolResultPayload{ToolCallID: "call", ToolName: "tool", Content: []agent.Part{{Type: agent.PartTypeText, Text: "result"}}, Details: nested(i), StructuredContent: raw, Execution: &agent.ToolExecutionInfo{Local: agent.ToolLocalReturned, Attempts: []agent.ToolSendAttempt{{Number: 1, Remote: agent.ToolRemoteUnknown}}}, Failure: &agent.ToolFailure{Code: agent.ToolFailureProtocol, Reason: "reason"}, ChildCalls: &agent.ChildCallReport{}}
		case 3:
			message.System = &agent.SystemMessagePayload{Sections: map[string]*string{}, ToolsAdded: []agent.ToolDeclaration{}, ToolsRemoved: []agent.ToolReference{}}
			message.Parts = []agent.Part{}
			message.ToolCalls = []agent.ToolCall{}
			message.Metadata = map[string]any{}
			message.Payload = map[string]any{}
		case 4:
			message.Metadata = nested(i)
			message.Payload = nested(i)
		}
		entries = append(entries, NewEntry{Type: EntryTypeMessage, ID: fmt.Sprintf("message-%d", i), Message: &message})
	}
	tail := agent.Message{Role: agent.RoleUser, Parts: []agent.Part{{Type: agent.PartTypeText, Text: "tail"}}, Metadata: nested(0)}
	entries = append(entries, NewEntry{Type: EntryTypeCompaction, ID: "compaction", Compaction: &CompactionData{Summary: "summary", RetainedTail: []agent.Message{tail}, Details: raw, TokensBefore: 9007199254740993}})
	for i, payload := range []json.RawMessage{nil, {}, raw, json.RawMessage(`null`)} {
		entries = append(entries, NewEntry{Type: EntryTypeCustom, ID: fmt.Sprintf("custom-%d", i), Custom: &CustomData{CustomType: "data", Payload: payload}})
	}
	return entries
}
