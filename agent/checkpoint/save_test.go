package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

type singleUseJSON struct {
	calls int
}

func (value *singleUseJSON) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls != 1 {
		return nil, errors.New("value was serialized more than once")
	}
	return []byte(`{"large":9007199254740993}`), nil
}

func TestSaveReturnsIsolatedPersistedRepresentation(t *testing.T) {
	store := NewMemoryStore()
	runner := &Runner{store: store}
	value := &singleUseJSON{}
	envelope := newEnvelope("save-owned", "v1", agent.AgentSnapshot{Metadata: map[string]any{"value": value}})
	saved, err := runner.save(context.Background(), envelope.CheckpointID, 0, envelope)
	if err != nil {
		t.Fatal(err)
	}
	outcome := outcomeFromEnvelope(envelope.CheckpointID, saved)
	if value.calls != 1 {
		t.Fatalf("save/outcome serialized source value %d times, want once", value.calls)
	}
	nested := outcome.Snapshot.Metadata["value"].(map[string]any)
	if number, ok := nested["large"].(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("saved value lost numeric precision/type: %#v", nested)
	}
	record, err := store.Load(context.Background(), envelope.CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeStoredCheckpoint(envelope.CheckpointID, record)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, loaded) || saved.CheckpointID != envelope.CheckpointID || saved.Revision != 1 {
		t.Fatalf("saved envelope differs from persisted identity/representation: saved=%+v loaded=%+v", saved, loaded)
	}
	encoded, err := encodeEnvelope(saved)
	if err != nil || !bytes.Equal(encoded, record.Payload) {
		t.Fatalf("checkpoint format changed after save: %v", err)
	}
	envelope.Snapshot.Metadata["value"] = "caller mutation"
	nested["large"] = "outcome mutation"
	if got := saved.Snapshot.Metadata["value"].(map[string]any)["large"]; got != json.Number("9007199254740993") {
		t.Fatalf("caller/outcome mutation reached saved envelope: %v", got)
	}
}

func TestSaveRetainsToolCallUnicodeValidation(t *testing.T) {
	for _, call := range []agent.ToolCall{
		{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"code":"abc\ud800"}`)},
		{ID: "call", Name: "tool", ParsedArgs: map[string]any{"code": json.RawMessage(`"abc\ud800"`)}},
	} {
		envelope := newEnvelope("unicode", "v1", agent.AgentSnapshot{Messages: []agent.Message{{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{call}}}})
		envelope.Status = StatusCompleted
		runner := &Runner{store: NewMemoryStore()}
		if _, err := runner.save(context.Background(), envelope.CheckpointID, 0, envelope); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("saved malformed Unicode accepted: %v", err)
		}
	}
}

type revisionMismatchStore struct{ Store }

func (store revisionMismatchStore) CompareAndSwap(ctx context.Context, id CheckpointID, expected Revision, payload []byte) (StoredCheckpoint, error) {
	record, err := store.Store.CompareAndSwap(ctx, id, expected, payload)
	record.Revision++
	return record, err
}

func TestSaveRejectsMismatchedRevisionAcknowledgement(t *testing.T) {
	runner := &Runner{store: revisionMismatchStore{NewMemoryStore()}}
	envelope := newEnvelope("revision", "v1", agent.AgentSnapshot{})
	if _, err := runner.save(context.Background(), envelope.CheckpointID, 0, envelope); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("mismatched CAS revision accepted: %v", err)
	}
}

type changingJSON struct {
	calls int
}

func (value *changingJSON) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls == 1 {
		return []byte(`"original"`), nil
	}
	return []byte(`"changed"`), nil
}

func TestSaveRejectsChangedSerializedPendingBinding(t *testing.T) {
	store := NewMemoryStore()
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Model: &scriptedModel{responses: []agent.Message{toolCallMessage(agent.ToolCall{
				ID: "call", Name: "tool", ParsedArgs: map[string]any{"value": "original"},
			})}},
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				t.Fatal("tool executed before approval")
				return agent.ToolResult{}, nil
			}}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	outcome, err, _ := awaitCheckpoint(runner.Run(context.Background(), "changing", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil || outcome.Status != StatusInterrupted {
		t.Fatalf("create interrupted checkpoint: outcome=%+v err=%v", outcome, err)
	}
	record, err := store.Load(context.Background(), outcome.CheckpointID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeStoredCheckpoint(outcome.CheckpointID, record)
	if err != nil {
		t.Fatal(err)
	}
	value := &changingJSON{}
	tail := &envelope.Snapshot.Messages[len(envelope.Snapshot.Messages)-1]
	tail.ToolCalls[0].ParsedArgs["value"] = value
	if _, err := runner.save(context.Background(), envelope.CheckpointID, envelope.Revision, envelope); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("changed serialized batch binding accepted: %v", err)
	}
	if value.calls != 2 {
		t.Fatalf("fixture did not change between validation and serialization: %d calls", value.calls)
	}
}

// Includes checkpoint encoding, memory-store ownership copies, and local JSON
// normalization. It can be run unchanged on the baseline for comparison.
func BenchmarkCheckpointSave(b *testing.B) {
	for _, size := range []int{64, 64 << 10} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			store := NewMemoryStore()
			runner := &Runner{store: store}
			envelope := newEnvelope("benchmark", "v1", agent.AgentSnapshot{Metadata: map[string]any{"text": strings.Repeat("x", size)}})
			b.ReportAllocs()
			for b.Loop() {
				var err error
				envelope, err = runner.save(context.Background(), envelope.CheckpointID, envelope.Revision, envelope)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
