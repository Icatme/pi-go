package agent

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestGatedBatchValidatesAllArgumentsBeforeCallbacks(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	deep := map[string]any{}
	for range 10001 {
		deep = map[string]any{"nested": deep}
	}
	tests := []struct {
		name string
		call ToolCall
	}{
		{"malformed raw with valid parsed", ToolCall{Arguments: json.RawMessage(`{"value":`), ParsedArgs: map[string]any{"value": "valid"}}},
		{"multiple raw values", ToolCall{Arguments: json.RawMessage(`{} {}`)}},
		{"whitespace only raw", ToolCall{Arguments: json.RawMessage(` `)}},
		{"nonfinite parsed with valid raw", ToolCall{Arguments: json.RawMessage(`{}`), ParsedArgs: map[string]any{"value": math.Inf(1)}}},
		{"cyclic parsed", ToolCall{ParsedArgs: cycle}},
		{"excessive parsed depth", ToolCall{ParsedArgs: deep}},
		{"unsupported parsed", ToolCall{ParsedArgs: map[string]any{"value": make(chan int)}}},
		{"malformed nested parsed JSON", ToolCall{ParsedArgs: map[string]any{"value": json.RawMessage(`{"value":`)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := tt.call
			call.ID, call.Name = "invalid", "tool"
			assistant := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "valid", Name: "tool"}, call}}
			callbacks := 0
			tool := ToolDefinition{
				Name: "tool",
				ParseArguments: func(ToolCall) (any, error) {
					callbacks++
					return map[string]any{}, nil
				},
				Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
					callbacks++
					return ToolResult{}, nil
				},
			}
			gate := func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				callbacks++
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}
			snapshot := &AgentSnapshot{}
			batch, err := NewEngine().executeToolCallsGated(context.Background(), AgentDefinition{}, snapshot, assistant, []ToolDefinition{tool}, func(AgentEvent) { callbacks++ }, gate)
			if err == nil || !strings.Contains(err.Error(), "assistant tool call 1") {
				t.Fatalf("invalid sibling arguments were not rejected: %v", err)
			}
			if callbacks != 0 || len(batch.messages) != 0 || len(snapshot.PendingToolCalls) != 0 {
				t.Fatalf("invalid batch reached callbacks or changed state: callbacks=%d batch=%+v snapshot=%+v", callbacks, batch, snapshot)
			}
		})
	}
}

func TestPendingArgumentValidationAcceptsDurableValues(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, {}, []byte(` {"large":9007199254740993,"exponent":1e1000} `)} {
		assistant := Message{ToolCalls: []ToolCall{{Arguments: raw, ParsedArgs: map[string]any{
			"large": int64(9007199254740993), "exponent": json.Number("1e1000"),
			"nested": json.RawMessage(`{"z":2,"a":1}`),
		}}}}
		if err := validatePendingToolArguments(assistant); err != nil {
			t.Fatalf("durable arguments rejected: %v", err)
		}
		if _, err := pendingToolBindingDigest(assistant, 1); err != nil {
			t.Fatalf("accepted arguments cannot be bound: %v", err)
		}
	}
}

// This compares the validation-only operation with the former discarded-digest
// operation; it is not an end-to-end agent throughput benchmark.
func BenchmarkPendingToolArgumentPreflight(b *testing.B) {
	for _, size := range []int{64, 64 << 10} {
		value := strings.Repeat("x", size)
		raw, err := json.Marshal(map[string]any{"value": value})
		if err != nil {
			b.Fatal(err)
		}
		assistant := Message{ToolCalls: []ToolCall{{ID: "call", Name: "tool", Arguments: raw, ParsedArgs: map[string]any{"value": value}}}}
		for name, run := range map[string]func() error{
			"validate": func() error { return validatePendingToolArguments(assistant) },
			"binding":  func() error { _, err := pendingToolBindingDigest(assistant, 1); return err },
		} {
			b.Run(name+"/"+strconv.Itoa(size), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := run(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
