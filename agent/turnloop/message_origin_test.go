package turnloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestInputAssistantFailureIsNotModelFailure(t *testing.T) {
	for _, delivery := range []Delivery{DeliveryNextRun, DeliverySteering, DeliveryFollowUp} {
		t.Run(string(delivery), func(t *testing.T) {
			model := &recordingModel{}
			loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 3})
			imported := Input{Delivery: delivery, Message: agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonError, ErrorMessage: "imported historical failure", Metadata: map[string]any{"model_response": true}, Payload: map[string]any{"model_response": true}}}
			inputs := []Input{imported, textInput(DeliveryNextRun, "next")}
			wantCalls := 2
			if delivery != DeliveryNextRun {
				inputs = append([]Input{textInput(DeliveryNextRun, "first")}, inputs...)
			}
			if delivery == DeliveryFollowUp {
				wantCalls = 3
			}
			admitBeforeRun(t, loop, inputs...)
			_ = loop.Stop(StopGraceful)
			result, err := waitLoop(t, loop)
			if err != nil || len(model.views()) != wantCalls || len(result.Unhandled) != 0 || result.Snapshot.Error != "" {
				t.Fatalf("err=%v calls=%d unhandled=%v snapshot.error=%q", err, len(model.views()), result.Unhandled, result.Snapshot.Error)
			}
		})
	}
}

func TestInputFailureDoesNotPollutePreparationError(t *testing.T) {
	cause := errors.New("prepare request failed")
	model := &recordingModel{}
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model, PrepareRequest: func(context.Context, agent.PrepareRequestContext) (*agent.AgentLoopTurnUpdate, error) {
		return nil, cause
	}}, QueueCapacity: 2})
	imported := Input{Delivery: DeliveryNextRun, Message: agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonError, ErrorMessage: "old unrelated failure"}}
	admitBeforeRun(t, loop, imported, textInput(DeliveryNextRun, "waiting"))
	result, err := waitLoop(t, loop)
	if !errors.Is(err, cause) || errors.Is(err, ErrAssistantFailure) || strings.Contains(err.Error(), "old unrelated failure") || len(model.views()) != 0 || len(result.Unhandled) != 1 {
		t.Fatalf("err=%v calls=%d unhandled=%v", err, len(model.views()), result.Unhandled)
	}
}

func TestTypedTerminalCauseDoesNotRequireModelOrigin(t *testing.T) {
	cause := errors.New("runtime failure")
	message := agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonError}
	if err := assistantFailure(agent.AgentEvent{Type: agent.EventMessageEnd, Message: &message, Err: cause}); !errors.Is(err, cause) {
		t.Fatalf("lost runtime cause: %v", err)
	}
}

func TestMessageOriginIsSetByEngineNotInputMetadata(t *testing.T) {
	model := &recordingModel{respond: func(_ context.Context, _ agent.ModelRequest, n int) (agent.AssistantStream, error) {
		if n > 1 {
			return newImmediateStream(assistantText("done"), nil), nil
		}
		partial := assistantText("working")
		events := make(chan agent.AssistantEvent, 2)
		events <- agent.AssistantEvent{Type: agent.AssistantEventStart, Message: partial}
		events <- agent.AssistantEvent{Type: agent.AssistantEventTextDelta, Message: partial, Delta: "working"}
		close(events)
		final := agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonToolUse, ToolCalls: []agent.ToolCall{{ID: "read-1", Name: "read", Arguments: json.RawMessage(`{}`)}}}
		return &immediateStream{events: events, final: final}, nil
	}}
	runner, err := agent.NewRunner(agent.AgentDefinition{Model: model, Tools: []agent.ToolDefinition{{Name: "read", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	input := agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonError, ErrorMessage: "old", Metadata: map[string]any{"model_response": true}, Payload: map[string]any{"model_response": true}}
	stream := runner.Run(t.Context(), agent.AgentSnapshot{}, []agent.Message{input})
	starts, updates, ends, oldEvents, toolEvents := 0, 0, 0, 0, 0
	for event := range stream.Events() {
		if event.Message == nil {
			continue
		}
		if event.Message.ErrorMessage == "old" {
			oldEvents++
			if event.ModelResponse {
				t.Error("input metadata forged model origin")
			}
			continue
		}
		if event.Message.Role == agent.RoleTool {
			toolEvents++
			if event.ModelResponse {
				t.Error("tool result marked as model response")
			}
			continue
		}
		if event.Message.Role != agent.RoleAssistant {
			continue
		}
		switch event.Type {
		case agent.EventMessageStart:
			starts++
		case agent.EventMessageUpdate:
			updates++
		case agent.EventMessageEnd:
			ends++
		default:
			continue
		}
		if !event.ModelResponse {
			t.Errorf("missing model origin on %s", event.Type)
		}
		raw, err := json.Marshal(event)
		if err != nil || !strings.Contains(string(raw), `"model_response":true`) {
			t.Errorf("model origin not serialized: %s %v", raw, err)
		}
	}
	_, err = stream.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if starts != 2 || ends != 2 || updates != 1 || oldEvents != 2 || toolEvents != 2 {
		t.Fatalf("starts=%d updates=%d ends=%d old=%d tool=%d", starts, updates, ends, oldEvents, toolEvents)
	}
}

func TestInputFailureDoesNotPolluteCancellation(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "parent"}[parent], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := make(chan struct{})
			model := &recordingModel{respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
				close(entered)
				return newCancelStream(ctx), nil
			}}
			loop := mustNewLoop(t, ctx, Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2})
			imported := Input{Delivery: DeliveryNextRun, Message: agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonError, ErrorMessage: "old unrelated failure"}}
			admitBeforeRun(t, loop, imported, textInput(DeliveryNextRun, "waiting"))
			<-entered
			if parent {
				cancel()
			} else {
				_ = loop.Stop(StopImmediate)
			}
			result, err := waitLoop(t, loop)
			if (!parent && err != nil) || (parent && !errors.Is(err, context.Canceled)) || errors.Is(err, ErrAssistantFailure) || len(result.Unhandled) != 1 || result.StopMode != StopImmediate {
				t.Fatalf("err=%v result=%+v", err, result)
			}
		})
	}
}
