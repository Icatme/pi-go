package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCustomParserExactIntegerSchemaProjection(t *testing.T) {
	for _, tc := range []struct {
		name, raw, key string
		bound          float64
		valid          bool
	}{
		{"above maximum", "9007199254740993e0", "maximum", 9007199254740992, false},
		{"above exclusive minimum", "9007199254740993e0", "exclusiveMinimum", 9007199254740992, true},
		{"above uint64 maximum boundary", "18446744073709551617", "maximum", 18446744073709551616, false},
		{"maxuint64 exact", "18446744073709551615e0", "minimum", 0, true},
		{"minint64 exact", "-9223372036854775808e0", "maximum", 0, true},
		{"below minint64 unsupported", "-9223372036854775809", "maximum", 0, false},
		{"ordinary decimal accepted", "0.1", "maximum", 0.5, true},
		{"ordinary decimal rejected", "0.75", "maximum", 0.5, false},
		{"fraction formatted integer", "9007199254740993.000", "maximum", 9007199254740992, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := map[string]any{"n": json.Number(tc.raw)}
			ran := false
			tool := ToolDefinition{Name: "counter", Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "number", tc.key: tc.bound}}},
				ParseArguments: func(ToolCall) (any, error) { return parsed, nil },
				Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
					ran = true
					if !reflect.DeepEqual(in.Args, parsed) {
						t.Fatalf("execution arguments changed: %#v", in.Args)
					}
					return ToolResult{}, nil
				}}
			out := RunToolCall(t.Context(), ToolCall{Name: "counter", Arguments: json.RawMessage("{}")}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
			if ran != tc.valid || out.IsError == tc.valid {
				t.Fatalf("ran=%v want=%v err=%v", ran, tc.valid, out.Err)
			}
			if parsed["n"] != json.Number(tc.raw) {
				t.Fatal("parser output mutated")
			}
		})
	}
}

func TestCustomParserTypedIntegerProjection(t *testing.T) {
	type payload struct {
		Values []json.Number `json:"values"`
	}
	original := payload{Values: []json.Number{"9007199254740993e0", "0.1"}}
	called := false
	tool := ToolDefinition{Name: "typed", Parameters: map[string]any{"type": "object", "properties": map[string]any{"values": map[string]any{"type": "array", "items": map[string]any{"type": "number"}}}},
		ParseArguments: func(ToolCall) (any, error) { return original, nil },
		Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
			called = true
			if got, ok := in.Args.(payload); !ok || !reflect.DeepEqual(got, original) {
				t.Fatalf("typed execution args changed: %#v", in.Args)
			}
			return ToolResult{}, nil
		}}
	out := RunToolCall(t.Context(), ToolCall{Name: "typed", Arguments: json.RawMessage("{}")}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
	if !called || out.IsError {
		t.Fatalf("called=%v err=%v", called, out.Err)
	}
}
