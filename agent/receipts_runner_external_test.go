package agent_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/checkpoint"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/executionreceipt"
)

type receiptModelStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *receiptModelStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *receiptModelStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *receiptModelStream) Close() error                        { return nil }

// Exercise the public model-runner entry points rather than constructing a
// private journal context. In-memory capture must never suggest these durable
// or model-loop paths support effect recovery.
func TestReceiptsPublicRunnerBoundariesRejectCapture(t *testing.T) {
	for _, path := range []string{"agent runner", "checkpoint runner"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer sandbox.Close(context.Background())
			var probes, agentBodies, hostBodies, requests atomic.Int32
			leaf := agent.ToolDefinition{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				agentBodies.Add(1)
				return agent.ToolResult{}, nil
			}}
			probe := agent.ToolDefinition{Name: "probe", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				probes.Add(1)
				if executionreceipt.RequireCapture(ctx) == nil {
					t.Error("runner tool obtained capture")
				}
				if executionreceipt.ReportLocalCommit(ctx) {
					t.Error("runner tool reported a captured commit")
				}
				_, handle, err := agent.RunToolCallWithReceipts(ctx, agent.ToolCall{ID: "nested", Name: leaf.Name}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{leaf}}, 1)
				if err == nil || handle != nil {
					t.Errorf("nested agent capture: handle=%v error=%v", handle, err)
				}
				_, handle, err = sandbox.RunWithReceipts(ctx, "await tools.write({});", codemode.RunOptions{Tools: []codemode.Tool{{Name: "write", Invoke: func(context.Context, codemode.HostCall) (json.RawMessage, error) {
					hostBodies.Add(1)
					return json.RawMessage(`null`), nil
				}}}}, 1)
				if err == nil || handle != nil {
					t.Errorf("nested sandbox capture: handle=%v error=%v", handle, err)
				}
				return agent.ToolResult{}, nil
			}}
			definition := agent.AgentDefinition{Tools: []agent.ToolDefinition{probe}, Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				message := agent.NewTextMessage(agent.RoleAssistant, "done")
				message.StopReason = agent.StopReasonStop
				if requests.Add(1) == 1 {
					message.Parts = nil
					message.StopReason = agent.StopReasonToolUse
					message.ToolCalls = []agent.ToolCall{{ID: "probe-call", Name: probe.Name, Arguments: json.RawMessage(`{}`)}}
				}
				events := make(chan agent.AssistantEvent, 1)
				events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
				close(events)
				return &receiptModelStream{message: message, events: events}, nil
			})}
			prompts := []agent.Message{agent.NewUserTextMessage("probe receipt boundaries")}
			if path == "agent runner" {
				runner, err := agent.NewRunner(definition)
				if err != nil {
					t.Fatal(err)
				}
				stream := runner.Run(ctx, agent.AgentSnapshot{}, prompts)
				for range stream.Events() {
				}
				if _, err := stream.Wait(); err != nil {
					t.Fatal(err)
				}
			} else {
				runner, err := checkpoint.NewRunner(checkpoint.RunnerConfig{Definition: definition, DefinitionVersion: "receipt-boundary-v1", Store: checkpoint.NewMemoryStore()})
				if err != nil {
					t.Fatal(err)
				}
				stream := runner.Run(ctx, "receipt-boundary", agent.AgentSnapshot{}, prompts)
				for range stream.Events() {
				}
				outcome, err := stream.Wait()
				if err != nil {
					t.Fatal(err)
				}
				if !outcome.Persisted {
					t.Fatal("checkpoint path did not persist")
				}
			}
			if probes.Load() != 1 || requests.Load() != 2 || agentBodies.Load() != 0 || hostBodies.Load() != 0 {
				t.Fatalf("probes=%d model requests=%d agent bodies=%d sandbox bodies=%d", probes.Load(), requests.Load(), agentBodies.Load(), hostBodies.Load())
			}
		})
	}
}
