package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

type assessmentStream struct {
	events <-chan agent.AssistantEvent
	wait   func() (agent.Message, error)
	close  func() error
}

func (s assessmentStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s assessmentStream) Wait() (agent.Message, error)        { return s.wait() }
func (s assessmentStream) Close() error {
	if s.close != nil {
		return s.close()
	}
	return nil
}

func assessmentMessage(raw string) agent.Message {
	return agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonToolUse,
		ToolCalls: []agent.ToolCall{{ID: "decision", Name: "decide_task", Arguments: json.RawMessage(raw)}}}
}

func assessmentAnswer(message agent.Message) assessmentStream {
	events := make(chan agent.AssistantEvent)
	close(events)
	return assessmentStream{events: events, wait: func() (agent.Message, error) { return message, nil }}
}

func assessmentInput() TaskAssessmentInput {
	return TaskAssessmentInput{SessionID: "assessment-1", Input: "第一家详情", Candidates: []TaskCandidate{
		{ID: "first", Question: "第一家的详情", RootQuestion: "最近一周新增的客户", Status: "finished", CanAttach: true},
		{ID: "second", Question: "生成一份方案", Status: "running"},
	}}
}

func TestPiTaskAssessorIsolatedRequestAndOwnedValues(t *testing.T) {
	input := assessmentInput()
	wantPayload, _ := json.Marshal(input)
	want := TaskAssessment{Action: "attach", TaskID: "first", Workload: WorkloadConversation}
	tripwire := func() { t.Error("inherited host behavior") }
	def := agent.AgentDefinition{
		Name: "private-worker", SessionID: "private-session", SystemPrompt: "private business policy",
		DefaultModel:  agent.ModelRef{Provider: "fixture", Model: "light", ProviderConfig: agent.ProviderConfig{Headers: map[string]string{"X-Host": "original"}}, Metadata: map[string]any{"nested": map[string]any{"value": "original"}}},
		ThinkingLevel: agent.ThinkingLow, ThinkingBudgets: agent.ThinkingBudgets{agent.ThinkingLow: 128}, Transport: agent.TransportAuto,
		Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			tripwire()
			return agent.ToolResult{}, nil
		}}},
		ToolResolver: func(context.Context, agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
			tripwire()
			return nil, nil
		},
		ModelResolver: func(context.Context, agent.ModelRef, agent.AgentSnapshot) (agent.StreamModel, error) {
			tripwire()
			return nil, nil
		},
		TransformContext: func(context.Context, []agent.Message) ([]agent.Message, error) { tripwire(); return nil, nil },
		ConvertToLLM:     func(context.Context, []agent.Message) ([]agent.Message, error) { tripwire(); return nil, nil },
		PrepareRequest: func(context.Context, agent.PrepareRequestContext) (*agent.AgentLoopTurnUpdate, error) {
			tripwire()
			return nil, nil
		},
		PrepareNextTurn: func(context.Context, agent.PrepareNextTurnContext) (*agent.AgentLoopTurnUpdate, error) {
			tripwire()
			return nil, nil
		},
		OnProviderStreamEvent: func(json.RawMessage, agent.ModelRef) error { tripwire(); return nil },
		FinishTurn: func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
			tripwire()
			return agent.AgentTurnDecision{}, nil
		},
	}
	calls := 0
	def.Model = agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
		calls++
		if request.SystemPrompt != TaskAssessmentInstructions || request.SessionID != "assessment-1" || len(request.Messages) != 1 || request.Messages[0].Role != agent.RoleUser || messageText(request.Messages[0]) != string(wantPayload) || len(request.Tools) != 1 || request.Tools[0].Name != "decide_task" || request.Tools[0].Execute != nil || request.OnProviderStreamEvent != nil {
			t.Fatal("request inherited authority or changed input", request)
		}
		if request.Model.Model != "light" || request.Model.ProviderConfig.Headers["X-Host"] != "original" || request.Model.Metadata["nested"].(map[string]any)["value"] != "original" || request.ThinkingLevel != agent.ThinkingLow || request.ThinkingBudgets[agent.ThinkingLow] != 128 {
			t.Fatal("configured model or reasoning changed", request)
		}
		request.Model.ProviderConfig.Headers["X-Host"] = "provider mutation"
		request.Model.Metadata["nested"].(map[string]any)["value"] = "provider mutation"
		request.ThinkingBudgets[agent.ThinkingLow] = 999
		request.Tools[0].Parameters["type"] = "provider mutation"
		// Even mutation of the original host slice during Stream cannot expand
		// the membership/CanAttach facts captured before the inference.
		input.Candidates[0].ID, input.Candidates[0].CanAttach = "changed", false
		return assessmentAnswer(assessmentMessage(`{"action":"attach","task_id":"first","workload":"conversation"}`)), nil
	})
	assess, err := PiTaskAssessor(def, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	def.DefaultModel.ProviderConfig.Headers["X-Host"] = "host mutation"
	def.DefaultModel.Metadata["nested"].(map[string]any)["value"] = "host mutation"
	def.ThinkingBudgets[agent.ThinkingLow] = 256
	for range 2 {
		input = assessmentInput()
		got, err := assess(t.Context(), input)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
		got.TaskID = "result mutation"
	}
	if calls != 2 || def.DefaultModel.ProviderConfig.Headers["X-Host"] != "host mutation" || def.ThinkingBudgets[agent.ThinkingLow] != 256 {
		t.Fatal("caller configuration aliased", calls, def)
	}
}

func TestPiTaskAssessorActionsUseOnlyHostEligibility(t *testing.T) {
	for _, want := range []TaskAssessment{
		{Action: "new", Workload: WorkloadLight}, {Action: "new", Workload: WorkloadTask},
		{Action: "clarify", Workload: WorkloadConversation}, {Action: "attach", TaskID: "first", Workload: WorkloadTask},
		{Action: "status", TaskID: "second", Workload: WorkloadLight},
	} {
		raw, _ := json.Marshal(want)
		assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
			return assessmentAnswer(assessmentMessage(string(raw))), nil
		})}, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		input := assessmentInput()
		// The SDK must not encode any product's completed/sent status grammar.
		input.Candidates[0].Status = "host permits continuation"
		if got, err := assess(t.Context(), input); err != nil || got != want {
			t.Fatal(got, err)
		}
	}
}

func TestPiTaskAssessorRejectsMalformedProtocolWithoutRetry(t *testing.T) {
	cases := map[string]agent.Message{}
	for name, raw := range map[string]string{
		"unknown task":     `{"action":"attach","task_id":"missing","workload":"task"}`,
		"unavailable":      `{"action":"attach","task_id":"second","workload":"task"}`,
		"unknown status":   `{"action":"status","task_id":"missing","workload":"light"}`,
		"new target":       `{"action":"new","task_id":"first","workload":"light"}`,
		"clarify target":   `{"action":"clarify","task_id":"first","workload":"light"}`,
		"empty status":     `{"action":"status","task_id":"","workload":"light"}`,
		"unknown action":   `{"action":"execute","task_id":"","workload":"task"}`,
		"unknown workload": `{"action":"new","task_id":"","workload":"main"}`,
		"null":             `{"action":"new","task_id":null,"workload":"light"}`,
		"number":           `{"action":"new","task_id":0,"workload":"light"}`,
		"missing":          `{"action":"new","workload":"light"}`,
		"duplicate":        `{"action":"new","action":"clarify","task_id":"","workload":"light"}`,
		"extra":            `{"action":"new","task_id":"","workload":"light","answer":"done"}`,
		"case alias":       `{"Action":"new","task_id":"","workload":"light"}`,
		"trailing":         `{"action":"new","task_id":"","workload":"light"} {}`,
		"nested":           `{"action":"new","task_id":{},"workload":"light"}`,
		"nul":              `{"action":"attach","task_id":"first\u0000","workload":"light"}`,
		"oversize":         strings.Repeat(" ", 4097), "invalid UTF8": string([]byte{0xff}),
	} {
		cases[name] = assessmentMessage(raw)
	}
	cases["prose"] = agent.NewTextMessage(agent.RoleAssistant, "The task is done")
	for _, name := range []string{"duplicate tool", "business tool", "namespace", "empty call", "wrong role", "wrong stop"} {
		message := assessmentMessage(`{"action":"new","task_id":"","workload":"light"}`)
		switch name {
		case "duplicate tool":
			message.ToolCalls = append(message.ToolCalls, message.ToolCalls[0])
		case "business tool":
			message.ToolCalls[0].Name = "write"
		case "namespace":
			message.ToolCalls[0].Namespace = "business"
		case "empty call":
			message.ToolCalls[0].ID = ""
		case "wrong role":
			message.Role = agent.RoleUser
		case "wrong stop":
			message.StopReason = agent.StopReasonStop
		}
		cases[name] = message
	}
	for name, message := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				return assessmentAnswer(message), nil
			})}, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := assess(t.Context(), assessmentInput()); !errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) || calls != 1 {
				t.Fatal(got, err, calls)
			}
		})
	}
}

func TestPiTaskAssessorInputAndBudgetValidation(t *testing.T) {
	model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		t.Fatal("invalid input reached model")
		return nil, nil
	})
	for _, budget := range []int{-1, 0, 1023, (8 << 20) + 1} {
		if _, err := PiTaskAssessor(agent.AgentDefinition{Model: model}, budget); !errors.Is(err, ErrInvalidTaskAssessment) {
			t.Fatal(budget, err)
		}
	}
	if _, err := PiTaskAssessor(agent.AgentDefinition{}, 64<<10); !errors.Is(err, agent.ErrModelNotConfigured) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*TaskAssessmentInput){
		func(i *TaskAssessmentInput) { i.SessionID = "" }, func(i *TaskAssessmentInput) { i.SessionID = "id\x00" },
		func(i *TaskAssessmentInput) { i.Input = " " }, func(i *TaskAssessmentInput) { i.Input = "bad\x00" },
		func(i *TaskAssessmentInput) { i.Input = string([]byte{0xff}) }, func(i *TaskAssessmentInput) { i.Input = strings.Repeat("a", (1<<20)+1) },
		func(i *TaskAssessmentInput) { i.Candidates = make([]TaskCandidate, 129) },
		func(i *TaskAssessmentInput) { i.Candidates[1].ID = "first" }, func(i *TaskAssessmentInput) { i.Candidates[0].ID = strings.Repeat("a", 257) },
		func(i *TaskAssessmentInput) { i.Candidates[0].Question = "" }, func(i *TaskAssessmentInput) { i.Candidates[0].Question = strings.Repeat("q", (64<<10)+1) },
		func(i *TaskAssessmentInput) { i.Candidates[0].RootQuestion = "bad\x00" }, func(i *TaskAssessmentInput) { i.Candidates[0].Status = "" },
	} {
		input := assessmentInput()
		mutate(&input)
		if !errors.Is(input.Validate(), ErrInvalidTaskAssessment) {
			t.Fatal("Validate accepted invalid input")
		}
		assess, _ := PiTaskAssessor(agent.AgentDefinition{Model: model}, 64<<10)
		if got, err := assess(t.Context(), input); !errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) {
			t.Fatal(got, err)
		}
	}
	assess, _ := PiTaskAssessor(agent.AgentDefinition{Model: model}, 1024)
	if got, err := assess(t.Context(), assessmentInput()); !errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) {
		t.Fatal(got, err)
	}
	if _, err := assess(nil, assessmentInput()); !errors.Is(err, ErrInvalidTaskAssessment) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := assess(ctx, assessmentInput()); !errors.Is(err, context.Canceled) || errors.Is(err, ErrInvalidTaskAssessment) {
		t.Fatal(err)
	}
}

func TestPiTaskAssessorBudgetIncludesToolAndInputIsNotMutated(t *testing.T) {
	input := assessmentInput()
	before := assessmentInput()
	var size int
	model := agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
		encoded, _ := json.Marshal(struct {
			SystemPrompt string                 `json:"system_prompt"`
			Messages     []agent.Message        `json:"messages"`
			Tools        []agent.ToolDefinition `json:"tools"`
		}{request.SystemPrompt, request.Messages, request.Tools})
		size = len(encoded)
		return assessmentAnswer(assessmentMessage(`{"action":"new","task_id":"","workload":"light"}`)), nil
	})
	assess, _ := PiTaskAssessor(agent.AgentDefinition{Model: model}, 64<<10)
	if _, err := assess(t.Context(), input); err != nil || !reflect.DeepEqual(input, before) {
		t.Fatal(err, input)
	}
	// Timestamp serialization can differ by a few bytes between invocations.
	assess, _ = PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		t.Fatal("tool budget omitted")
		return nil, nil
	})}, size-128)
	if _, err := assess(t.Context(), input); !errors.Is(err, ErrInvalidTaskAssessment) {
		t.Fatal(err)
	}
}

func TestPiTaskAssessorProviderErrorsPreservedAndStreamClosed(t *testing.T) {
	failure := errors.New("provider failure")
	for _, boundary := range []string{"stream", "wait", "close"} {
		t.Run(boundary, func(t *testing.T) {
			calls, closes, waits := 0, 0, 0
			model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				if boundary == "stream" {
					return nil, failure
				}
				s := assessmentAnswer(assessmentMessage(`{"action":"new","task_id":"","workload":"light"}`))
				s.wait = func() (agent.Message, error) {
					waits++
					if boundary == "wait" {
						return agent.Message{}, failure
					}
					return assessmentMessage(`{"action":"new","task_id":"","workload":"light"}`), nil
				}
				s.close = func() error {
					closes++
					if boundary == "close" {
						return failure
					}
					return nil
				}
				return s, nil
			})
			assess, _ := PiTaskAssessor(agent.AgentDefinition{Model: model}, 64<<10)
			if got, err := assess(t.Context(), assessmentInput()); !errors.Is(err, failure) || errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) || calls != 1 {
				t.Fatal(got, err, calls)
			}
			if boundary != "stream" && (closes != 1 || waits != 1) {
				t.Fatal(closes, waits)
			}
		})
	}
}

func TestPiTaskAssessorCancellationClosesOnceAndRejectsLateSuccess(t *testing.T) {
	for _, phase := range []string{"events", "wait", "close"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started, closed := make(chan struct{}), make(chan struct{})
			var closes, waits atomic.Int32
			model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				events := make(chan agent.AssistantEvent)
				if phase != "events" {
					close(events)
				}
				if phase == "events" {
					close(started)
				}
				return assessmentStream{events: events, close: func() error {
					closes.Add(1)
					if phase == "close" {
						cancel()
					}
					close(closed)
					return nil
				}, wait: func() (agent.Message, error) {
					waits.Add(1)
					if phase == "wait" {
						close(started)
					}
					if phase != "close" {
						<-closed
					}
					return assessmentMessage(`{"action":"new","task_id":"","workload":"light"}`), nil
				}}, nil
			})
			assess, _ := PiTaskAssessor(agent.AgentDefinition{Model: model}, 64<<10)
			done := make(chan error, 1)
			go func() {
				got, err := assess(ctx, assessmentInput())
				if got != (TaskAssessment{}) {
					done <- errors.New("late success escaped")
					return
				}
				done <- err
			}()
			if phase != "close" {
				select {
				case <-started:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("stream never started")
				}
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || closes.Load() != 1 || waits.Load() != 1 {
					t.Fatal(err, closes.Load(), waits.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled inference did not settle")
			}
		})
	}
}

func TestPiTaskAssessorRejectsAssistantErrorAlongsideValidDecision(t *testing.T) {
	calls, waits, closes := 0, 0, 0
	assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		calls++
		stream := assessmentAnswer(agent.Message{})
		stream.wait = func() (agent.Message, error) {
			waits++
			message := assessmentMessage(`{"action":"attach","task_id":"first","workload":"task"}`)
			message.ErrorMessage = "provider stopped with partial output"
			return message, nil
		}
		stream.close = func() error { closes++; return nil }
		return stream, nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	got, err := assess(t.Context(), assessmentInput())
	if err == nil || !strings.Contains(err.Error(), "provider stopped with partial output") || errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) || calls != 1 || waits != 1 || closes != 1 {
		t.Fatal(got, err, calls, waits, closes)
	}
}

func TestPiTaskAssessorEmptyTerminalFailureIsNotInvalidProtocol(t *testing.T) {
	for _, tc := range []struct {
		reason agent.StopReason
		text   string
	}{
		{agent.StopReasonError, "assistant generation failed"},
		{agent.StopReasonAborted, "assistant generation aborted"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			calls, waits, closes := 0, 0, 0
			assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				stream := assessmentAnswer(agent.Message{})
				stream.wait = func() (agent.Message, error) {
					waits++
					return agent.Message{Role: agent.RoleAssistant, StopReason: tc.reason}, nil
				}
				stream.close = func() error { closes++; return nil }
				return stream, nil
			})}, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			got, err := assess(t.Context(), assessmentInput())
			if err == nil || errors.Is(err, ErrInvalidTaskAssessment) || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), tc.text) || got != (TaskAssessment{}) || calls != 1 || waits != 1 || closes != 1 {
				t.Fatal(got, err, calls, waits, closes)
			}
		})
	}
}

func TestPiTaskAssessorEventErrorsCannotBeRevivedBySuccessfulWait(t *testing.T) {
	cause := errors.New("original event failure")
	for _, tc := range []struct {
		name  string
		event agent.AssistantEvent
	}{
		{"terminal cause", agent.AssistantEvent{Type: agent.AssistantEventError, Err: cause}},
		{"terminal message", agent.AssistantEvent{Type: agent.AssistantEventError, Message: agent.Message{ErrorMessage: "provider event failure"}}},
		{"terminal without cause", agent.AssistantEvent{Type: agent.AssistantEventError}},
		{"nonterminal cause", agent.AssistantEvent{Type: agent.AssistantEventTextDelta, Err: cause}},
		{"done with cause", agent.AssistantEvent{Type: agent.AssistantEventDone, Err: cause}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, waits, closes := 0, 0, 0
			model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				events := make(chan agent.AssistantEvent, 2)
				events <- tc.event
				message := assessmentMessage(`{"action":"attach","task_id":"first","workload":"task"}`)
				events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
				closed := make(chan struct{})
				return assessmentStream{events: events,
					close: func() error { closes++; close(closed); close(events); return nil },
					wait:  func() (agent.Message, error) { waits++; <-closed; return message, nil },
				}, nil
			})
			assess, err := PiTaskAssessor(agent.AgentDefinition{Model: model}, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			got, err := assess(t.Context(), assessmentInput())
			if err == nil || errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) || calls != 1 || waits != 1 || closes != 1 {
				t.Fatal(got, err, calls, waits, closes)
			}
			if tc.event.Err != nil && !errors.Is(err, cause) {
				t.Fatal("event cause lost", err)
			}
			if tc.event.Message.ErrorMessage != "" && !strings.Contains(err.Error(), tc.event.Message.ErrorMessage) {
				t.Fatal("event message lost", err)
			}
		})
	}
}

func TestPiTaskAssessorJoinsEventWaitAndCloseFailures(t *testing.T) {
	eventErr, waitErr, closeErr := errors.New("event cause"), errors.New("wait cause"), errors.New("close cause")
	assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		events := make(chan agent.AssistantEvent, 1)
		events <- agent.AssistantEvent{Type: agent.AssistantEventError, Err: eventErr}
		return assessmentStream{events: events, close: func() error { return closeErr }, wait: func() (agent.Message, error) { return agent.Message{}, waitErr }}, nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	got, err := assess(t.Context(), assessmentInput())
	if got != (TaskAssessment{}) || !errors.Is(err, eventErr) || !errors.Is(err, waitErr) || !errors.Is(err, closeErr) {
		t.Fatal(got, err)
	}
}

func TestPiTaskAssessorNormalDoneEventRemainsSuccessful(t *testing.T) {
	message := assessmentMessage(`{"action":"new","task_id":"","workload":"light"}`)
	assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		events := make(chan agent.AssistantEvent, 1)
		events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
		close(events)
		return assessmentStream{events: events, wait: func() (agent.Message, error) { return message, nil }}, nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := assess(t.Context(), assessmentInput()); err != nil || got != (TaskAssessment{Action: "new", Workload: WorkloadLight}) {
		t.Fatal(got, err)
	}
}
