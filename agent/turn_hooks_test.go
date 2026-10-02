package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestFinishTurnSchedulingAndEventOrder(t *testing.T) {
	for _, scheduling := range []string{"context-only", "tool", "steering", "follow-up"} {
		t.Run(scheduling, func(t *testing.T) {
			calls, prepared := 0, 0
			var order []string
			var steering, followUp []Message
			definition := AgentDefinition{
				Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
					calls++
					message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
					if calls == 1 {
						switch scheduling {
						case "tool":
							message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
							message.StopReason = StopReasonToolUse
						case "steering":
							steering = []Message{NewUserTextMessage("steer")}
						case "follow-up":
							followUp = []Message{NewUserTextMessage("follow")}
						}
					}
					return newStaticAssistantStream(message, nil), nil
				}),
				Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
				PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
					prepared++
					return nil, nil
				},
				FinishTurn: func(_ context.Context, turn AgentTurnContext) (AgentTurnDecision, error) {
					order = append(order, "finish")
					if turn.Message.ThinkingLevel != ThinkingOff {
						t.Fatalf("missing response thinking level: %+v", turn.Message)
					}
					if calls == 1 {
						return AgentTurnDecision{Action: TurnActionContinue}, nil
					}
					return AgentTurnDecision{Action: TurnActionEnd}, nil
				},
			}
			hooks := LoopHooks{
				GetSteeringMessages: func(context.Context) ([]Message, error) { return dequeueByMode(&steering, QueueModeOneAtATime), nil },
				GetFollowUpMessages: func(context.Context) ([]Message, error) { return dequeueByMode(&followUp, QueueModeOneAtATime), nil },
			}
			_, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
				if event.Type == EventTurnEnd {
					order = append(order, "turn_end")
				}
			}, hooks)
			if err != nil || calls != 2 || prepared != 1 || !reflect.DeepEqual(order, []string{"finish", "turn_end", "finish", "turn_end"}) {
				t.Fatalf("calls=%d prepared=%d order=%v err=%v", calls, prepared, order, err)
			}
		})
	}
}

func TestFinishTurnHardExitsIgnoreContinuation(t *testing.T) {
	for _, reason := range []StopReason{StopReasonError, StopReasonAborted} {
		t.Run(string(reason), func(t *testing.T) {
			calls, finished, prepared := 0, 0, 0
			definition := AgentDefinition{
				Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
					calls++
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: reason, ErrorMessage: "failed"}, nil), nil
				}),
				FinishTurn: func(_ context.Context, turn AgentTurnContext) (AgentTurnDecision, error) {
					finished++
					if turn.Message.StopReason != reason {
						t.Fatal(turn.Message)
					}
					return AgentTurnDecision{Action: TurnActionContinue}, nil
				},
				PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
					prepared++
					return nil, nil
				},
			}
			_, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
			if err != nil || calls != 1 || finished != 1 || prepared != 0 {
				t.Fatalf("calls=%d finish=%d prepare=%d err=%v", calls, finished, prepared, err)
			}
		})
	}
}

func TestPrepareRequestRunsAfterInputAndOncePerRequest(t *testing.T) {
	requests, resolved, prepared, emitted := 0, 0, 0, false
	definition := AgentDefinition{
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			requests++
			if request.SystemPrompt != "canonical" || request.ThinkingLevel != ThinkingHigh {
				t.Fatalf("request=%+v", request)
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
		ToolResolver: func(context.Context, AgentSnapshot) ([]ToolDefinition, error) { resolved++; return nil, nil },
		PrepareRequest: func(_ context.Context, request PrepareRequestContext) (*AgentLoopTurnUpdate, error) {
			prepared++
			if !emitted || len(request.Context.Messages) == 0 {
				t.Fatal("request preparation ran before input lifecycle")
			}
			request.Context.SystemPrompt = "canonical"
			level := ThinkingHigh
			return &AgentLoopTurnUpdate{Context: &request.Context, ThinkingLevel: &level}, nil
		},
		FinishTurn: func(context.Context, AgentTurnContext) (AgentTurnDecision, error) {
			if requests == 1 {
				return AgentTurnDecision{Action: TurnActionContinue}, nil
			}
			return AgentTurnDecision{}, nil
		},
	}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventMessageEnd && event.Message.Role == RoleUser {
			emitted = true
		}
	})
	if err != nil || requests != 2 || prepared != 2 || resolved != 1 || len(next.Messages) != 3 || next.SystemPrompt != "" {
		t.Fatalf("requests=%d prepare=%d resolve=%d next=%+v err=%v", requests, prepared, resolved, next, err)
	}
}

func TestPrepareRequestFailurePreservesAcceptedInput(t *testing.T) {
	failure := errors.New("prepare failed")
	definition := AgentDefinition{PrepareRequest: func(context.Context, PrepareRequestContext) (*AgentLoopTurnUpdate, error) { return nil, failure }}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("accepted")}, nil)
	if !errors.Is(err, failure) || len(next.Messages) != 1 || next.Messages[0].Parts[0].Text != "accepted" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestPeekQueuedMessagesPreservesPriorityAndIsolation(t *testing.T) {
	runtime, err := NewAgent(AgentDefinition{SteeringMode: QueueModeOneAtATime, FollowUpMode: QueueModeAll})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Steer(NewUserTextMessage("first"))
	runtime.Steer(NewUserTextMessage("second"))
	runtime.FollowUp(NewUserTextMessage("later"))
	peek := runtime.PeekQueuedMessages()
	peek[0].Parts[0].Text = "mutated"
	if next := runtime.PeekQueuedMessages(); len(next) != 1 || next[0].Parts[0].Text != "first" {
		t.Fatal(next)
	}
	runtime.ClearSteeringQueue()
	if next := runtime.PeekQueuedMessages(); len(next) != 1 || next[0].Parts[0].Text != "later" {
		t.Fatal(next)
	}
}

func TestRunToolCallPreservesStructuredErrorAndHooks(t *testing.T) {
	var order []string
	tool := ToolDefinition{Name: "work", Execute: func(_ context.Context, _ ToolExecutionContext) (ToolResult, error) {
		order = append(order, "execute")
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "failure"}}, StructuredContent: json.RawMessage(`{"code":42}`), IsError: true}, nil
	}}
	outcome := RunToolCall(context.Background(), ToolCall{ID: "call", Name: "work"}, RunToolCallOptions{Tools: []ToolDefinition{tool}, BeforeToolCall: func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error) {
		order = append(order, "before")
		return BeforeToolCallResult{}, nil
	}, AfterToolCall: func(_ context.Context, input AfterToolCallContext) (AfterToolCallResult, error) {
		order = append(order, "after")
		if !input.IsError {
			t.Fatal("explicit error lost")
		}
		return AfterToolCallResult{}, nil
	}})
	if !outcome.IsError || !outcome.Result.IsError || string(outcome.Result.StructuredContent) != `{"code":42}` || !reflect.DeepEqual(order, []string{"before", "execute", "after"}) {
		t.Fatalf("outcome=%+v order=%v", outcome, order)
	}
	message := NewToolResultMessage(outcome.ToolCall, outcome.Result, outcome.IsError)
	outcome.Result.StructuredContent[0] = 'x'
	if string(message.ToolResult.StructuredContent) != `{"code":42}` {
		t.Fatal("result aliases transcript")
	}
}

func TestAfterToolContentReplacementDropsStaleStructuredContent(t *testing.T) {
	base := ToolResult{Content: []Part{{Type: PartTypeText, Text: "old"}}, StructuredContent: json.RawMessage(`{"old":true}`)}
	tool := ToolDefinition{Name: "work", Execute: func(_ context.Context, _ ToolExecutionContext) (ToolResult, error) { return base, nil }}
	outcome := RunToolCall(context.Background(), ToolCall{ID: "call", Name: "work"}, RunToolCallOptions{Tools: []ToolDefinition{tool}, AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
		return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "new"}}}}, nil
	}})
	if outcome.IsError || outcome.Result.StructuredContent != nil || outcome.Result.Content[0].Text != "new" {
		t.Fatal(outcome)
	}
}

func TestPrepareRequestRejectsInvalidToolSchemaBeforeModelIO(t *testing.T) {
	called := false
	definition := AgentDefinition{
		Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
			called = true
			return newStaticAssistantStream(Message{Role: RoleAssistant}, nil), nil
		}),
		PrepareRequest: func(_ context.Context, input PrepareRequestContext) (*AgentLoopTurnUpdate, error) {
			input.Context.Tools = []ToolDefinition{{Name: "invalid", Parameters: map[string]any{"$ref": "https://example.invalid/schema.json"}}}
			return &AgentLoopTurnUpdate{Context: &input.Context}, nil
		},
	}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("accepted")}, nil)
	if err == nil || called || len(next.Messages) != 1 {
		t.Fatalf("called=%v snapshot=%+v err=%v", called, next, err)
	}
}

func TestFinishTurnOnCanceledCommittedResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	assistant := Message{Role: RoleAssistant, StopReason: StopReasonToolUse, ToolCalls: []ToolCall{{ID: "call", Name: "work"}}}
	snapshot := AgentSnapshot{Messages: []Message{NewUserTextMessage("run"), assistant}, PendingToolCalls: pendingToolCallsForAssistant(assistant)}
	setPendingToolControlState(&snapshot, 1, assistant)
	var order []string
	definition := AgentDefinition{
		Tools: []ToolDefinition{{Name: "work", Execute: func(_ context.Context, _ ToolExecutionContext) (ToolResult, error) {
			cancel()
			return ToolResult{Content: []Part{{Type: PartTypeText, Text: "committed"}}}, nil
		}}},
		FinishTurn: func(_ context.Context, turn AgentTurnContext) (AgentTurnDecision, error) {
			order = append(order, "finish")
			if len(turn.ToolResults) != 1 || ctx.Err() == nil {
				t.Fatalf("turn=%+v err=%v", turn, ctx.Err())
			}
			return AgentTurnDecision{Action: TurnActionContinue}, nil
		},
	}
	next, err := NewEngine().ResumePendingToolCallsWithHooks(ctx, definition, &snapshot, func(event AgentEvent) {
		if event.Type == EventTurnEnd {
			order = append(order, "turn_end")
		}
	}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(order, []string{"finish", "turn_end"}) || len(next.PendingToolCalls) != 0 || next.PendingToolControl != nil {
		t.Fatalf("order=%v snapshot=%+v err=%v", order, next, err)
	}
}

func TestFinishTurnContinuationUsesFollowUpRequest(t *testing.T) {
	for _, maxTurns := range []int{20, 2} {
		t.Run(fmt.Sprintf("max-turns-%d", maxTurns), func(t *testing.T) {
			calls := 0
			followUp := []Message{NewUserTextMessage("follow")}
			definition := AgentDefinition{Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
				calls++
				hasFollow := false
				for _, message := range request.Messages {
					if message.Role == RoleUser && message.Parts[0].Text == "follow" {
						hasFollow = true
					}
				}
				if hasFollow != (calls == 2) {
					t.Fatalf("call=%d follow=%v", calls, hasFollow)
				}
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
			}), FinishTurn: func(context.Context, AgentTurnContext) (AgentTurnDecision, error) {
				if calls == 1 {
					return AgentTurnDecision{Action: TurnActionContinue}, nil
				}
				return AgentTurnDecision{}, nil
			}, MaxTurns: maxTurns}
			_, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, LoopHooks{GetFollowUpMessages: func(context.Context) ([]Message, error) { return dequeueByMode(&followUp, QueueModeOneAtATime), nil }})
			if err != nil || calls != 2 || len(followUp) != 0 {
				t.Fatalf("calls=%d pending=%v err=%v", calls, followUp, err)
			}
		})
	}
}
