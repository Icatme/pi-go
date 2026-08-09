package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestDecodeStoredCheckpointIsStrict(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "unknown envelope field",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"strict","definition_version":"v1","revision":1,"status":"running","snapshot":{},"extra":true}`,
		},
		{
			name:    "unknown snapshot field",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"strict","definition_version":"v1","revision":1,"status":"running","snapshot":{"extra":true}}`,
		},
		{
			name:    "duplicate field",
			payload: `{"kind":"pi-go.agent.checkpoint","kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"strict","definition_version":"v1","revision":1,"status":"running","snapshot":{}}`,
		},
		{
			name:    "nested duplicate field",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"strict","definition_version":"v1","revision":1,"status":"running","snapshot":{"metadata":{"value":1,"value":2}}}`,
		},
		{
			name:    "trailing value",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"strict","definition_version":"v1","revision":1,"status":"running","snapshot":{}} {}`,
		},
		{
			name:    "malformed",
			payload: `{"kind":`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeStoredCheckpoint("strict", StoredCheckpoint{Revision: 1, Payload: []byte(test.payload)})
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("expected strict decode failure, got %v", err)
			}
		})
	}
}

func TestDecodeRejectsCaseInsensitiveFieldAliases(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "top-level case variant",
			payload: `{"Kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{}}`,
		},
		{
			name:    "top-level alias collision",
			payload: `{"kind":"pi-go.agent.checkpoint","Kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{}}`,
		},
		{
			name:    "nested snapshot case variant",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{"Messages":[]}}`,
		},
		{
			name:    "recursive message case variant",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{"messages":[{"Role":"user"}]}}`,
		},
		{
			name:    "tool call case variant",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{"messages":[{"role":"assistant","tool_calls":[{"ID":"call","name":"tool"}]}]}}`,
		},
		{
			name:    "tool call unknown field",
			payload: `{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{"messages":[{"role":"assistant","tool_calls":[{"id":"call","name":"tool","extra":true}]}]}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeStoredCheckpoint("case", StoredCheckpoint{Revision: 1, Payload: []byte(test.payload)})
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("expected exact field-name rejection, got %v", err)
			}
		})
	}

	metadataPayload := []byte(`{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"case","definition_version":"v1","revision":1,"status":"running","snapshot":{"metadata":{"Kind":1}}}`)
	if _, err := decodeStoredCheckpoint("case", StoredCheckpoint{Revision: 1, Payload: metadataPayload}); err != nil {
		t.Fatalf("map-owned metadata key was incorrectly treated as a struct alias: %v", err)
	}
}

func TestDecodeStoredCheckpointRejectsOversizePayload(t *testing.T) {
	payload := bytes.Repeat([]byte{' '}, MaxPayloadBytes+1)
	_, err := decodeStoredCheckpoint("oversize", StoredCheckpoint{Revision: 1, Payload: payload})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected payload limit error, got %v", err)
	}
}

func TestDecodeStoredCheckpointRejectsWrongStoreKeyAndExcessiveDepth(t *testing.T) {
	envelope := newEnvelope("checkpoint-a", "v1", agent.AgentSnapshot{})
	envelope.Revision = 1
	payload, err := encodeEnvelope(envelope)
	if err != nil {
		t.Fatalf("encode checkpoint A: %v", err)
	}
	if _, err := decodeStoredCheckpoint("checkpoint-b", StoredCheckpoint{Revision: 1, Payload: payload}); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("expected copied payload ID mismatch, got %v", err)
	}

	deep := strings.Repeat("[", maxJSONDepth+2) + "0" + strings.Repeat("]", maxJSONDepth+2)
	if _, err := decodeStoredCheckpoint("deep", StoredCheckpoint{Revision: 1, Payload: []byte(deep)}); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("expected nesting depth rejection, got %v", err)
	}
}

func TestCheckpointCodecNormalizesNumbersAndRejectsNonDurableValues(t *testing.T) {
	payload := []byte(`{"kind":"pi-go.agent.checkpoint","version":1,"checkpoint_id":"numbers","definition_version":"v1","revision":1,"status":"running","snapshot":{"metadata":{"large":9007199254740993}}}`)
	envelope, err := decodeStoredCheckpoint("numbers", StoredCheckpoint{Revision: 1, Payload: payload})
	if err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	number, ok := envelope.Snapshot.Metadata["large"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("number was not preserved with UseNumber: %T %v", envelope.Snapshot.Metadata["large"], envelope.Snapshot.Metadata["large"])
	}

	nondurable := newEnvelope("nondurable", "v1", agent.AgentSnapshot{Metadata: map[string]any{"channel": make(chan int)}})
	nondurable.Revision = 1
	if _, err := encodeEnvelope(nondurable); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("expected non-durable envelope rejection, got %v", err)
	}

	duplicateArguments := newEnvelope("duplicate-args", "v1", agent.AgentSnapshot{Messages: []agent.Message{{
		Role: agent.RoleAssistant,
		ToolCalls: []agent.ToolCall{{
			ID:        "call",
			Name:      "tool",
			Arguments: json.RawMessage(`{"value":1,"value":2}`),
		}},
	}}})
	duplicateArguments.Revision = 1
	if _, err := encodeEnvelope(duplicateArguments); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("expected duplicate generated key rejection before storage, got %v", err)
	}
}

func TestCheckpointCodecRejectsCorruptStateMachinePayloads(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*checkpointEnvelope)
	}{
		{
			name: "revision mismatch",
			mutate: func(envelope *checkpointEnvelope) {
				envelope.Revision = 2
			},
		},
		{
			name: "interrupted without interrupt",
			mutate: func(envelope *checkpointEnvelope) {
				envelope.Status = StatusInterrupted
			},
		},
		{
			name: "active decision overlap",
			mutate: func(envelope *checkpointEnvelope) {
				envelope.Status = StatusInterrupted
				envelope.Interrupts = []storedInterrupt{{
					Interrupt: Interrupt{
						ID:   "interrupt",
						Kind: InterruptKindToolApproval,
						Tool: ToolApprovalRequest{ToolCallID: "call", ToolName: "tool", Arguments: json.RawMessage(`{}`)},
					},
					Digest: "digest",
				}}
				envelope.Decisions = []storedDecision{{Digest: "digest", Decision: ResumeDecision{Action: DecisionActionApprove}}}
			},
		},
		{
			name: "mismatched interrupted batch",
			mutate: func(envelope *checkpointEnvelope) {
				envelope.Status = StatusInterrupted
				envelope.Snapshot = agent.AgentSnapshot{
					Messages: []agent.Message{{
						Role:      agent.RoleAssistant,
						ToolCalls: []agent.ToolCall{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}},
					}},
					PendingToolCalls: []agent.PendingToolCall{{ToolCallID: "other", ToolName: "tool"}},
				}
				envelope.Interrupts = []storedInterrupt{{
					Interrupt: Interrupt{
						ID:   "interrupt",
						Kind: InterruptKindToolApproval,
						Tool: ToolApprovalRequest{ToolCallID: "other", ToolName: "tool", Arguments: json.RawMessage(`{}`)},
					},
					Digest: "digest",
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := newEnvelope("corrupt", "v1", agent.AgentSnapshot{})
			envelope.Revision = 1
			test.mutate(&envelope)
			payload, marshalErr := json.Marshal(envelope)
			if marshalErr != nil {
				t.Fatalf("marshal corrupt envelope: %v", marshalErr)
			}
			_, err := decodeStoredCheckpoint("corrupt", StoredCheckpoint{Revision: 1, Payload: payload})
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("expected invalid checkpoint, got %v", err)
			}
		})
	}
}

func TestCloneOutcomeDoesNotFallBackToAliasedNonDurableSnapshot(t *testing.T) {
	outcome := Outcome{Snapshot: agent.AgentSnapshot{Metadata: map[string]any{"function": func() {}}}}
	cloned := cloneOutcome(outcome)
	if cloned.Snapshot.Metadata != nil {
		t.Fatalf("non-durable snapshot was returned by alias: %+v", cloned.Snapshot)
	}
}

func TestApprovalDigestBindsDefinitionVersion(t *testing.T) {
	call := agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}
	v1, err := makePendingApproval("v1", call, map[string]any{})
	if err != nil {
		t.Fatalf("v1 binding: %v", err)
	}
	v2, err := makePendingApproval("v2", call, map[string]any{})
	if err != nil {
		t.Fatalf("v2 binding: %v", err)
	}
	if v1.digest == v2.digest {
		t.Fatal("definition version was not bound into approval digest")
	}
}

func TestDecodeRecomputesApprovalBindingFromSnapshotAndDisplay(t *testing.T) {
	call := agent.ToolCall{ID: "call", OriginalID: "provider-call", Name: "tool", Arguments: json.RawMessage(`{"value":"one"}`)}
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{toolCallMessage(call)}}
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "binding",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(new(atomic.Int64))}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	interrupted, err, _ := awaitCheckpoint(runner.Run(context.Background(), "binding", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil {
		t.Fatalf("create valid interrupted checkpoint: %v", err)
	}
	if interrupted.Status != StatusInterrupted {
		t.Fatalf("unexpected valid outcome: %+v", interrupted)
	}
	record, err := store.Load(context.Background(), "binding")
	if err != nil {
		t.Fatalf("load valid interrupted checkpoint: %v", err)
	}
	envelope, err := decodeStoredCheckpoint("binding", record)
	if err != nil {
		t.Fatalf("decode valid interrupted checkpoint: %v", err)
	}

	envelope.Interrupts[0].Interrupt.Tool.Arguments = json.RawMessage(`{"value":"tampered"}`)
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal tampered binding: %v", err)
	}
	if _, err := decodeStoredCheckpoint("binding", StoredCheckpoint{Revision: record.Revision, Payload: payload}); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("tampered approval display passed binding validation: %v", err)
	}
}

func TestCanonicalJSONUsesStableObjectOrdering(t *testing.T) {
	canonical, err := canonicalJSON(map[string]any{"z": 1, "a": strings.Repeat("x", 2)})
	if err != nil {
		t.Fatalf("canonical JSON: %v", err)
	}
	if string(canonical) != `{"a":"xx","z":1}` {
		t.Fatalf("unexpected canonical JSON: %s", canonical)
	}
}
