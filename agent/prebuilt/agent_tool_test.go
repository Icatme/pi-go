package prebuilt

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/Icatme/pi-go/agent"
)

func TestNewAgentToolValidatesConfigAndUsesStrictTaskSchema(t *testing.T) {
	model := agentToolStaticModel("ok")
	valid := AgentToolConfig{
		Definition:  core.AgentDefinition{Name: "delegate", Model: model},
		Description: "Delegate one task",
	}
	tool, err := NewAgentTool(valid)
	if err != nil {
		t.Fatalf("NewAgentTool returned error: %v", err)
	}
	wantSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
		},
		"required":             []string{"task"},
		"additionalProperties": false,
	}
	if !reflect.DeepEqual(tool.Parameters, wantSchema) {
		t.Fatalf("unexpected agent tool schema: %#v", tool.Parameters)
	}
	tool.Parameters["properties"].(map[string]any)["task"].(map[string]any)["type"] = "number"
	again, err := NewAgentTool(valid)
	if err != nil {
		t.Fatalf("second NewAgentTool returned error: %v", err)
	}
	if got := again.Parameters["properties"].(map[string]any)["task"].(map[string]any)["type"]; got != "string" {
		t.Fatalf("schema mutation leaked across tools: %v", got)
	}

	tests := []struct {
		name   string
		config AgentToolConfig
	}{
		{name: "missing name", config: AgentToolConfig{Definition: core.AgentDefinition{Model: model}, Description: "desc"}},
		{name: "blank name", config: AgentToolConfig{Definition: core.AgentDefinition{Name: " \t", Model: model}, Description: "desc"}},
		{name: "missing description", config: AgentToolConfig{Definition: core.AgentDefinition{Name: "child", Model: model}}},
		{name: "negative definition turns", config: AgentToolConfig{Definition: core.AgentDefinition{Name: "child", Model: model, MaxTurns: -1}, Description: "desc"}},
		{name: "negative depth", config: AgentToolConfig{Definition: core.AgentDefinition{Name: "child", Model: model}, Description: "desc", Limits: AgentToolLimits{MaxDepth: -1}}},
		{name: "negative turns", config: AgentToolConfig{Definition: core.AgentDefinition{Name: "child", Model: model}, Description: "desc", Limits: AgentToolLimits{MaxTurns: -1}}},
		{name: "negative timeout", config: AgentToolConfig{Definition: core.AgentDefinition{Name: "child", Model: model}, Description: "desc", Limits: AgentToolLimits{Timeout: -time.Nanosecond}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAgentTool(tt.config); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}

	for _, args := range []any{
		map[string]any{"task": " \n\t "},
		map[string]any{"task": "ok", "history": "not allowed"},
		map[string]any{"task": 42},
		map[string]any{},
		"task",
	} {
		if _, err := again.Execute(context.Background(), "call", args, nil); err == nil {
			t.Fatalf("expected arguments %#v to fail", args)
		}
	}
}

func TestAgentToolUsesTaskOnlyAndForwardsChildLineageAsTransientUpdates(t *testing.T) {
	var childRequests []core.ModelRequest
	childModel := core.StreamFunc(func(_ context.Context, request core.ModelRequest) (core.AssistantStream, error) {
		childRequests = append(childRequests, request)
		partial := core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "partial"}}}
		final := core.Message{
			Role:       core.RoleAssistant,
			Parts:      []core.Part{{Type: core.PartTypeText, Text: "child answer"}},
			StopReason: core.StopReasonStop,
		}
		return newAgentToolTestStream(final, nil,
			core.AssistantEvent{Type: core.AssistantEventStart, Message: core.Message{Role: core.RoleAssistant}},
			core.AssistantEvent{Type: core.AssistantEventTextDelta, Message: partial, Delta: "partial"},
		), nil
	})
	childTool, err := NewAgentTool(AgentToolConfig{
		Definition:  core.AgentDefinition{Name: "delegate", Model: childModel},
		Description: "Delegate one task",
		Limits:      AgentToolLimits{MaxDepth: 2},
	})
	if err != nil {
		t.Fatalf("NewAgentTool returned error: %v", err)
	}

	parentCalls := 0
	parent, err := core.NewRunner(core.AgentDefinition{
		Name:  "parent",
		Tools: []core.ToolDefinition{childTool},
		Model: core.StreamFunc(func(_ context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
			parentCalls++
			if parentCalls == 1 {
				return agentToolMessageStream(core.Message{
					Role:       core.RoleAssistant,
					ToolCalls:  []core.ToolCall{{ID: "delegate-call", Name: "delegate", Arguments: []byte(`{"task":"  solve only this  "}`)}},
					StopReason: core.StopReasonToolUse,
				}), nil
			}
			return agentToolMessageStream(core.Message{
				Role:       core.RoleAssistant,
				Parts:      []core.Part{{Type: core.PartTypeText, Text: "parent answer"}},
				StopReason: core.StopReasonStop,
			}), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}

	stream := parent.Query(context.Background(), "parent history must not leak")
	var (
		parentEvents []core.AgentEvent
		partials     []AgentToolEventDetails
	)
	for event := range stream.Events() {
		parentEvents = append(parentEvents, event)
		if event.Type != core.EventToolExecutionUpdate || event.PartialToolResult == nil {
			continue
		}
		details, ok := event.PartialToolResult.Details.(AgentToolEventDetails)
		if !ok {
			t.Fatalf("unexpected partial details type %T", event.PartialToolResult.Details)
		}
		partials = append(partials, details)
	}
	snapshot, err := stream.Wait()
	if err != nil {
		t.Fatalf("parent Wait returned error: %v", err)
	}
	if len(childRequests) != 1 || len(childRequests[0].Messages) != 1 {
		t.Fatalf("child did not receive exactly one task message: %+v", childRequests)
	}
	childMessage := childRequests[0].Messages[0]
	if childMessage.Role != core.RoleUser || len(childMessage.Parts) != 1 || childMessage.Parts[0].Text != "  solve only this  " {
		t.Fatalf("child task changed or inherited history: %+v", childMessage)
	}
	if len(parentEvents) == 0 || len(partials) == 0 {
		t.Fatalf("expected parent and child events, parent=%d partials=%d", len(parentEvents), len(partials))
	}
	parentRunID := parentEvents[0].RunID
	childRunID := partials[0].Event.RunID
	for i, details := range partials {
		if details.Depth != 1 || details.Error != "" || details.Event.Err != nil {
			t.Fatalf("unexpected partial details %d: %+v", i, details)
		}
		if details.Event.RunID != childRunID || details.Event.ParentRunID != parentRunID || details.Event.Sequence != uint64(i+1) {
			t.Fatalf("unexpected child lineage/sequence %d: %+v", i, details.Event)
		}
	}

	var finalDetails AgentToolResultDetails
	var foundToolResult bool
	for _, message := range snapshot.Messages {
		if message.Role != core.RoleTool || message.ToolResult == nil || message.ToolResult.ToolName != "delegate" {
			continue
		}
		foundToolResult = true
		if _, ok := message.ToolResult.Details.(AgentToolEventDetails); ok {
			t.Fatalf("transient event details were persisted: %+v", message.ToolResult.Details)
		}
		var ok bool
		finalDetails, ok = message.ToolResult.Details.(AgentToolResultDetails)
		if !ok {
			t.Fatalf("unexpected final details type %T", message.ToolResult.Details)
		}
		if len(message.ToolResult.Content) != 1 || message.ToolResult.Content[0].Text != "child answer" {
			t.Fatalf("unexpected final child content: %+v", message.ToolResult.Content)
		}
	}
	if !foundToolResult {
		t.Fatal("parent transcript has no child tool result")
	}
	if finalDetails.RunID != childRunID || finalDetails.ParentRunID != parentRunID || finalDetails.AgentName != "delegate" || finalDetails.Depth != 1 || finalDetails.Turns != 1 || finalDetails.StopReason != core.StopReasonStop {
		t.Fatalf("unexpected final details: %+v", finalDetails)
	}
}

func TestAgentToolDepthLimitTightensAndCannotWidenInheritedLimit(t *testing.T) {
	tests := []struct {
		name       string
		outerMax   int
		innerMax   int
		wantReject bool
	}{
		{name: "inner tightens absolute depth", outerMax: 3, innerMax: 1, wantReject: true},
		{name: "inner cannot widen parent", outerMax: 1, innerMax: 3, wantReject: true},
		{name: "root default is one", outerMax: 0, innerMax: 0, wantReject: true},
		{name: "zero inherits parent", outerMax: 2, innerMax: 0},
		{name: "larger child stays capped by parent", outerMax: 2, innerMax: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			innerCalls := 0
			inner, err := NewAgentTool(AgentToolConfig{
				Definition: core.AgentDefinition{
					Name: "inner",
					Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
						innerCalls++
						return agentToolMessageStream(core.Message{
							Role:       core.RoleAssistant,
							Parts:      []core.Part{{Type: core.PartTypeText, Text: "inner answer"}},
							StopReason: core.StopReasonStop,
						}), nil
					}),
				},
				Description: "Inner agent",
				Limits:      AgentToolLimits{MaxDepth: tt.innerMax},
			})
			if err != nil {
				t.Fatalf("NewAgentTool inner returned error: %v", err)
			}

			outerCalls := 0
			outerModel := core.StreamFunc(func(_ context.Context, request core.ModelRequest) (core.AssistantStream, error) {
				outerCalls++
				if outerCalls == 1 {
					return agentToolMessageStream(core.Message{
						Role:       core.RoleAssistant,
						ToolCalls:  []core.ToolCall{{ID: "inner-call", Name: "inner", Arguments: []byte(`{"task":"nested"}`)}},
						StopReason: core.StopReasonToolUse,
					}), nil
				}
				last := request.Messages[len(request.Messages)-1]
				if last.Role == core.RoleTool && last.ToolResult != nil && last.ToolResult.IsError {
					return agentToolMessageStream(core.Message{
						Role:         core.RoleAssistant,
						StopReason:   core.StopReasonError,
						ErrorMessage: "nested agent was rejected",
					}), nil
				}
				return agentToolMessageStream(core.Message{
					Role:       core.RoleAssistant,
					Parts:      []core.Part{{Type: core.PartTypeText, Text: "outer answer"}},
					StopReason: core.StopReasonStop,
				}), nil
			})
			outer, err := NewAgentTool(AgentToolConfig{
				Definition:  core.AgentDefinition{Name: "outer", Model: outerModel, Tools: []core.ToolDefinition{inner}},
				Description: "Outer agent",
				Limits:      AgentToolLimits{MaxDepth: tt.outerMax},
			})
			if err != nil {
				t.Fatalf("NewAgentTool outer returned error: %v", err)
			}

			result, runErr := outer.Execute(context.Background(), "outer-call", map[string]any{"task": "root"}, nil)
			if tt.wantReject {
				if runErr == nil || !strings.Contains(runErr.Error(), "nested agent was rejected") {
					t.Fatalf("expected nested depth rejection, result=%+v err=%v", result, runErr)
				}
				if innerCalls != 0 {
					t.Fatalf("depth-rejected inner model ran %d times", innerCalls)
				}
				return
			}
			if runErr != nil {
				t.Fatalf("unexpected nested run error: %v", runErr)
			}
			if innerCalls != 1 || len(result.Content) != 1 || result.Content[0].Text != "outer answer" {
				t.Fatalf("unexpected allowed nested result calls=%d result=%+v", innerCalls, result)
			}
		})
	}
}

func TestAgentToolMaxTurnsIsPerChildUpperBound(t *testing.T) {
	tests := []struct {
		name            string
		definitionTurns int
		limitTurns      int
	}{
		{name: "limit tightens definition", definitionTurns: 3, limitTurns: 1},
		{name: "definition is not widened", definitionTurns: 1, limitTurns: 3},
		{name: "limit bounds default", definitionTurns: 0, limitTurns: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelCalls := 0
			tool, err := NewAgentTool(AgentToolConfig{
				Definition: core.AgentDefinition{
					Name:     "turn-limited",
					MaxTurns: tt.definitionTurns,
					Tools: []core.ToolDefinition{{
						Name: "again",
						Execute: func(context.Context, string, any, core.ToolUpdateFunc) (core.ToolResult, error) {
							return core.ToolResult{Content: []core.Part{{Type: core.PartTypeText, Text: "continue"}}}, nil
						},
					}},
					Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
						modelCalls++
						return agentToolMessageStream(core.Message{
							Role:       core.RoleAssistant,
							ToolCalls:  []core.ToolCall{{ID: "again", Name: "again"}},
							StopReason: core.StopReasonToolUse,
						}), nil
					}),
				},
				Description: "Turn-limited child",
				Limits:      AgentToolLimits{MaxTurns: tt.limitTurns},
			})
			if err != nil {
				t.Fatalf("NewAgentTool returned error: %v", err)
			}
			_, err = tool.Execute(context.Background(), "call", map[string]any{"task": "loop"}, nil)
			if !errors.Is(err, core.ErrMaxTurnsExceeded) {
				t.Fatalf("expected max turns error, got %v", err)
			}
			if modelCalls != 1 {
				t.Fatalf("expected one child turn, got %d model calls", modelCalls)
			}
		})
	}
}

func TestAgentToolTimeoutUsesConfiguredOrInheritedContext(t *testing.T) {
	tests := []struct {
		name       string
		timeout    time.Duration
		parentTime time.Duration
	}{
		{name: "configured timeout", timeout: 20 * time.Millisecond},
		{name: "zero inherits caller deadline", parentTime: 20 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, err := NewAgentTool(AgentToolConfig{
				Definition: core.AgentDefinition{
					Name: "slow-child",
					Model: core.StreamFunc(func(ctx context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					}),
				},
				Description: "Slow child",
				Limits:      AgentToolLimits{Timeout: tt.timeout},
			})
			if err != nil {
				t.Fatalf("NewAgentTool returned error: %v", err)
			}
			ctx := context.Background()
			if tt.parentTime > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.parentTime)
				defer cancel()
			}
			started := time.Now()
			_, err = tool.Execute(ctx, "call", map[string]any{"task": "wait"}, nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline exceeded, got %v", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("timeout took too long: %s", elapsed)
			}
		})
	}
}

func TestAgentToolPreservesCancellationCauseAndDoesNotStartCanceledChild(t *testing.T) {
	cause := errors.New("caller stopped delegation")
	t.Run("already canceled", func(t *testing.T) {
		modelCalls := 0
		tool, err := NewAgentTool(AgentToolConfig{
			Definition: core.AgentDefinition{
				Name: "canceled-child",
				Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
					modelCalls++
					return agentToolMessageStream(reflectionFinal("unexpected", core.StopReasonStop, "")), nil
				}),
			},
			Description: "Canceled child",
		})
		if err != nil {
			t.Fatalf("NewAgentTool returned error: %v", err)
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		_, err = tool.Execute(ctx, "call", map[string]any{"task": "do not start"}, nil)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("Execute error=%v, want canceled and custom cause identities", err)
		}
		if modelCalls != 0 {
			t.Fatalf("pre-canceled child model ran %d times", modelCalls)
		}
	})

	t.Run("canceled in flight", func(t *testing.T) {
		started := make(chan struct{})
		tool, err := NewAgentTool(AgentToolConfig{
			Definition: core.AgentDefinition{
				Name: "canceling-child",
				Model: core.StreamFunc(func(ctx context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				}),
			},
			Description: "Canceling child",
		})
		if err != nil {
			t.Fatalf("NewAgentTool returned error: %v", err)
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		go func() {
			<-started
			cancel(cause)
		}()
		_, err = tool.Execute(ctx, "call", map[string]any{"task": "cancel after start"}, nil)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("Execute error=%v, want canceled and custom cause identities", err)
		}
	})

	t.Run("canceled child returns cause", func(t *testing.T) {
		started := make(chan struct{})
		tool, err := NewAgentTool(AgentToolConfig{
			Definition: core.AgentDefinition{
				Name: "cause-returning-child",
				Model: core.StreamFunc(func(ctx context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
					close(started)
					<-ctx.Done()
					return nil, context.Cause(ctx)
				}),
			},
			Description: "Cause-returning child",
		})
		if err != nil {
			t.Fatalf("NewAgentTool returned error: %v", err)
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		go func() {
			<-started
			cancel(cause)
		}()
		_, err = tool.Execute(ctx, "call", map[string]any{"task": "return cause"}, nil)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("Execute error=%v, want canceled and custom cause identities", err)
		}
	})
}

func TestAgentToolTerminalTrackingIsScopedToFinalTurn(t *testing.T) {
	terminal := core.ToolDefinition{
		Name: "terminal",
		Execute: func(context.Context, string, any, core.ToolUpdateFunc) (core.ToolResult, error) {
			return core.ToolResult{Content: []core.Part{{Type: core.PartTypeText, Text: "old terminal"}}, Terminate: true}, nil
		},
	}
	ordinary := core.ToolDefinition{
		Name: "ordinary",
		Execute: func(context.Context, string, any, core.ToolUpdateFunc) (core.ToolResult, error) {
			return core.ToolResult{Content: []core.Part{{Type: core.PartTypeText, Text: "must not escape"}}}, nil
		},
	}
	modelCalls := 0
	completedTurns := 0
	tool, err := NewAgentTool(AgentToolConfig{
		Definition: core.AgentDefinition{
			Name:  "reused-id-child",
			Tools: []core.ToolDefinition{terminal, ordinary},
			Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
				modelCalls++
				if modelCalls == 1 {
					return agentToolMessageStream(core.Message{
						Role: core.RoleAssistant,
						ToolCalls: []core.ToolCall{
							{ID: "reused", Name: "terminal"},
							{ID: "other", Name: "ordinary"},
						},
						StopReason: core.StopReasonToolUse,
					}), nil
				}
				return agentToolMessageStream(core.Message{
					Role:       core.RoleAssistant,
					ToolCalls:  []core.ToolCall{{ID: "reused", Name: "ordinary"}},
					StopReason: core.StopReasonToolUse,
				}), nil
			}),
			ShouldStopAfterTurn: func(context.Context, core.ShouldStopAfterTurnContext) (bool, error) {
				completedTurns++
				return completedTurns == 2, nil
			},
		},
		Description: "Reused tool-call ID child",
	})
	if err != nil {
		t.Fatalf("NewAgentTool returned error: %v", err)
	}
	result, err := tool.Execute(context.Background(), "call", map[string]any{"task": "reuse an id"}, nil)
	if err == nil || !strings.Contains(err.Error(), "non-terminal tool result") {
		t.Fatalf("Execute result=%+v error=%v, want final non-terminal tool rejection", result, err)
	}
}

func TestAgentToolKeepsChildTerminateInsideChildRun(t *testing.T) {
	childCalls := 0
	child, err := NewAgentTool(AgentToolConfig{
		Definition: core.AgentDefinition{
			Name: "terminating-child",
			Tools: []core.ToolDefinition{{
				Name: "finish",
				Execute: func(context.Context, string, any, core.ToolUpdateFunc) (core.ToolResult, error) {
					return core.ToolResult{
						Content:   []core.Part{{Type: core.PartTypeText, Text: "terminal child output"}},
						Terminate: true,
					}, nil
				},
			}},
			Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
				childCalls++
				return agentToolMessageStream(core.Message{
					Role:       core.RoleAssistant,
					ToolCalls:  []core.ToolCall{{ID: "finish-call", Name: "finish"}},
					StopReason: core.StopReasonToolUse,
				}), nil
			}),
		},
		Description: "Terminating child",
	})
	if err != nil {
		t.Fatalf("NewAgentTool returned error: %v", err)
	}

	parentCalls := 0
	parent, err := core.NewRunner(core.AgentDefinition{
		Tools: []core.ToolDefinition{child},
		Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
			parentCalls++
			if parentCalls == 1 {
				return agentToolMessageStream(core.Message{
					Role:       core.RoleAssistant,
					ToolCalls:  []core.ToolCall{{ID: "child-call", Name: "terminating-child", Arguments: []byte(`{"task":"finish"}`)}},
					StopReason: core.StopReasonToolUse,
				}), nil
			}
			return agentToolMessageStream(core.Message{
				Role:       core.RoleAssistant,
				Parts:      []core.Part{{Type: core.PartTypeText, Text: "parent continued"}},
				StopReason: core.StopReasonStop,
			}), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	stream := parent.Query(context.Background(), "delegate")
	for range stream.Events() {
	}
	snapshot, err := stream.Wait()
	if err != nil {
		t.Fatalf("parent Wait returned error: %v", err)
	}
	if childCalls != 1 || parentCalls != 2 {
		t.Fatalf("terminate crossed run boundary, child calls=%d parent calls=%d", childCalls, parentCalls)
	}
	if len(snapshot.Messages) != 4 || snapshot.Messages[2].Role != core.RoleTool || snapshot.Messages[2].ToolResult == nil {
		t.Fatalf("unexpected parent snapshot: %+v", snapshot.Messages)
	}
	childResult := snapshot.Messages[2].ToolResult
	if len(childResult.Content) != 1 || childResult.Content[0].Text != "terminal child output" {
		t.Fatalf("terminal tool content was not selected: %+v", childResult.Content)
	}
	details, ok := childResult.Details.(AgentToolResultDetails)
	if !ok || details.StopReason != core.StopReasonToolUse {
		t.Fatalf("unexpected terminal child details: %#v", childResult.Details)
	}
}

func TestAgentToolDrainsChildEventsAndKeepsFirstEventErrorPrimary(t *testing.T) {
	firstErr := errors.New("first child event error")
	waitErr := errors.New("later child wait error")
	model := core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
		return newAgentToolTestStream(core.Message{}, waitErr,
			core.AssistantEvent{Type: core.AssistantEventStart, Message: core.Message{Role: core.RoleAssistant}},
			core.AssistantEvent{
				Type:    core.AssistantEventTextDelta,
				Message: core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "before error"}}},
				Delta:   "before error",
				Err:     firstErr,
			},
			core.AssistantEvent{
				Type:    core.AssistantEventTextDelta,
				Message: core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "after error"}}},
				Delta:   "after error",
			},
		), nil
	})
	tool, err := NewAgentTool(AgentToolConfig{
		Definition:  core.AgentDefinition{Name: "failing-child", Model: model},
		Description: "Failing child",
	})
	if err != nil {
		t.Fatalf("NewAgentTool returned error: %v", err)
	}
	var updates []AgentToolEventDetails
	_, err = tool.Execute(context.Background(), "call", map[string]any{"task": "fail"}, func(result core.ToolResult) {
		details, ok := result.Details.(AgentToolEventDetails)
		if !ok {
			t.Fatalf("unexpected partial details type %T", result.Details)
		}
		updates = append(updates, details)
	})
	if !errors.Is(err, firstErr) {
		t.Fatalf("expected first event error, got %v", err)
	}
	var (
		sawError      bool
		sawAfterError bool
		sawAgentEnd   bool
	)
	for _, details := range updates {
		if details.Event.Err != nil {
			t.Fatalf("forwarded event retained non-serializable error: %+v", details.Event)
		}
		if details.Error == firstErr.Error() {
			sawError = true
		}
		if details.Event.Delta == "after error" {
			sawAfterError = true
		}
		if details.Event.Type == core.EventAgentEnd {
			sawAgentEnd = true
		}
	}
	if !sawError || !sawAfterError || !sawAgentEnd {
		t.Fatalf("child events were not fully drained: error=%v after=%v end=%v updates=%+v", sawError, sawAfterError, sawAgentEnd, updates)
	}
}

func TestFinalAgentToolContentRejectsTerminalFailuresAndMissingOutput(t *testing.T) {
	tests := []struct {
		name              string
		messages          []core.Message
		terminalToolCalls []string
		want              string
		wantErr           bool
	}{
		{
			name: "assistant success",
			messages: []core.Message{{
				Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "ok"}}, StopReason: core.StopReasonStop,
			}},
			want: "ok",
		},
		{
			name: "terminal tool success",
			messages: []core.Message{
				{Role: core.RoleAssistant, StopReason: core.StopReasonToolUse},
				{Role: core.RoleTool, ToolResult: &core.ToolResultPayload{ToolCallID: "finish", Content: []core.Part{{Type: core.PartTypeText, Text: "terminal"}}}},
			},
			terminalToolCalls: []string{"finish"},
			want:              "terminal",
		},
		{
			name: "empty final assistant does not expose intermediate tool output",
			messages: []core.Message{
				{Role: core.RoleAssistant, StopReason: core.StopReasonToolUse},
				{Role: core.RoleTool, ToolResult: &core.ToolResultPayload{Content: []core.Part{{Type: core.PartTypeText, Text: "intermediate"}}}},
				{Role: core.RoleAssistant, StopReason: core.StopReasonStop},
			},
			wantErr: true,
		},
		{
			name: "non-terminal tool result cannot be final output",
			messages: []core.Message{
				{Role: core.RoleAssistant, StopReason: core.StopReasonToolUse},
				{Role: core.RoleTool, ToolResult: &core.ToolResultPayload{Content: []core.Part{{Type: core.PartTypeText, Text: "intermediate"}}}},
			},
			wantErr: true,
		},
		{
			name: "duplicate id requires a terminal occurrence for every result",
			messages: []core.Message{
				{Role: core.RoleAssistant, StopReason: core.StopReasonToolUse},
				{Role: core.RoleTool, ToolResult: &core.ToolResultPayload{ToolCallID: "same", Content: []core.Part{{Type: core.PartTypeText, Text: "first"}}}},
				{Role: core.RoleTool, ToolResult: &core.ToolResultPayload{ToolCallID: "same", Content: []core.Part{{Type: core.PartTypeText, Text: "second"}}}},
			},
			terminalToolCalls: []string{"same"},
			wantErr:           true,
		},
		{name: "assistant error", messages: []core.Message{{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "failed"}}, wantErr: true},
		{name: "assistant error message with stop", messages: []core.Message{{Role: core.RoleAssistant, StopReason: core.StopReasonStop, ErrorMessage: "failed"}}, wantErr: true},
		{name: "assistant aborted", messages: []core.Message{{Role: core.RoleAssistant, StopReason: core.StopReasonAborted}}, wantErr: true},
		{name: "assistant length", messages: []core.Message{{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "truncated"}}, StopReason: core.StopReasonLength}}, wantErr: true},
		{name: "tool error", messages: []core.Message{{Role: core.RoleTool, ToolResult: &core.ToolResultPayload{IsError: true, Content: []core.Part{{Type: core.PartTypeText, Text: "tool failed"}}}}}, wantErr: true},
		{name: "missing output", messages: []core.Message{{Role: core.RoleUser}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terminalToolCalls := make(map[string]int, len(tt.terminalToolCalls))
			for _, id := range tt.terminalToolCalls {
				terminalToolCalls[id]++
			}
			content, _, err := finalAgentToolContent(tt.messages, "child", terminalToolCalls)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got content %+v", content)
				}
				return
			}
			if err != nil || len(content) != 1 || content[0].Text != tt.want {
				t.Fatalf("unexpected content=%+v err=%v", content, err)
			}
		})
	}
}

type agentToolTestStream struct {
	events  chan core.AssistantEvent
	final   core.Message
	waitErr error
}

func newAgentToolTestStream(final core.Message, waitErr error, events ...core.AssistantEvent) *agentToolTestStream {
	stream := &agentToolTestStream{
		events:  make(chan core.AssistantEvent, len(events)),
		final:   final,
		waitErr: waitErr,
	}
	for _, event := range events {
		stream.events <- event
	}
	close(stream.events)
	return stream
}

func (s *agentToolTestStream) Events() <-chan core.AssistantEvent { return s.events }

func (s *agentToolTestStream) Wait() (core.Message, error) { return s.final, s.waitErr }

func (s *agentToolTestStream) Close() error { return nil }

func agentToolMessageStream(message core.Message) core.AssistantStream {
	return newAgentToolTestStream(message, nil)
}

func agentToolStaticModel(text string) core.StreamModel {
	return core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
		return agentToolMessageStream(core.Message{
			Role:       core.RoleAssistant,
			Parts:      []core.Part{{Type: core.PartTypeText, Text: text}},
			StopReason: core.StopReasonStop,
		}), nil
	})
}
