package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNullableUnionPreservesNullAtExecution(t *testing.T) {
	for _, union := range []string{"anyOf", "oneOf"} {
		t.Run(union, func(t *testing.T) {
			ran := false
			var got any
			tool := ToolDefinition{Name: "echo", Parameters: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{union: []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}}}}, Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
				ran = true
				got = in.Args.(map[string]any)["value"]
				return ToolResult{}, nil
			}}
			out := RunToolCall(t.Context(), ToolCall{ID: "call", Name: "echo", Arguments: json.RawMessage(`{"value":null}`)}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
			if !ran || got != nil || out.IsError {
				t.Fatalf("ran=%v got=%#v error=%v", ran, got, out.Err)
			}
		})
	}
}
