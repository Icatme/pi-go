package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestAgentDefinitionDefaultsToAutomaticTransport(t *testing.T) {
	definition, err := (AgentDefinition{}).Validate()
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if definition.Transport != TransportAuto {
		t.Fatalf("expected default transport %q, got %q", TransportAuto, definition.Transport)
	}
}

func TestThinkingMaxAndCachedTransportMapToProviderContracts(t *testing.T) {
	if got := toPigoThinkingLevel(ThinkingMax); got != pigo.ThinkingLevelMax {
		t.Fatalf("expected max reasoning mapping, got %q", got)
	}
	if got := toPigoTransport(TransportWebSocketCached); got != pigo.TransportWebSocketCached {
		t.Fatalf("expected cached websocket mapping, got %q", got)
	}
}

func TestAgentResetRejectsActiveRun(t *testing.T) {
	started := make(chan struct{})
	runtime, err := NewAgent(AgentDefinition{Model: &blockingModel{started: started}})
	if err != nil {
		t.Fatalf("NewAgent returned error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- runtime.PromptText(context.Background(), "keep")
	}()
	<-started

	if err := runtime.Reset(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("expected ErrAlreadyRunning, got %v", err)
	}
	state := runtime.State()
	if !state.IsStreaming || len(state.Messages) != 1 || state.Messages[0].Parts[0].Text != "keep" {
		t.Fatalf("active reset changed runtime state: %+v", state)
	}

	runtime.Abort()
	if err := <-done; err != nil {
		t.Fatalf("Prompt returned error after abort: %v", err)
	}
}

func TestEngineDoesNotExecuteToolCallsFromLengthTruncatedMessage(t *testing.T) {
	var (
		modelCalls         int
		executeCalls       atomic.Int32
		toolLifecycleCalls atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "one", Name: "write", Arguments: []byte(`{"value":"valid"}`)},
						{ID: "two", Name: "write", Arguments: []byte(`{"value":`)},
					},
					StopReason: StopReasonLength,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				Parts:      []Part{{Type: PartTypeText, Text: "recovered"}},
				StopReason: StopReasonStop,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "write",
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				executeCalls.Add(1)
				return ToolResult{}, nil
			},
		}},
	}

	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd {
			toolLifecycleCalls.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("expected no truncated tool calls to execute, got %d", executeCalls.Load())
	}
	if toolLifecycleCalls.Load() != 4 {
		t.Fatalf("expected start/end lifecycle for two truncated calls, got %d", toolLifecycleCalls.Load())
	}
	if modelCalls != 2 {
		t.Fatalf("expected model to retry after truncated results, got %d calls", modelCalls)
	}
	if len(next.Messages) != 5 {
		t.Fatalf("expected user, truncated assistant, two errors, and recovery, got %d messages", len(next.Messages))
	}
	for _, message := range next.Messages[2:4] {
		if message.ToolResult == nil || !message.ToolResult.IsError {
			t.Fatalf("expected synthetic error tool result, got %+v", message)
		}
	}
}

func TestEngineDoesNotExecuteToolCallsFromFailedAssistantMessages(t *testing.T) {
	tests := []struct {
		name   string
		reason StopReason
	}{
		{name: "error stop reason", reason: StopReasonError},
		{name: "aborted stop reason", reason: StopReasonAborted},
		{name: "error message without stop reason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var executed bool
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
					return newStaticAssistantStream(Message{
						Role:         RoleAssistant,
						ToolCalls:    []ToolCall{{ID: "residual", Name: "side_effect"}},
						StopReason:   tt.reason,
						ErrorMessage: "generation failed",
						Timestamp:    time.Now().UTC(),
					}, nil), nil
				}},
				Tools: []ToolDefinition{{
					Name: "side_effect",
					Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
						executed = true
						return ToolResult{}, nil
					},
				}},
			}

			next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if executed {
				t.Fatal("residual tool call from failed assistant message executed")
			}
			if len(next.Messages) != 3 || next.Messages[1].StopReason != tt.reason || next.Messages[2].ToolResult == nil || !next.Messages[2].ToolResult.IsError {
				t.Fatalf("unexpected failed transcript: %+v", next.Messages)
			}
		})
	}
}

func TestEngineValidatesToolArgumentsAndHookMutations(t *testing.T) {
	tests := []struct {
		name       string
		arguments  string
		beforeHook BeforeToolCallHook
	}{
		{name: "initial arguments", arguments: `{"value":{"invalid":true}}`},
		{
			name:      "mutated arguments",
			arguments: `{"value":"valid"}`,
			beforeHook: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
				input.Args.(map[string]any)["value"] = map[string]any{"invalid": true}
				return BeforeToolCallResult{}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				modelCalls int
				executed   bool
			)
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
					modelCalls++
					if modelCalls == 1 {
						return newStaticAssistantStream(Message{
							Role:       RoleAssistant,
							ToolCalls:  []ToolCall{{ID: "call", Name: "echo", Arguments: []byte(tt.arguments)}},
							StopReason: StopReasonToolUse,
							Timestamp:  time.Now().UTC(),
						}, nil), nil
					}
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
				}},
				Tools: []ToolDefinition{{
					Name: "echo",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{"type": "string"},
						},
						"required":             []any{"value"},
						"additionalProperties": false,
					},
					Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
						executed = true
						return ToolResult{}, nil
					},
				}},
				BeforeToolCall: tt.beforeHook,
			}

			next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if executed {
				t.Fatal("schema-invalid arguments reached the tool executor")
			}
			if next.Messages[2].ToolResult == nil || !next.Messages[2].ToolResult.IsError {
				t.Fatalf("expected validation error tool result, got %+v", next.Messages[2])
			}
		})
	}
}

func TestEngineSchemaCoercionIsTheExecutedValue(t *testing.T) {
	var (
		modelCalls int
		executed   any
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "count", Arguments: []byte(`{"value":"42"}`)}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "count",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "integer"}},
				"required":   []any{"value"},
			},
			Execute: func(_ context.Context, _ string, args any, _ ToolUpdateFunc) (ToolResult, error) {
				executed = args.(map[string]any)["value"]
				return ToolResult{}, nil
			},
		}},
	}
	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executed != int(42) {
		t.Fatalf("expected coerced numeric execution value, got %#v", executed)
	}
}

func TestEngineSchemaValidationKeepsCustomIntegerPrecision(t *testing.T) {
	const (
		maximum = int64(9007199254740992)
		actual  = int64(9007199254740993)
	)
	var (
		modelCalls int
		executed   bool
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "bounded"}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "bounded",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "integer", "maximum": maximum}},
			},
			ParseArguments: func(ToolCall) (any, error) {
				return map[string]any{"value": actual}, nil
			},
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				executed = true
				return ToolResult{}, nil
			},
		}},
	}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executed {
		t.Fatal("out-of-range custom integer reached executor")
	}
	if next.Messages[2].ToolResult == nil || !next.Messages[2].ToolResult.IsError {
		t.Fatalf("expected precision-safe validation error, got %+v", next.Messages[2])
	}
}

func TestEngineSchemaValidationKeepsRawIntegerPrecision(t *testing.T) {
	const (
		maximum = int64(9007199254740992)
		actual  = int64(9007199254740993)
	)
	var (
		modelCalls int
		executed   bool
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "bounded", Arguments: []byte(`{"value":9007199254740993}`)}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "bounded",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "integer", "maximum": maximum}},
			},
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				executed = true
				return ToolResult{}, nil
			},
		}},
	}

	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executed {
		t.Fatal("out-of-range raw integer reached executor")
	}
	if next.Messages[2].ToolResult == nil || !next.Messages[2].ToolResult.IsError || actual <= maximum {
		t.Fatalf("expected precision-safe raw validation error, got %+v", next.Messages[2])
	}
}

func TestEngineRawIntegerConversionIsBounded(t *testing.T) {
	const exact = uint64(9223372036854775808)
	var (
		modelCalls int
		executed   any
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "integer", Arguments: []byte(`{"value":9223372036854775808}`)}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "integer",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "integer"}},
			},
			Execute: func(_ context.Context, _ string, args any, _ ToolUpdateFunc) (ToolResult, error) {
				executed = args.(map[string]any)["value"]
				return ToolResult{}, nil
			},
		}},
	}

	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if value, ok := executed.(uint64); !ok || value != exact {
		t.Fatalf("expected exact uint64 execution value, got %#v", executed)
	}
}

func TestAgentDefinitionRejectsInvalidToolSchemas(t *testing.T) {
	invalid := ToolDefinition{
		Name:       "remote",
		Parameters: map[string]any{"$ref": "https://example.invalid/schema.json"},
	}
	if _, err := NewAgent(AgentDefinition{Model: staticModel{}, Tools: []ToolDefinition{invalid}}); err == nil {
		t.Fatal("expected static invalid schema to fail agent construction")
	}

	var modelCalls int
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			modelCalls++
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		ToolResolver: func(context.Context, AgentSnapshot) ([]ToolDefinition, error) {
			return []ToolDefinition{invalid}, nil
		},
	}
	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err == nil {
		t.Fatal("expected dynamic invalid schema to fail the run")
	}
	if modelCalls != 0 {
		t.Fatalf("invalid resolved schema reached model stream %d times", modelCalls)
	}
}

func TestEngineCustomArgumentParserRunsOnceWithBeforeHook(t *testing.T) {
	type customArgs struct {
		Value string `json:"value"`
	}
	var (
		modelCalls int
		parseCalls int
		executed   bool
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "custom"}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "custom",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []any{"value"},
			},
			ParseArguments: func(ToolCall) (any, error) {
				parseCalls++
				return &customArgs{Value: "parsed"}, nil
			},
			Execute: func(_ context.Context, _ string, args any, _ ToolUpdateFunc) (ToolResult, error) {
				executed = args.(*customArgs).Value == "canonical"
				return ToolResult{}, nil
			},
		}},
		BeforeToolCall: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
			input.Args.(*customArgs).Value = "canonical"
			return BeforeToolCallResult{}, nil
		},
	}

	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if parseCalls != 1 {
		t.Fatalf("expected one custom parse, got %d", parseCalls)
	}
	if !executed {
		t.Fatal("custom parsed arguments did not reach executor")
	}
}

func TestAgentToolSchemasAreDeeplyIsolated(t *testing.T) {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string", "enum": []string{"a", "b"}},
		},
	}
	runtime, err := NewAgent(AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Parameters: parameters}},
	})
	if err != nil {
		t.Fatalf("NewAgent returned error: %v", err)
	}

	parameters["properties"].(map[string]any)["value"].(map[string]any)["type"] = "number"
	state := runtime.State()
	valueSchema := state.Tools[0].Parameters["properties"].(map[string]any)["value"].(map[string]any)
	if valueSchema["type"] != "string" {
		t.Fatalf("constructor schema mutation leaked into agent: %+v", valueSchema)
	}
	valueSchema["enum"].([]string)[0] = "mutated"
	state = runtime.State()
	valueSchema = state.Tools[0].Parameters["properties"].(map[string]any)["value"].(map[string]any)
	if valueSchema["enum"].([]string)[0] != "a" {
		t.Fatalf("state schema mutation leaked into agent: %+v", valueSchema)
	}
}

func TestEngineToolResultDetailsAreIsolatedAcrossHooksEventsAndTranscript(t *testing.T) {
	type resultDetails struct {
		Labels map[string]string
	}
	var modelCalls int
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "details"}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "details", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			return ToolResult{Details: &resultDetails{Labels: map[string]string{"source": "tool"}}}, nil
		}}},
		AfterToolCall: func(_ context.Context, input AfterToolCallContext) (AfterToolCallResult, error) {
			input.Result.Details.(*resultDetails).Labels["hook"] = "mutated"
			return AfterToolCallResult{}, nil
		},
	}

	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventToolExecutionEnd && event.ToolResult != nil {
			event.ToolResult.Details.(*resultDetails).Labels["listener"] = "mutated"
		}
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	details := next.Messages[2].ToolResult.Details.(*resultDetails)
	if len(details.Labels) != 1 || details.Labels["source"] != "tool" {
		t.Fatalf("hook or listener mutation leaked into transcript: %+v", details.Labels)
	}
}

func TestCloneAnyPreservesSelfReferentialMaps(t *testing.T) {
	original := map[string]any{}
	original["self"] = original
	cloned := cloneAny(original).(map[string]any)
	clonedSelf := cloned["self"].(map[string]any)
	clonedSelf["value"] = "clone"
	if original["value"] != nil {
		t.Fatalf("clone mutation leaked into original: %+v", original)
	}
	if cloned["value"] != "clone" {
		t.Fatalf("clone did not preserve its self reference: %+v", cloned)
	}
}

func TestCloneAnyPreservesDistinctSliceViews(t *testing.T) {
	type sliceViews struct {
		Short []int
		Long  []int
	}
	backing := []int{1, 2, 3}
	original := sliceViews{Short: backing[:1], Long: backing[:2]}
	cloned := cloneAny(original).(sliceViews)
	if len(cloned.Short) != 1 || len(cloned.Long) != 2 || cloned.Long[1] != 2 {
		t.Fatalf("slice views were corrupted during clone: %+v", cloned)
	}
	cloned.Long[0] = 99
	if original.Short[0] != 1 {
		t.Fatalf("clone shared its backing array with the original: %+v", original)
	}
}

func TestEngineIgnoresToolUpdatesAfterExecutionSettles(t *testing.T) {
	var (
		modelCalls int
		lateUpdate ToolUpdateFunc
		updates    atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "echo",
			Execute: func(_ context.Context, _ string, _ any, update ToolUpdateFunc) (ToolResult, error) {
				lateUpdate = update
				update(ToolResult{Content: []Part{{Type: PartTypeText, Text: "accepted"}}})
				return ToolResult{}, nil
			},
		}},
	}

	_, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventToolExecutionUpdate {
			updates.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	lateUpdate(ToolResult{Content: []Part{{Type: PartTypeText, Text: "late"}}})
	if updates.Load() != 1 {
		t.Fatalf("expected only the in-flight update, got %d", updates.Load())
	}
}

func TestEngineAbortStopsLaterToolPreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var preflight []string
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{ID: "first", Name: "echo"},
					{ID: "second", Name: "echo"},
				},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			return ToolResult{}, nil
		}}},
		BeforeToolCall: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
			preflight = append(preflight, input.ToolCall.ID)
			cancel()
			return BeforeToolCallResult{}, nil
		},
	}

	next, err := NewEngine().Run(ctx, definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if len(preflight) != 1 || preflight[0] != "first" {
		t.Fatalf("expected only first preflight, got %v", preflight)
	}
	if len(next.PendingToolCalls) != 0 {
		t.Fatalf("expected pending calls to clear on abort, got %+v", next.PendingToolCalls)
	}
}

func TestEngineParallelAbortAfterSecondPreflightStartsNoToolBodies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		preflight []string
		executed  atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{ID: "first", Name: "echo"},
					{ID: "second", Name: "echo"},
				},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{}, nil
		}}},
		BeforeToolCall: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
			preflight = append(preflight, input.ToolCall.ID)
			if input.ToolCall.ID == "second" {
				cancel()
			}
			return BeforeToolCallResult{}, nil
		},
	}

	next, err := NewEngine().Run(ctx, definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if executed.Load() != 0 {
		t.Fatalf("expected no tool body after preflight cancellation, got %d", executed.Load())
	}
	if len(preflight) != 2 {
		t.Fatalf("expected cancellation during second preflight, got %v", preflight)
	}
	if len(next.Messages) != 4 || next.Messages[2].ToolResult == nil || next.Messages[3].ToolResult == nil {
		t.Fatalf("expected durable aborted results for prepared calls, got %+v", next.Messages)
	}
}

func TestEngineSequentialAbortDuringExecutionStopsLaterPreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var preflight []string
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{ID: "first", Name: "echo"},
					{ID: "second", Name: "echo"},
				},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(_ context.Context, id string, _ any, _ ToolUpdateFunc) (ToolResult, error) {
			if id == "first" {
				cancel()
			}
			return ToolResult{}, nil
		}}},
		ToolExecution: ToolExecutionSequential,
		BeforeToolCall: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
			preflight = append(preflight, input.ToolCall.ID)
			return BeforeToolCallResult{}, nil
		},
	}

	if _, err := NewEngine().Run(ctx, definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if len(preflight) != 1 || preflight[0] != "first" {
		t.Fatalf("expected later sequential preflight to be skipped, got %v", preflight)
	}
}

func TestEngineSequentialCancellationCannotBeHiddenByTerminate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{ID: "first", Name: "echo"},
					{ID: "second", Name: "echo"},
				},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		ToolExecution: ToolExecutionSequential,
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			cancel()
			return ToolResult{Terminate: true}, nil
		}}},
	}

	next, err := NewEngine().Run(ctx, definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation despite terminate result, got %v", err)
	}
	if len(next.Messages) != 4 || next.Messages[2].ToolResult == nil || next.Messages[3].ToolResult == nil {
		t.Fatalf("expected source-order tool results to remain durable, got %+v", next.Messages)
	}
}

func TestAgentPostTurnCancellationKeepsBalancedLifecycleAndInvocationWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		modelCalls int
		eventTypes []EventType
		agentEnd   []Message
	)
	runtime, err := NewAgent(AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "cancel"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "cancel", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			cancel()
			return ToolResult{}, nil
		}}},
	})
	if err != nil {
		t.Fatalf("NewAgent returned error: %v", err)
	}
	runtime.Subscribe(func(event AgentEvent) {
		eventTypes = append(eventTypes, event.Type)
		if event.Type == EventAgentEnd {
			agentEnd = cloneMessages(event.Messages)
		}
	})
	if err := runtime.PromptText(ctx, "run"); err != nil {
		t.Fatalf("PromptText returned error: %v", err)
	}
	if modelCalls != 1 {
		t.Fatalf("expected one model call, got %d", modelCalls)
	}
	snapshot := runtime.Snapshot()
	if len(snapshot.Messages) != 4 || snapshot.Messages[3].StopReason != StopReasonAborted {
		t.Fatalf("unexpected cancellation transcript: %+v", snapshot.Messages)
	}
	if len(agentEnd) != 4 {
		t.Fatalf("agent_end lost invocation messages: %+v", agentEnd)
	}
	var turnStarts, turnEnds int
	for _, eventType := range eventTypes {
		switch eventType {
		case EventTurnStart:
			turnStarts++
		case EventTurnEnd:
			turnEnds++
		}
	}
	if turnStarts != 2 || turnEnds != 2 {
		t.Fatalf("unbalanced cancellation lifecycle starts=%d ends=%d events=%v", turnStarts, turnEnds, eventTypes)
	}
}

func TestEngineWaitErrorAfterPartialStreamClosesMessageLifecycle(t *testing.T) {
	waitErr := errors.New("stream wait failed")
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			stream := newStaticAssistantStream(Message{}, []AssistantEvent{
				{Type: AssistantEventStart, Message: Message{Role: RoleAssistant, Timestamp: time.Now().UTC()}},
				{Type: AssistantEventTextDelta, Message: Message{Role: RoleAssistant, Parts: []Part{{Type: PartTypeText, Text: "partial"}}, Timestamp: time.Now().UTC()}, Delta: "partial"},
			})
			stream.err = waitErr
			return stream, nil
		}},
	}
	var eventTypes []EventType
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		eventTypes = append(eventTypes, event.Type)
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(next.Messages) != 2 || next.Messages[1].ErrorMessage != waitErr.Error() || next.Messages[1].StopReason != StopReasonError {
		t.Fatalf("unexpected failed stream transcript: %+v", next.Messages)
	}
	var messageStarts, messageEnds int
	for _, eventType := range eventTypes {
		switch eventType {
		case EventMessageStart:
			messageStarts++
		case EventMessageEnd:
			messageEnds++
		}
	}
	if messageStarts != 2 || messageEnds != 2 {
		t.Fatalf("unbalanced message lifecycle starts=%d ends=%d events=%v", messageStarts, messageEnds, eventTypes)
	}
}

func TestEnginePerToolSequentialModeForcesWholeBatchSequential(t *testing.T) {
	var (
		active    atomic.Int32
		maxActive atomic.Int32
		modelCall atomic.Int32
	)
	tool := ToolDefinition{
		Name:          "echo",
		ExecutionMode: ToolExecutionSequential,
		Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			current := active.Add(1)
			for {
				seen := maxActive.Load()
				if current <= seen || maxActive.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			return ToolResult{}, nil
		},
	}
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			if modelCall.Add(1) == 1 {
				return newStaticAssistantStream(Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "first", Name: "echo"},
						{ID: "second", Name: "echo"},
					},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools:         []ToolDefinition{tool},
		ToolExecution: ToolExecutionParallel,
	}

	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("expected sequential batch, observed %d concurrent tools", maxActive.Load())
	}
}

func TestEngineTerminateRequiresEveryToolResult(t *testing.T) {
	tests := []struct {
		name           string
		terminates     []bool
		wantModelCalls int
	}{
		{name: "all terminate", terminates: []bool{true, true}, wantModelCalls: 1},
		{name: "mixed", terminates: []bool{true, false}, wantModelCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var modelCalls int
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
					modelCalls++
					if modelCalls == 1 {
						return newStaticAssistantStream(Message{
							Role: RoleAssistant,
							ToolCalls: []ToolCall{
								{ID: "first", Name: "first"},
								{ID: "second", Name: "second"},
							},
							StopReason: StopReasonToolUse,
							Timestamp:  time.Now().UTC(),
						}, nil), nil
					}
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
				}},
				Tools: []ToolDefinition{
					{Name: "first", Execute: terminatingTool(tt.terminates[0])},
					{Name: "second", Execute: terminatingTool(tt.terminates[1])},
				},
			}

			if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if modelCalls != tt.wantModelCalls {
				t.Fatalf("expected %d model calls, got %d", tt.wantModelCalls, modelCalls)
			}
		})
	}
}

func TestEngineHooksCanSetTerminate(t *testing.T) {
	tests := []struct {
		name   string
		before BeforeToolCallHook
		after  AfterToolCallHook
	}{
		{
			name: "blocked call",
			before: func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error) {
				return BeforeToolCallResult{Block: true, Terminate: true}, nil
			},
		},
		{
			name: "after override",
			after: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
				terminate := true
				return AfterToolCallResult{Terminate: &terminate}, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var modelCalls int
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
					modelCalls++
					return newStaticAssistantStream(Message{
						Role:       RoleAssistant,
						ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
						StopReason: StopReasonToolUse,
						Timestamp:  time.Now().UTC(),
					}, nil), nil
				}},
				Tools:          []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
				BeforeToolCall: tt.before,
				AfterToolCall:  tt.after,
			}
			if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if modelCalls != 1 {
				t.Fatalf("expected hook terminate to stop automatic continuation, got %d calls", modelCalls)
			}
		})
	}
}

func TestEngineAfterHookContentOverridePreservesTerminate(t *testing.T) {
	var (
		modelCalls  int
		finalResult ToolResult
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			return ToolResult{Content: []Part{{Type: PartTypeText, Text: "original"}}, Terminate: true}, nil
		}}},
		AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
			return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "overridden"}}}}, nil
		},
	}

	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventToolExecutionEnd && event.ToolResult != nil {
			finalResult = cloneToolResult(*event.ToolResult)
		}
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if modelCalls != 1 {
		t.Fatalf("expected preserved terminate to stop continuation, got %d calls", modelCalls)
	}
	if next.Messages[2].ToolResult == nil || !finalResult.Terminate || len(finalResult.Content) != 1 || finalResult.Content[0].Text != "overridden" {
		t.Fatalf("unexpected merged tool result: %+v", finalResult)
	}
}

func TestEngineToolHookErrorsRemainPerCallAndPreserveSiblings(t *testing.T) {
	tests := []struct {
		name       string
		beforeHook BeforeToolCallHook
		afterHook  AfterToolCallHook
		wantBodies []string
	}{
		{
			name: "before hook",
			beforeHook: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
				if input.ToolCall.ID == "first" {
					return BeforeToolCallResult{}, errors.New("before failed")
				}
				return BeforeToolCallResult{}, nil
			},
			wantBodies: []string{"second"},
		},
		{
			name: "after hook",
			afterHook: func(_ context.Context, input AfterToolCallContext) (AfterToolCallResult, error) {
				if input.ToolCall.ID == "first" {
					return AfterToolCallResult{}, errors.New("after failed")
				}
				return AfterToolCallResult{}, nil
			},
			wantBodies: []string{"first", "second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				modelCalls int
				bodies     []string
				starts     []string
				ends       []string
			)
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
					modelCalls++
					if modelCalls == 1 {
						return newStaticAssistantStream(Message{
							Role: RoleAssistant,
							ToolCalls: []ToolCall{
								{ID: "first", Name: "echo"},
								{ID: "second", Name: "echo"},
							},
							StopReason: StopReasonToolUse,
							Timestamp:  time.Now().UTC(),
						}, nil), nil
					}
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
				}},
				ToolExecution: ToolExecutionSequential,
				Tools: []ToolDefinition{{Name: "echo", Execute: func(_ context.Context, id string, _ any, _ ToolUpdateFunc) (ToolResult, error) {
					bodies = append(bodies, id)
					return ToolResult{}, nil
				}}},
				BeforeToolCall: tt.beforeHook,
				AfterToolCall:  tt.afterHook,
			}

			next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
				switch event.Type {
				case EventToolExecutionStart:
					starts = append(starts, event.ToolCallID)
				case EventToolExecutionEnd:
					ends = append(ends, event.ToolCallID)
				}
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if len(next.Messages) != 5 || next.Messages[2].ToolResult == nil || !next.Messages[2].ToolResult.IsError || next.Messages[3].ToolResult == nil || next.Messages[3].ToolResult.IsError {
				t.Fatalf("unexpected source-order tool results: %+v", next.Messages)
			}
			if fmt.Sprint(bodies) != fmt.Sprint(tt.wantBodies) {
				t.Fatalf("expected tool bodies %v, got %v", tt.wantBodies, bodies)
			}
			if fmt.Sprint(starts) != "[first second]" || fmt.Sprint(ends) != "[first second]" {
				t.Fatalf("unexpected tool lifecycle starts=%v ends=%v", starts, ends)
			}
		})
	}
}

func TestEnginePreparesNextTurnBeforeStopDecision(t *testing.T) {
	var (
		primaryCalls   int
		replacementReq ModelRequest
		order          []string
	)
	replacement := staticModel{streamFn: func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		replacementReq = request
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
	}}
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			primaryCalls++
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
		PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			order = append(order, "prepare")
			if len(order) == 1 && (len(input.NewMessages) != 3 || input.NewMessages[0].Role != RoleUser) {
				t.Fatalf("unexpected new-message window: %+v", input.NewMessages)
			}
			level := ThinkingMax
			modelRef := ModelRef{Provider: "test", Model: "replacement"}
			updated := input.Context
			updated.SystemPrompt = "updated"
			return &AgentLoopTurnUpdate{
				Context:       &updated,
				Model:         replacement,
				ModelRef:      &modelRef,
				ThinkingLevel: &level,
			}, nil
		},
		ShouldStopAfterTurn: func(_ context.Context, input ShouldStopAfterTurnContext) (bool, error) {
			order = append(order, "stop")
			if input.Context.SystemPrompt != "updated" {
				t.Fatalf("stop hook did not observe prepared state: %+v", input)
			}
			return false, nil
		},
	}

	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if primaryCalls != 1 {
		t.Fatalf("expected primary model once, got %d", primaryCalls)
	}
	if len(order) < 2 || order[0] != "prepare" || order[1] != "stop" {
		t.Fatalf("expected prepare before stop, got %v", order)
	}
	if replacementReq.SystemPrompt != "updated" || replacementReq.ThinkingLevel != ThinkingMax || replacementReq.Model.Model != "replacement" {
		t.Fatalf("replacement request did not receive turn update: %+v", replacementReq)
	}
}

func TestEngineShouldStopAfterTurnPreventsAutomaticContinuation(t *testing.T) {
	var (
		modelCalls int
		queuePolls int
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
		ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) {
			return true, nil
		},
	}

	hooks := LoopHooks{
		GetSteeringMessages: func(context.Context) ([]Message, error) {
			queuePolls++
			return []Message{NewUserTextMessage("steer")}, nil
		},
		GetFollowUpMessages: func(context.Context) ([]Message, error) {
			queuePolls++
			return []Message{NewUserTextMessage("follow")}, nil
		},
	}
	if _, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, hooks); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if modelCalls != 1 {
		t.Fatalf("expected stop hook to prevent second model call, got %d", modelCalls)
	}
	if queuePolls != 1 {
		t.Fatalf("expected only the initial steering poll, got %d polls", queuePolls)
	}
}

func TestEngineTurnContextNewMessagesExcludesContinueHistory(t *testing.T) {
	existing := []Message{
		NewUserTextMessage("old request"),
		{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()},
		NewUserTextMessage("continue request"),
	}
	var observed []Message
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		ShouldStopAfterTurn: func(_ context.Context, input ShouldStopAfterTurnContext) (bool, error) {
			observed = cloneMessages(input.NewMessages)
			return true, nil
		},
	}
	if _, err := NewEngine().Continue(context.Background(), definition, &AgentSnapshot{Messages: existing}, nil); err != nil {
		t.Fatalf("Continue returned error: %v", err)
	}
	if len(observed) != 1 || observed[0].Role != RoleAssistant {
		t.Fatalf("expected only the new assistant message, got %+v", observed)
	}
}

func TestEngineTurnUpdateCanPruneContextWithoutLosingInvocationMessages(t *testing.T) {
	var (
		modelCalls int
		prepared   bool
		agentEnd   []Message
	)
	tool := ToolDefinition{Name: "echo", Execute: terminatingTool(false)}
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{tool},
		PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			if prepared {
				return nil, nil
			}
			prepared = true
			pruned := input.Context
			pruned.Messages = nil
			return &AgentLoopTurnUpdate{Context: &pruned}, nil
		},
	}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventAgentEnd {
			agentEnd = cloneMessages(event.Messages)
		}
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(next.Messages) != 4 || next.Messages[0].Role != RoleUser || next.Messages[3].Role != RoleAssistant {
		t.Fatalf("expected durable transcript to remain complete, got %+v", next.Messages)
	}
	if len(agentEnd) != 4 {
		t.Fatalf("expected all invocation artifacts in agent_end, got %+v", agentEnd)
	}
}

func TestEngineContinueContextPruningPreservesHistoryAndNewMessageWindow(t *testing.T) {
	history := []Message{
		NewUserTextMessage("old"),
		{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()},
		NewUserTextMessage("continue"),
	}
	var agentEnd []Message
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
		PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			pruned := cloneAgentContext(input.Context)
			pruned.Messages = []Message{NewUserTextMessage("compressed")}
			return &AgentLoopTurnUpdate{Context: &pruned}, nil
		},
		ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) {
			return true, nil
		},
	}

	next, err := NewEngine().Continue(context.Background(), definition, &AgentSnapshot{Messages: history}, func(event AgentEvent) {
		if event.Type == EventAgentEnd {
			agentEnd = cloneMessages(event.Messages)
		}
	})
	if err != nil {
		t.Fatalf("Continue returned error: %v", err)
	}
	if len(next.Messages) != 5 || next.Messages[0].Parts[0].Text != "old" || next.Messages[4].Role != RoleTool {
		t.Fatalf("context pruning changed durable history: %+v", next.Messages)
	}
	if len(agentEnd) != 2 || agentEnd[0].Role != RoleAssistant || agentEnd[1].Role != RoleTool {
		t.Fatalf("unexpected continue invocation window: %+v", agentEnd)
	}
}

func terminatingTool(terminate bool) ToolExecutorFunc {
	return func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
		return ToolResult{Terminate: terminate}, nil
	}
}

func TestEngineTurnOverridesSurviveDynamicDefinitionResolution(t *testing.T) {
	var (
		resolverCalls    int
		prepareCalls     int
		primaryCalls     int
		replacementCalls int
	)

	echo := ToolDefinition{Name: "echo", Execute: terminatingTool(false)}
	primary := staticModel{streamFn: func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		primaryCalls++
		if request.ThinkingLevel != ThinkingOff {
			t.Fatalf("primary request used unexpected thinking level %q", request.ThinkingLevel)
		}
		return newStaticAssistantStream(Message{
			Role:       RoleAssistant,
			ToolCalls:  []ToolCall{{ID: "primary", Name: "echo"}},
			StopReason: StopReasonToolUse,
			Timestamp:  time.Now().UTC(),
		}, nil), nil
	}}
	replacement := staticModel{streamFn: func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		replacementCalls++
		if request.ThinkingLevel != ThinkingMax {
			t.Fatalf("replacement request used thinking level %q", request.ThinkingLevel)
		}
		if request.SystemPrompt != "" {
			t.Fatalf("replacement request restored resolver prompt %q", request.SystemPrompt)
		}
		message := Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}
		if replacementCalls == 1 {
			message.ToolCalls = []ToolCall{{ID: "replacement", Name: "echo"}}
			message.StopReason = StopReasonToolUse
		}
		return newStaticAssistantStream(message, nil), nil
	}}

	prepare := func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
		prepareCalls++
		if prepareCalls != 1 {
			return nil, nil
		}
		context := cloneAgentContext(input.Context)
		context.SystemPrompt = ""
		thinking := ThinkingMax
		return &AgentLoopTurnUpdate{Context: &context, Model: replacement, ThinkingLevel: &thinking}, nil
	}

	runtime, err := NewAgent(
		AgentDefinition{Model: primary, SystemPrompt: "resolver prompt", Tools: []ToolDefinition{echo}},
		WithDefinitionResolver(func(context.Context, AgentSnapshot) (AgentDefinition, error) {
			resolverCalls++
			return AgentDefinition{
				Model:           primary,
				SystemPrompt:    "resolver prompt",
				ThinkingLevel:   ThinkingOff,
				Tools:           []ToolDefinition{echo},
				PrepareNextTurn: prepare,
			}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewAgent returned error: %v", err)
	}
	if err := runtime.PromptText(context.Background(), "run"); err != nil {
		t.Fatalf("PromptText returned error: %v", err)
	}
	if primaryCalls != 1 || replacementCalls != 2 {
		t.Fatalf("expected primary once and replacement twice, got %d and %d", primaryCalls, replacementCalls)
	}
	if resolverCalls != 3 {
		t.Fatalf("expected resolver to remain dynamic across three turns, got %d calls", resolverCalls)
	}
}

func TestEngineTurnContextToolsOverrideToolResolver(t *testing.T) {
	var (
		modelCalls int
		aExecuted  bool
		bExecuted  bool
		prepared   bool
	)
	toolA := ToolDefinition{
		Name: "tool_a",
		Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			aExecuted = true
			return ToolResult{}, nil
		},
	}
	toolB := ToolDefinition{
		Name: "tool_b",
		Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			bExecuted = true
			return ToolResult{}, nil
		},
	}
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			modelCalls++
			if len(request.Tools) != 1 {
				t.Fatalf("request %d exposed %d tools", modelCalls, len(request.Tools))
			}
			message := Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}
			switch modelCalls {
			case 1:
				if request.Tools[0].Name != toolA.Name {
					t.Fatalf("first request exposed %q", request.Tools[0].Name)
				}
				message.ToolCalls = []ToolCall{{ID: "truncated", Name: toolA.Name}}
				message.StopReason = StopReasonLength
			case 2:
				if request.Tools[0].Name != toolB.Name {
					t.Fatalf("second request exposed %q", request.Tools[0].Name)
				}
				message.ToolCalls = []ToolCall{{ID: "execute-b", Name: toolB.Name}}
				message.StopReason = StopReasonToolUse
			case 3:
				if request.Tools[0].Name != toolB.Name {
					t.Fatalf("third request exposed %q", request.Tools[0].Name)
				}
			}
			return newStaticAssistantStream(message, nil), nil
		}},
		ToolResolver: func(context.Context, AgentSnapshot) ([]ToolDefinition, error) {
			return []ToolDefinition{toolA}, nil
		},
		PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			if prepared {
				return nil, nil
			}
			prepared = true
			context := cloneAgentContext(input.Context)
			context.Tools = []ToolDefinition{toolB}
			return &AgentLoopTurnUpdate{Context: &context}, nil
		},
	}

	if _, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if aExecuted {
		t.Fatal("resolver tool executed despite the truncated first call")
	}
	if !bExecuted {
		t.Fatal("context replacement tool was not executed")
	}
	if modelCalls != 3 {
		t.Fatalf("expected three model calls, got %d", modelCalls)
	}
}

func TestEngineProgressGateSerializesSettlement(t *testing.T) {
	// This smaller race-focused case exercises a callback already in flight while
	// Execute returns. The tool end event must wait until that accepted callback
	// has finished emitting.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var modelCalls int
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(_ context.Context, _ ModelRequest) (AssistantStream, error) {
			modelCalls++
			if modelCalls == 1 {
				return newStaticAssistantStream(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "echo"}}, StopReason: StopReasonToolUse, Timestamp: time.Now().UTC()}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(_ context.Context, _ string, _ any, update ToolUpdateFunc) (ToolResult, error) {
			go update(ToolResult{})
			<-entered
			return ToolResult{}, nil
		}}},
	}

	var eventsMu sync.Mutex
	var events []EventType
	done := make(chan error, 1)
	go func() {
		_, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
			if event.Type == EventToolExecutionUpdate {
				once.Do(func() { close(entered) })
				<-release
			}
			eventsMu.Lock()
			events = append(events, event.Type)
			eventsMu.Unlock()
		})
		done <- err
	}()
	<-entered
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	updateIndex, endIndex := -1, -1
	for i, event := range events {
		if event == EventToolExecutionUpdate {
			updateIndex = i
		}
		if event == EventToolExecutionEnd {
			endIndex = i
		}
	}
	if updateIndex == -1 || endIndex == -1 || updateIndex > endIndex {
		t.Fatalf("expected accepted update before tool end, got %v", events)
	}
}
