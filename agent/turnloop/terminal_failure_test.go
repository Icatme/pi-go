package turnloop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestAssistantFailurePreservesQueuedInput(t *testing.T) {
	for _, reason := range []agent.StopReason{agent.StopReasonError, agent.StopReasonAborted, agent.StopReasonStop} {
		for _, detail := range []string{"upstream failed", ""} {
			if reason == agent.StopReasonStop && detail == "" {
				continue
			}
			t.Run(string(reason)+"/"+detail, func(t *testing.T) {
				model := &recordingModel{respond: func(_ context.Context, _ agent.ModelRequest, n int) (agent.AssistantStream, error) {
					if n == 1 {
						return newImmediateStream(agent.Message{Role: agent.RoleAssistant, StopReason: reason, ErrorMessage: detail}, nil), nil
					}
					return newImmediateStream(assistantText("should not run"), nil), nil
				}}
				loop := mustNewLoop(t, context.Background(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2})
				admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
				if err := loop.Stop(StopGraceful); err != nil {
					t.Fatal(err)
				}
				result, err := waitLoop(t, loop)
				if !errors.Is(err, ErrAssistantFailure) || len(model.views()) != 1 || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"waiting"}) {
					t.Fatalf("err=%v requests=%d unhandled=%v", err, len(model.views()), inputTexts(result.Unhandled))
				}
				if result.StopMode != "" {
					t.Fatalf("failure mistaken for graceful stop: %q", result.StopMode)
				}
				if detail != "" && !strings.Contains(err.Error(), detail) {
					t.Fatal("lost provider detail")
				}
			})
		}
	}
}

func TestAssistantFailureSettlesToolResultsBeforeStopping(t *testing.T) {
	executions := 0
	model := &recordingModel{respond: func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error) {
		return newImmediateStream(agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonError, ToolCalls: []agent.ToolCall{{ID: "a", Name: "write", Arguments: json.RawMessage(`{}`)}}}, nil), nil
	}}
	loop := mustNewLoop(t, context.Background(), Config{Definition: agent.AgentDefinition{Model: model, Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		executions++
		return agent.ToolResult{}, nil
	}}}}, QueueCapacity: 2})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
	_ = loop.Stop(StopGraceful)
	result, err := waitLoop(t, loop)
	if !errors.Is(err, ErrAssistantFailure) || executions != 0 {
		t.Fatalf("err=%v executions=%d", err, executions)
	}
	tail := result.Snapshot.Messages[len(result.Snapshot.Messages)-1]
	if tail.ToolResult == nil || tail.ToolResult.ToolCallID != "a" || !tail.ToolResult.IsError {
		t.Fatalf("unsettled result: %+v", tail)
	}
	if len(result.Unhandled) != 1 {
		t.Fatal(result.Unhandled)
	}
}

func TestAssistantFailureIgnoresHistoryAndAllowsExplicitNewLoop(t *testing.T) {
	initial := agent.AgentSnapshot{Messages: []agent.Message{agent.NewUserTextMessage("old"), {Role: agent.RoleAssistant, StopReason: agent.StopReasonError, ErrorMessage: "historical failure"}}, Error: "historical failure"}
	model := &recordingModel{}
	loop := mustNewLoop(t, context.Background(), Config{Definition: agent.AgentDefinition{Model: model}, Initial: initial, QueueCapacity: 2})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "new"), textInput(DeliveryNextRun, "next"))
	_ = loop.Stop(StopGraceful)
	result, err := waitLoop(t, loop)
	if err != nil || len(model.views()) != 2 || result.Snapshot.Error != "" || len(result.Unhandled) != 0 {
		t.Fatalf("err=%v result=%+v calls=%d", err, result, len(model.views()))
	}
}

func TestAssistantFailurePreservesTypedStreamCause(t *testing.T) {
	cause := errors.New("stream transport failed")
	hookCause := errors.New("finish hook failed")
	for _, withHook := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "stream-and-hook"}[withHook], func(t *testing.T) {
			model := &recordingModel{respond: func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error) {
				return newImmediateStream(agent.Message{Role: agent.RoleAssistant}, cause), nil
			}}
			definition := agent.AgentDefinition{Model: model}
			if withHook {
				definition.FinishTurn = func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
					return agent.AgentTurnDecision{}, hookCause
				}
			}
			loop := mustNewLoop(t, context.Background(), Config{Definition: definition, QueueCapacity: 2})
			admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
			_ = loop.Stop(StopGraceful)
			result, err := waitLoop(t, loop)
			if !errors.Is(err, cause) || (withHook && !errors.Is(err, hookCause)) || len(result.Unhandled) != 1 {
				t.Fatalf("err=%v unhandled=%v", err, result.Unhandled)
			}
		})
	}
}

func TestAssistantFailureCancellationDoesNotHideIndependentError(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "parent"}[parent], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("independent provider failure")
			var loop *TurnLoop
			model := &recordingModel{respond: func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error) {
				if parent {
					cancel()
				} else {
					_ = loop.Stop(StopImmediate)
				}
				return newImmediateStream(agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonAborted}, cause), nil
			}}
			loop = mustNewLoop(t, ctx, Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2})
			admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
			result, err := waitLoop(t, loop)
			if !errors.Is(err, cause) || result.StopMode != StopImmediate || len(result.Unhandled) != 1 {
				t.Fatalf("err=%v result=%+v", err, result)
			}
			if parent && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost parent cause: %v", err)
			}
		})
	}
}

func TestAssistantFailureWithoutCauseIsNotErasedByLaterStop(t *testing.T) {
	var loop *TurnLoop
	model := &recordingModel{respond: func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error) {
		return newImmediateStream(agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonAborted}, nil), nil
	}}
	loop = mustNewLoop(t, context.Background(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2, OnEvent: func(event agent.AgentEvent) {
		if event.Type == agent.EventMessageEnd && event.Message != nil && event.Message.Role == agent.RoleAssistant {
			_ = loop.Stop(StopImmediate)
		}
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
	result, err := waitLoop(t, loop)
	if !errors.Is(err, ErrAssistantFailure) || result.StopMode != StopImmediate || len(result.Unhandled) != 1 {
		t.Fatalf("err=%v result=%+v", err, result)
	}
}

func TestAssistantFailureProviderDetailSurvivesCancellation(t *testing.T) {
	for _, reason := range []agent.StopReason{agent.StopReasonError, agent.StopReasonAborted} {
		t.Run(string(reason), func(t *testing.T) {
			var loop *TurnLoop
			model := &recordingModel{respond: func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error) {
				_ = loop.Stop(StopImmediate)
				return newImmediateStream(agent.Message{Role: agent.RoleAssistant, StopReason: reason, ErrorMessage: "provider overloaded"}, context.Canceled), nil
			}}
			loop = mustNewLoop(t, context.Background(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2})
			admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
			result, err := waitLoop(t, loop)
			if err == nil || !strings.Contains(err.Error(), "provider overloaded") || len(result.Unhandled) != 1 {
				t.Fatalf("err=%v unhandled=%v", err, result.Unhandled)
			}
		})
	}
}
