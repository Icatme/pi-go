package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestNextTurnPreparationOnlyWhenContinuing(t *testing.T) {
	for _, mode := range []string{"natural", "stop", "max-turns", "terminate"} {
		t.Run(mode, func(t *testing.T) {
			prepared := 0
			definition := AgentDefinition{
				Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
					message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
					if mode == "max-turns" || mode == "terminate" {
						message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
						message.StopReason = StopReasonToolUse
					}
					return newStaticAssistantStream(message, nil), nil
				}),
				Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(mode == "terminate")}},
				PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
					prepared++
					return nil, errors.New("unnecessary preparation")
				},
				ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) { return mode == "stop", nil },
				MaxTurns:            1,
			}
			next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
			if mode == "max-turns" {
				if !errors.Is(err, ErrMaxTurnsExceeded) {
					t.Fatalf("expected turn limit, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prepared != 0 {
				t.Fatalf("preparation ran %d times without a next model request", prepared)
			}
			if next == nil || len(next.Messages) < 2 {
				t.Fatalf("lost completed turn: %+v", next)
			}
		})
	}
}

func TestNextTurnPreparationFailureKeepsCompletedTurnBoundary(t *testing.T) {
	failure := errors.New("preparation failed")
	calls := 0
	var events []EventType
	definition := AgentDefinition{
		Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
			calls++
			return newStaticAssistantStream(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "echo"}}, StopReason: StopReasonToolUse}, nil), nil
		}),
		Tools:           []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
		PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) { return nil, failure },
	}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) { events = append(events, event.Type) })
	if !errors.Is(err, failure) || calls != 1 || len(next.Messages) != 3 {
		t.Fatalf("calls=%d snapshot=%+v error=%v", calls, next, err)
	}
	_, ended := engineRunErrorContext(err)
	if !ended || events[len(events)-1] != EventTurnEnd {
		t.Fatalf("preparation opened an unfinished turn: ended=%v events=%v", ended, events)
	}
}

func TestNextTurnPreparationPicksUpSteeringWithoutDoubleDequeue(t *testing.T) {
	for _, queuedBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "during-preparation", true: "already-pending"}[queuedBefore], func(t *testing.T) {
			var queue []Message
			var requests [][]string
			prepared := false
			definition := AgentDefinition{
				Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
					var users []string
					for _, message := range request.Messages {
						if message.Role == RoleUser {
							users = append(users, message.Parts[0].Text)
						}
					}
					requests = append(requests, users)
					message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
					if len(requests) == 1 {
						message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
						message.StopReason = StopReasonToolUse
						if queuedBefore {
							queue = append(queue, NewUserTextMessage("first"))
						}
					}
					return newStaticAssistantStream(message, nil), nil
				}),
				Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
				PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
					if !prepared {
						queue = append(queue, NewUserTextMessage("during"))
						prepared = true
					}
					return nil, nil
				},
				ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) { return len(requests) == 2, nil },
			}
			_, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, LoopHooks{GetSteeringMessages: func(context.Context) ([]Message, error) { return dequeueByMode(&queue, QueueModeOneAtATime), nil }})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"run", "during"}
			if queuedBefore {
				want = []string{"run", "first"}
			}
			if len(requests) != 2 || !reflect.DeepEqual(requests[1], want) {
				t.Fatalf("requests=%v want second=%v", requests, want)
			}
			if queuedBefore && (len(queue) != 1 || queue[0].Parts[0].Text != "during") {
				t.Fatalf("preparation consumed a second queued input: %+v", queue)
			}
		})
	}
}

func TestResumeNextTurnPreparationBoundary(t *testing.T) {
	for _, mode := range []string{"failure", "cancel", "steering"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			assistant := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "echo"}}, StopReason: StopReasonToolUse}
			snapshot := AgentSnapshot{Messages: []Message{NewUserTextMessage("run"), assistant}, PendingToolCalls: []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}}}
			setPendingToolControlState(&snapshot, 1, assistant)
			failure := errors.New("prepare failure")
			var queue []Message
			var events []EventType
			var order []string
			calls := 0
			definition := AgentDefinition{
				Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
					calls++
					if tail := request.Messages[len(request.Messages)-1]; tail.Role != RoleUser || tail.Parts[0].Text != "steer" {
						t.Fatalf("resume lost input queued during preparation: %+v", request.Messages)
					}
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
				}),
				Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
				ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) {
					order = append(order, "stop")
					return false, nil
				},
				PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
					order = append(order, "prepare")
					if len(input.NewMessages) != 1 || input.NewMessages[0].Role != RoleTool {
						t.Fatalf("resume preparation included earlier invocation: %+v", input.NewMessages)
					}
					switch mode {
					case "failure":
						return nil, failure
					case "cancel":
						cancel()
					case "steering":
						queue = append(queue, NewUserTextMessage("steer"))
					}
					return nil, nil
				},
			}
			next, err := NewEngine().ResumePendingToolCallsWithHooks(ctx, definition, &snapshot, func(event AgentEvent) { events = append(events, event.Type) }, LoopHooks{
				ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				},
				GetSteeringMessages: func(context.Context) ([]Message, error) { return dequeueByMode(&queue, QueueModeOneAtATime), nil },
			})
			if len(order) < 2 || order[0] != "stop" || order[1] != "prepare" {
				t.Fatalf("unexpected hook order: %v", order)
			}
			if mode == "steering" {
				if err != nil || calls != 1 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				return
			}
			want := failure
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls != 0 || len(next.Messages) != 3 || len(next.PendingToolCalls) != 0 {
				t.Fatalf("bad failure settlement: calls=%d snapshot=%+v error=%v", calls, next, err)
			}
			for _, event := range events {
				if event == EventTurnStart {
					t.Fatalf("failed preparation started a new turn: %v", events)
				}
			}
		})
	}
}

func TestPreparationRunsBeforeFollowUpRequest(t *testing.T) {
	var queue = []Message{NewUserTextMessage("follow-up")}
	calls, prepared := 0, 0
	definition := AgentDefinition{
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			calls++
			if calls == 2 && (prepared != 1 || request.Messages[len(request.Messages)-1].Parts[0].Text != "follow-up") {
				t.Fatalf("follow-up missed preparation: prepared=%d request=%+v", prepared, request)
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
		PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			prepared++
			return nil, nil
		},
	}
	_, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, LoopHooks{GetFollowUpMessages: func(context.Context) ([]Message, error) { return dequeueByMode(&queue, QueueModeOneAtATime), nil }})
	if err != nil || calls != 2 || prepared != 1 {
		t.Fatalf("calls=%d prepared=%d error=%v", calls, prepared, err)
	}
}

func TestNextTurnPreparationKeepsDequeuedInputs(t *testing.T) {
	for _, entry := range []string{"run", "continue", "resume"} {
		for _, queueKind := range []string{"steering", "follow-up"} {
			for _, outcome := range []string{"error", "cancel", "success"} {
				t.Run(entry+"/"+queueKind+"/"+outcome, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					failure := errors.New("preparation failed")
					var queue []Message
					enqueued, consumed, completed := false, false, false
					enqueue := func() {
						if !enqueued {
							queue = []Message{NewUserTextMessage("queued-first"), NewUserTextMessage("queued-second")}
							enqueued = true
						}
					}
					getter := func(context.Context) ([]Message, error) {
						messages := dequeueByMode(&queue, QueueModeOneAtATime)
						if len(messages) > 0 {
							consumed = true
						}
						return messages, nil
					}
					calls, prepared := 0, 0
					var request ModelRequest
					definition := AgentDefinition{
						Model: StreamFunc(func(_ context.Context, input ModelRequest) (AssistantStream, error) {
							calls++
							request = input
							if consumed {
								completed = true
							} else {
								enqueue()
							}
							return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
						}),
						Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
							enqueue()
							return ToolResult{}, nil
						}}},
						PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
							if consumed {
								prepared++
							}
							if consumed && outcome == "error" {
								return nil, failure
							}
							replacement := AgentContext{SystemPrompt: "temporary", Messages: []Message{NewUserTextMessage("temporary-context")}}
							if consumed && outcome == "cancel" {
								cancel()
							}
							return &AgentLoopTurnUpdate{Context: &replacement}, nil
						},
						ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) { return completed, nil },
					}
					snapshot := AgentSnapshot{SystemPrompt: "durable", Messages: []Message{NewUserTextMessage("root")}}
					hooks := LoopHooks{}
					if queueKind == "steering" {
						hooks.GetSteeringMessages = getter
					} else {
						hooks.GetFollowUpMessages = getter
					}
					var events []AgentEvent
					emit := func(event AgentEvent) { events = append(events, event) }
					var next *AgentSnapshot
					var err error
					switch entry {
					case "run":
						snapshot.Messages = nil
						next, err = NewEngine().RunWithHooks(ctx, definition, &snapshot, []Message{NewUserTextMessage("root")}, emit, hooks)
					case "continue":
						next, err = NewEngine().ContinueWithHooks(ctx, definition, &snapshot, emit, hooks)
					case "resume":
						assistant := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "echo"}}, StopReason: StopReasonToolUse}
						snapshot.Messages = append(snapshot.Messages, assistant)
						snapshot.PendingToolCalls = []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}}
						setPendingToolControlState(&snapshot, 1, assistant)
						hooks.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
							return ToolGateResult{Action: ToolGateActionAllow}, nil
						}
						next, err = NewEngine().ResumePendingToolCallsWithHooks(ctx, definition, &snapshot, emit, hooks)
					}
					expectedErr := failure
					if outcome == "cancel" {
						expectedErr = context.Canceled
					} else if outcome == "success" {
						expectedErr = nil
					}
					if !errors.Is(err, expectedErr) || prepared != 1 {
						t.Fatalf("error=%v prepared=%d", err, prepared)
					}
					if next == nil {
						t.Fatal("missing snapshot")
					}
					if got := next.SystemPrompt; got != "durable" {
						t.Fatalf("temporary system prompt leaked: %q", got)
					}
					var userTexts []string
					for _, message := range next.Messages {
						if message.Role == RoleUser {
							userTexts = append(userTexts, message.Parts[0].Text)
						}
					}
					if !reflect.DeepEqual(userTexts, []string{"root", "queued-first"}) {
						t.Fatalf("lost, duplicated, or replaced durable input: %v", userTexts)
					}
					if len(queue) != 1 || queue[0].Parts[0].Text != "queued-second" {
						t.Fatalf("consumed more than one queued input: %+v", queue)
					}
					starts, ends := 0, 0
					for _, event := range events {
						if event.Message != nil && event.Message.Role == RoleUser && len(event.Message.Parts) > 0 && event.Message.Parts[0].Text == "queued-first" {
							if event.Type == EventMessageStart {
								starts++
							}
							if event.Type == EventMessageEnd {
								ends++
							}
						}
					}
					if starts != 1 || ends != 1 {
						t.Fatalf("accepted input events starts=%d ends=%d", starts, ends)
					}
					if outcome == "success" || entry == "resume" {
						finalEvents, finalInputs := 0, 0
						for _, event := range events {
							if event.Type != EventAgentEnd {
								continue
							}
							finalEvents++
							for _, message := range event.Messages {
								if message.Role == RoleUser && message.Parts[0].Text == "queued-first" {
									finalInputs++
								}
							}
						}
						if finalEvents != 1 || finalInputs != 1 {
							t.Fatalf("agent_end events=%d accepted inputs=%d", finalEvents, finalInputs)
						}
					}
					if outcome == "success" {
						if request.SystemPrompt != "temporary" || len(request.Messages) != 2 || request.Messages[0].Parts[0].Text != "temporary-context" || request.Messages[1].Parts[0].Text != "queued-first" {
							t.Fatalf("next request lost override or input: %+v", request)
						}
					} else {
						newMessages, ended := engineRunErrorContext(err)
						if !ended || len(newMessages) == 0 || newMessages[len(newMessages)-1].Parts[0].Text != "queued-first" {
							t.Fatalf("error lost invocation input or completed boundary: %+v ended=%v", newMessages, ended)
						}
						expectedCalls := 1
						if entry == "resume" && queueKind == "steering" {
							expectedCalls = 0
						}
						if calls != expectedCalls {
							t.Fatalf("failed preparation made another request: %d", calls)
						}
						definition.PrepareNextTurn = nil
						continued, continueErr := NewEngine().Continue(context.Background(), definition, next, nil)
						if continueErr != nil {
							t.Fatal(continueErr)
						}
						var replayedUsers []string
						for _, message := range request.Messages {
							if message.Role == RoleUser {
								replayedUsers = append(replayedUsers, message.Parts[0].Text)
							}
						}
						if !reflect.DeepEqual(replayedUsers, []string{"root", "queued-first"}) || len(continued.Messages) != len(next.Messages)+1 {
							t.Fatalf("continuation duplicated input or restored temporary context: users=%v messages=%+v", replayedUsers, continued.Messages)
						}
					}
				})
			}
		}
	}
}

func TestAgentPreparationFailurePreservesSteering(t *testing.T) {
	var runtime *Agent
	model := StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
		runtime.Steer(NewUserTextMessage("keep me"))
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
	})
	var err error
	runtime, err = NewAgent(AgentDefinition{Model: model, PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
		return nil, errors.New("prepare failed")
	}})
	if err != nil {
		t.Fatal(err)
	}
	var ended []Message
	runtime.Subscribe(func(event AgentEvent) {
		if event.Type == EventAgentEnd {
			ended = append(ended, cloneMessages(event.Messages)...)
		}
	})
	if err := runtime.PromptText(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if !reflect.DeepEqual(ended, snapshot.Messages) {
		t.Fatalf("agent_end lost or duplicated accepted input: %+v", ended)
	}
	if len(snapshot.Messages) != 4 || snapshot.Messages[2].Role != RoleUser || snapshot.Messages[2].Parts[0].Text != "keep me" || snapshot.Messages[3].ErrorMessage != "prepare failed" {
		t.Fatalf("agent error settlement lost input order: %+v", snapshot.Messages)
	}
	if len(runtime.steeringQueue) != 0 {
		t.Fatalf("durable input remained queued: %+v", runtime.steeringQueue)
	}
}
