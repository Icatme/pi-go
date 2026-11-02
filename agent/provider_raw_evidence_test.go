package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func rawEvidenceFixture() (pigo.ToolCall, ToolCall) {
	original := pigo.ToolCall{ID: "wire-1", Name: "lookup", RawArguments: " { \"id\" : 9007199254740993, \"nested\":{\"v\":true} } ", Arguments: map[string]any{"id": json.Number("9007199254740993"), "nested": map[string]any{"v": true}}}
	return original, convertPigoToolCall(original)
}
func evidenceRoundTrip(call ToolCall) pigo.ToolCall {
	message := convertAssistantMessageToPigo(Message{Role: RoleAssistant, StopReason: StopReasonToolUse, ToolCalls: []ToolCall{call}}).(pigo.AssistantMessage)
	return message.Content[0].(pigo.ToolCall)
}

func TestProviderRawEvidenceRoundTripAndClone(t *testing.T) {
	original, call := rawEvidenceFixture()
	got := evidenceRoundTrip(cloneToolCall(call))
	if got.RawArguments != original.RawArguments || got.Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("wire or precise parsed evidence lost")
	}
	cloned := cloneToolCall(call)
	cloned.ParsedArgs["nested"].(map[string]any)["v"] = false
	if evidenceRoundTrip(cloned).RawArguments != "" || evidenceRoundTrip(call).RawArguments != original.RawArguments {
		t.Fatal("clone edit/isolation")
	}
	// Local ID rewrites also invalidate until explicit provider identity policy exists.
	call.ID = "local-alias"
	if evidenceRoundTrip(call).RawArguments != "" {
		t.Fatal("identity edit retained original evidence")
	}
	call.ID = original.ID
	if evidenceRoundTrip(call).RawArguments != original.RawArguments {
		t.Fatal("current-value binding changed into edit history")
	}
}

func TestProviderRawEvidenceEditsInvalidate(t *testing.T) {
	for i, edit := range []func(*ToolCall){
		func(c *ToolCall) { c.OriginalID = "changed" },
		func(c *ToolCall) { c.Name = "changed" },
		func(c *ToolCall) { c.Namespace = "changed" },
		func(c *ToolCall) { c.ThoughtSignature = "changed" },
		func(c *ToolCall) { c.Arguments = append(c.Arguments, ' ') },
		func(c *ToolCall) { c.ParsedArgs["id"] = float64(9007199254740993) },
		func(c *ToolCall) { delete(c.ParsedArgs, "id") },
		func(c *ToolCall) { c.ParsedArgs = nil },
	} {
		_, call := rawEvidenceFixture()
		edit(&call)
		if got := evidenceRoundTrip(call); got.RawArguments != "" {
			t.Fatalf("edit %d retained raw", i)
		}
	}
}

func TestProviderRawEvidencePersistenceAndDebugDoNotAddRaw(t *testing.T) {
	original, call := rawEvidenceFixture()
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), original.RawArguments) || strings.Contains(string(encoded), "providerEvidence") {
		t.Fatal("private wire persisted")
	}
	without := call
	without.providerEvidence = nil
	plain, _ := json.Marshal(without)
	if string(plain) != string(encoded) {
		t.Fatal("evidence changed persistence bytes")
	}
	var restored ToolCall
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.providerEvidence != nil || evidenceRoundTrip(restored).RawArguments != "" {
		t.Fatal("restored evidence invented")
	}
	// Unmarshalling into a previously populated value must erase old evidence.
	if err := json.Unmarshal(encoded, &call); err != nil || call.providerEvidence != nil {
		t.Fatal("stale evidence survives decode")
	}
	_, call = rawEvidenceFixture()
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, call), original.RawArguments) || strings.Contains(fmt.Sprintf(format, call), strconv.Quote(original.RawArguments)) {
			t.Fatal("debug adds private raw")
		}
	}
	if fmt.Sprint(call.providerEvidence) != "[provider evidence]" || fmt.Sprintf("%#v", call.providerEvidence) != "[provider evidence]" {
		t.Fatal("direct evidence debug")
	}
}

type rawEvidenceMarshaler struct{ calls *int }

func (v rawEvidenceMarshaler) MarshalJSON() ([]byte, error) { *v.calls++; return []byte("{}"), nil }

func TestProviderRawEvidenceBoundedAndNoCustomMethods(t *testing.T) {
	_, call := rawEvidenceFixture()
	methods := 0
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, parsed := range []map[string]any{
		{"custom": rawEvidenceMarshaler{&methods}}, cycle,
		{"huge": strings.Repeat("x", (1<<20)+1)},
		{"invalid": json.Number("true")},
	} {
		changed := call
		changed.ParsedArgs = parsed
		if replayProviderRawArguments(changed) != "" {
			t.Fatal("unsupported mutable tree accepted")
		}
	}
	if methods != 0 {
		t.Fatal("fingerprint invoked custom marshaler")
	}
	if captureProviderRawArguments(strings.Repeat(" ", (1<<20)+1), call) != nil || captureProviderRawArguments("[]", call) != nil || captureProviderRawArguments(`{"x":"\ud800"}`, call) != nil {
		t.Fatal("invalid raw captured")
	}
}

func TestProviderRawEvidenceAbsentKeepsLegacyData(t *testing.T) {
	source := pigo.ToolCall{ID: "legacy", Name: "tool", Arguments: map[string]any{"x": float64(1)}}
	call := convertPigoToolCall(source)
	if call.providerEvidence != nil {
		t.Fatal("invented wire")
	}
	got := evidenceRoundTrip(call)
	if got.RawArguments != "" || !reflect.DeepEqual(got.Arguments, source.Arguments) || string(call.Arguments) != `{"x":1}` {
		t.Fatal("legacy conversion changed")
	}
	// Empty object survives the existing top-level empty-map normalization.
	source.RawArguments = " { } "
	source.Arguments = map[string]any{}
	call = convertPigoToolCall(source)
	if evidenceRoundTrip(cloneToolCall(call)).RawArguments != source.RawArguments {
		t.Fatal("empty object evidence lost")
	}
}

func TestProviderRawEvidenceNeverBypassesExecutionPolicy(t *testing.T) {
	for _, mode := range []string{"cancel", "permission", "gate"} {
		_, call := rawEvidenceFixture()
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
