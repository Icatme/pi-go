package session

import (
	"encoding/json"
	"github.com/Icatme/pi-go/agent"
	"strings"
	"testing"
)

func BenchmarkCompactionCutLargeArguments(b *testing.B) {
	call := agent.NewTextMessage(agent.RoleAssistant, "")
	raw, _ := json.Marshal(map[string]string{"content": strings.Repeat("x", 1<<20)})
	call.ToolCalls = []agent.ToolCall{{ID: "one", Name: "write", Arguments: raw}}
	result := agent.Message{Role: agent.RoleTool, ToolResult: &agent.ToolResultPayload{ToolCallID: "one"}}
	prefix := []agent.Message{agent.NewTextMessage(agent.RoleUser, "old"), call, result}
	tail := []agent.Message{agent.NewTextMessage(agent.RoleUser, "new")}
	b.ReportAllocs()
	for b.Loop() {
		if err := ValidateCompactionCut(prefix, tail); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCompactionCutValidationDoesNotMutatePayloadsOrBackingArray(t *testing.T) {
	call := agent.NewTextMessage(agent.RoleAssistant, "")
	call.ToolCalls = []agent.ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{"n":9007199254740993}`), ParsedArgs: map[string]any{"nested": []any{"kept"}}}}
	call.Metadata = map[string]any{"bytes": json.RawMessage(`{"saved":true}`)}
	result := agent.Message{Role: agent.RoleTool, ToolResult: &agent.ToolResultPayload{ToolCallID: "one", Details: map[string]any{"nested": []any{"kept"}}}}
	backing := []agent.Message{agent.NewTextMessage(agent.RoleUser, "old"), call, result, agent.NewTextMessage(agent.RoleUser, "sentinel")}
	prefix := backing[:3]
	tail := []agent.Message{agent.NewTextMessage(agent.RoleUser, "new")}
	before, _ := json.Marshal(backing)
	for index, candidate := range [][]agent.Message{tail, {agent.NewTextMessage(agent.RoleAssistant, "invalid cut")}} {
		err := ValidateCompactionCut(prefix, candidate)
		if (index == 0 && err != nil) || (index == 1 && err == nil) {
			t.Fatalf("cut %d result %v", index, err)
		}
		after, _ := json.Marshal(backing)
		if string(before) != string(after) {
			t.Fatal("validation mutated caller data or spare capacity")
		}
	}
}
