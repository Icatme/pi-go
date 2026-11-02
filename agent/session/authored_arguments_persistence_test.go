package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Exercise the durable JSONL boundary, not just ToolCall's JSON encoder.
func TestJSONLAuthoredArgumentPresenceSurvivesReopen(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"absent", nil},
		{"empty-edit", map[string]any{}},
		{"exact-number", map[string]any{"id": json.Number("9007199254740993")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			storage, err := CreateJSONLStorage(path, Header{ID: "authored-presence"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			message := agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "call", Name: "lookup", Arguments: json.RawMessage(`{"stale":true}`), ParsedArgs: tc.args}}}
			entry, err := storage.AppendEntry(MainLane, NewEntry{ID: "authored-entry", Type: EntryTypeMessage, Message: &message})
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenJSONLStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			loaded, ok := reopened.Entry(entry.ID)
			if !ok || loaded.Message == nil || len(loaded.Message.ToolCalls) != 1 {
				t.Fatal("tool call not restored")
			}
			call := loaded.Message.ToolCalls[0]
			if (call.ParsedArgs == nil) != (tc.args == nil) || len(call.ParsedArgs) != len(tc.args) {
				t.Fatalf("argument presence changed: %#v", call.ParsedArgs)
			}
			if tc.name == "exact-number" && call.ParsedArgs["id"] != json.Number("9007199254740993") {
				t.Fatalf("number changed: %#v", call.ParsedArgs)
			}
			var executed map[string]any
			out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "lookup", Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
				executed = in.Args.(map[string]any)
				return agent.ToolResult{}, nil
			}}}})
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			if tc.args == nil {
				if executed["stale"] != true {
					t.Fatal("absent arguments no longer use raw fallback")
				}
			} else if _, stale := executed["stale"]; stale {
				t.Fatal("restored authored object revived stale raw arguments")
			}
		})
	}
}
