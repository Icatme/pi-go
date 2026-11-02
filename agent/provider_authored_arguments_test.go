package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Icatme/pi-go/pkg/pigo"
	"reflect"
	"strings"
	"testing"
)

func authoredArgumentFixture() (pigo.ToolCall, ToolCall) {
	source := pigo.ToolCall{ID: "wire-1", Name: "lookup", RawArguments: ` { "id" : 9007199254740993 } `, Arguments: map[string]any{"id": json.Number("9007199254740993")}}
	return source, convertPigoToolCall(source)
}
func authoredArgumentRoundTrip(call ToolCall) pigo.ToolCall {
	m := convertAssistantMessageToPigo(Message{Role: RoleAssistant, StopReason: StopReasonToolUse, ToolCalls: []ToolCall{call}}).(pigo.AssistantMessage)
	return m.Content[0].(pigo.ToolCall)
}

func TestProviderAuthoredArgumentsPreserveValuesNotWireEvidence(t *testing.T) {
	_, call := authoredArgumentFixture()
	got := authoredArgumentRoundTrip(cloneToolCall(call))
	if got.RawArguments != "" || got.Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("value lost or original wire invented")
	}
	clone := cloneToolCall(call)
	clone.ParsedArgs["id"] = json.Number("9007199254740995")
	edited := authoredArgumentRoundTrip(clone)
	if edited.RawArguments != "" || edited.Arguments["id"] != json.Number("9007199254740995") || call.ParsedArgs["id"] != json.Number("9007199254740993") {
		t.Fatal("current edit or clone isolation lost")
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "providerEvidence") || strings.Contains(string(encoded), "RawArguments") {
		t.Fatal("private evidence persisted")
	}
	var restored ToolCall
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	got = authoredArgumentRoundTrip(restored)
	if got.RawArguments != "" || got.Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("typed restored author value lost or became original evidence")
	}
}

func TestProviderHistoryArgumentsFollowExecutionPrecedence(t *testing.T) {
	for _, call := range []ToolCall{
		{Arguments: json.RawMessage(`{"id":99}`), ParsedArgs: map[string]any{}},
		{Arguments: json.RawMessage(`{"id":99}`), ParsedArgs: map[string]any{"id": json.Number("1")}},
		{Arguments: json.RawMessage(`{"id":9007199254740993}`)},
	} {
		executed, err := parseToolArguments(ToolDefinition{}, call)
		if err != nil {
			t.Fatal(err)
		}
		history := toolCallArgumentsMap(call)
		if !reflect.DeepEqual(history, executed) {
			t.Fatalf("history differs from ordinary execution: %#v %#v", history, executed)
		}
		if authoredArgumentRoundTrip(call).RawArguments != "" {
			t.Fatal("authored JSON promoted to original wire")
		}
	}
	for _, raw := range []string{`{"id":1} {}`, `[]`, `null`, `{"x":"\ud800"}`, `bad`} {
		if got := toolCallArgumentsMap(ToolCall{Arguments: json.RawMessage(raw)}); got != nil {
			t.Fatal("invalid/non-object history accepted")
		}
	}
}

func TestProviderAuthoredArgumentsDoNotPersistDiscardedProviderRaw(t *testing.T) {
	source := pigo.ToolCall{ID: "wire", Name: "lookup", Arguments: map[string]any{"value": "kept"}, RawArguments: `{"value":"SYNTHETIC_DISCARDED","value":"kept"}`}
	call := convertPigoToolCall(source)
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SYNTHETIC_DISCARDED") || authoredArgumentRoundTrip(call).RawArguments != "" {
		t.Fatal("discarded raw changed public execution/persistence data")
	}
}
func TestProviderAuthoredArgumentsNeverBypassExecutionPolicy(t *testing.T) {
	for _, mode := range []string{"cancel", "permission", "gate"} {
		_, call := authoredArgumentFixture()
		ctx, cancel := context.WithCancel(context.Background())
		bodies, hooks := 0, 0
		options := RunToolCallOptions{Tools: []ToolDefinition{{Name: "lookup", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { bodies++; return ToolResult{}, nil }}}}
		switch mode {
		case "cancel":
			cancel()
		case "permission":
			options.CheckToolPermission = func(context.Context, BeforeToolCallContext) error { hooks++; return errors.New("denied fixture") }
		case "gate":
			options.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				hooks++
				return ToolGateResult{Action: ToolGateActionBlock, Reason: "denied fixture"}, nil
			}
		}
		out := RunToolCall(ctx, call, options)
		cancel()
		wantHooks := 1
		if mode == "cancel" {
			wantHooks = 0
		}
		if hooks != wantHooks || !out.IsError || bodies != 0 || out.Execution.Local != ToolLocalNotStarted {
			t.Fatalf("%s evidence bypassed gate", mode)
		}
	}
}

func TestProviderAuthoredEmptyEditSurvivesCloneAndJSON(t *testing.T) {
	call := ToolCall{ID: "empty", Name: "lookup", Arguments: json.RawMessage(`{"id":99}`), ParsedArgs: map[string]any{}}
	clone := cloneToolCall(call)
	encoded, err := json.Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	var restored ToolCall
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []ToolCall{call, clone, restored, cloneToolCall(restored)} {
		object := toolCallArgumentsMap(candidate)
		executed, err := parseToolArguments(ToolDefinition{}, candidate)
		if err != nil || object == nil || len(object) != 0 || !reflect.DeepEqual(object, executed) {
			t.Fatal("empty edit resurrected stale arguments")
		}
		got := authoredArgumentRoundTrip(candidate)
		if got.Arguments == nil || len(got.Arguments) != 0 || got.RawArguments != "" {
			t.Fatal("empty object became absent or original evidence")
		}
	}
}

func TestProviderAuthoredToolCallJSONCompatibility(t *testing.T) {
	for _, tc := range []struct {
		call ToolCall
		want string
	}{
		{ToolCall{}, `{"id":"","name":""}`},
		{ToolCall{ID: "i", Name: "n", ParsedArgs: map[string]any{}}, `{"id":"i","name":"n","parsed_args":{}}`},
		{ToolCall{ID: "i", Name: "n", ParsedArgs: map[string]any{"x": json.Number("9007199254740993")}}, `{"id":"i","name":"n","parsed_args":{"x":9007199254740993}}`},
	} {
		encoded, err := json.Marshal(tc.call)
		if err != nil || string(encoded) != tc.want {
			t.Fatalf("JSON mismatch: %s %v", encoded, err)
		}
	}
	// Do not introduce MarshalJSON: its promoted method would swallow outer fields.
	outer := struct {
		ToolCall
		Extra string `json:"extra"`
	}{ToolCall: ToolCall{ID: "i", Name: "n"}, Extra: "preserved"}
	encoded, err := json.Marshal(outer)
	if err != nil || string(encoded) != `{"id":"i","name":"n","extra":"preserved"}` {
		t.Fatalf("embedded JSON changed: %s %v", encoded, err)
	}
}

func TestProviderAuthoredChildEmptyEditNeverResurrectsArguments(t *testing.T) {
	executions := 0
	parent := ToolDefinition{Name: "parent", ResolveChildTools: func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
		return ChildToolResolution{Tools: []ToolDefinition{{Name: "leaf", Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
			executions++
			values, ok := in.Args.(map[string]any)
			if !ok || values == nil || len(values) != 0 {
				t.Error("child revived stale arguments")
			}
			return ToolResult{}, nil
		}}}}, nil
	}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		outcome := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf", Arguments: json.RawMessage(`{"stale":true}`), ParsedArgs: map[string]any{}})
		return outcome.Result, outcome.Err
	}}
	outcome := RunToolCall(t.Context(), ToolCall{Name: "parent"}, RunToolCallOptions{Tools: []ToolDefinition{parent}})
	if outcome.Err != nil || executions != 1 {
		t.Fatal(outcome.Err, executions)
	}
}
