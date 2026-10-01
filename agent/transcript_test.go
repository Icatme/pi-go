package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestAgentLateSystemUpdateSurvivesRequestsAndRestore(t *testing.T) {
	var prompts []string
	definition := AgentDefinition{Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		prompts = append(prompts, request.SystemPrompt)
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
	})}
	runtime, err := NewAgent(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.PromptText(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	policy := "policy"
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{Sections: map[string]*string{"policy": &policy}}))
	if err := runtime.PromptText(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.State().SystemPrompt; got != "policy" {
		t.Fatalf("late system section was duplicated: %q", got)
	}
	restored, err := NewAgent(definition, WithSnapshot(runtime.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	updated := "updated policy"
	restored.AppendMessage(NewSystemMessage(SystemMessagePayload{Sections: map[string]*string{"policy": &updated}}))
	if err := restored.PromptText(context.Background(), "third"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prompts, []string{"", "policy", "updated policy"}) {
		t.Fatalf("late instructions were reseeded instead of replayed: %q", prompts)
	}
	if len(restored.Snapshot().Messages) != 8 {
		t.Fatalf("unexpected leading system message: %+v", restored.Snapshot().Messages)
	}
}

func TestAgentResetRetainsSystemSectionsAndTools(t *testing.T) {
	var prompt string
	runtime, err := NewAgent(AgentDefinition{
		SystemPrompt: "base",
		Tools:        []ToolDefinition{{Name: "lookup"}},
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			prompt = request.SystemPrompt
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldPolicy, style := "old policy", "concise"
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{
		Content: "addition", Sections: map[string]*string{"policy": &oldPolicy, "style": &style},
	}))
	if err := runtime.PromptText(context.Background(), "before reset"); err != nil {
		t.Fatal(err)
	}
	runtime.FollowUp(NewUserTextMessage("queued"))
	if err := runtime.Reset(); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Role != RoleSystem || runtime.HasQueuedMessages() {
		t.Fatalf("reset did not retain only the system baseline: %+v", snapshot)
	}
	baseline := snapshot.Messages[0].System
	if baseline.Content != "base\n\naddition" || *baseline.Sections["policy"] != oldPolicy || *baseline.Sections["style"] != style || len(baseline.ToolsAdded) != 1 || baseline.ToolsAdded[0].Name != "lookup" {
		t.Fatalf("reset lost structured system state: %+v", baseline)
	}
	newPolicy := "new policy"
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{Sections: map[string]*string{"policy": &newPolicy}}))
	if err := runtime.PromptText(context.Background(), "after reset"); err != nil {
		t.Fatal(err)
	}
	if prompt != "base\n\naddition\n\nnew policy\n\nconcise" {
		t.Fatalf("reset froze the old section into content: %q", prompt)
	}
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{Sections: map[string]*string{"policy": nil}}))
	if err := runtime.PromptText(context.Background(), "after removal"); err != nil {
		t.Fatal(err)
	}
	if prompt != "base\n\naddition\n\nconcise" {
		t.Fatalf("reset section cannot be removed: %q", prompt)
	}
}

func TestAgentContinueRejectsSystemOnlyHistory(t *testing.T) {
	calls := 0
	runtime, err := NewAgent(AgentDefinition{
		SystemPrompt: "policy",
		Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
			calls++
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Continue(context.Background()); !errors.Is(err, ErrNoMessagesToContinue) {
		t.Fatalf("system-only history should not continue: %v", err)
	}
	if calls != 0 || len(runtime.Snapshot().Messages) != 1 {
		t.Fatalf("empty continuation called the model: calls=%d snapshot=%+v", calls, runtime.Snapshot())
	}
	if err := runtime.PromptText(context.Background(), "first prompt"); err != nil || calls != 1 {
		t.Fatalf("rejected continuation left runtime active: calls=%d err=%v", calls, err)
	}
}

func TestAgentEmptySystemTranscriptDoesNotRestoreConfiguredPrompt(t *testing.T) {
	calls := 0
	runtime, err := NewAgent(AgentDefinition{
		SystemPrompt: "configured policy",
		PrepareRequest: func(_ context.Context, input PrepareRequestContext) (*AgentLoopTurnUpdate, error) {
			if input.Context.SystemPrompt != "" {
				t.Fatalf("empty transcript restored configured prompt before preparation: %q", input.Context.SystemPrompt)
			}
			return &AgentLoopTurnUpdate{Context: &input.Context}, nil
		},
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			calls++
			if request.SystemPrompt != "" {
				t.Fatalf("empty transcript restored configured prompt before request: %q", request.SystemPrompt)
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReplaceMessages([]Message{NewSystemMessage(SystemMessagePayload{})})
	if err := runtime.PromptText(context.Background(), "run"); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if runtime.State().SystemPrompt != "" {
		t.Fatalf("empty prompt did not survive the invocation: %q", runtime.State().SystemPrompt)
	}
}

func TestAgentSystemTranscriptReplayAndClone(t *testing.T) {
	section := "policy"
	changed := "new policy"
	tool := ToolDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "string"}}
	runtime, err := NewAgent(AgentDefinition{SystemPrompt: "base", Tools: []ToolDefinition{tool}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{Content: "addition", Sections: map[string]*string{"policy": &section}}))
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{Sections: map[string]*string{"policy": &changed}}))
	if got := runtime.State().SystemPrompt; got != "base\n\naddition\n\nnew policy" {
		t.Fatal(got)
	}
	snapshot := runtime.Snapshot()
	*snapshot.Messages[1].System.Sections["policy"] = "mutated"
	snapshot.Messages[0].System.ToolsAdded[0].OutputSchema["type"] = "number"
	if got := runtime.Snapshot(); *got.Messages[1].System.Sections["policy"] != "policy" || got.Messages[0].System.ToolsAdded[0].OutputSchema["type"] != "string" {
		t.Fatal(got)
	}
	runtime.AppendMessage(NewSystemMessage(SystemMessagePayload{Sections: map[string]*string{"policy": nil}, ToolsRemoved: []ToolReference{{Name: "lookup"}}}))
	if runtime.State().SystemPrompt != "base\n\naddition" || len(GetCurrentTools(runtime.Snapshot().Messages)) != 0 {
		t.Fatal(runtime.State())
	}
	wire, err := json.Marshal(runtime.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var restored AgentSnapshot
	if err := json.Unmarshal(wire, &restored); err != nil {
		t.Fatal(err)
	}
	if GetCurrentSystemPrompt(restored.Messages) != "base\n\naddition" || len(GetCurrentTools(restored.Messages)) != 0 {
		t.Fatal(restored)
	}
}

func TestAgentPersistsToolLoadoutChangesBeforeRequests(t *testing.T) {
	calls := 0
	definition := AgentDefinition{
		SystemPrompt: "base",
		ToolResolver: func(context.Context, AgentSnapshot) ([]ToolDefinition, error) {
			name := "first"
			if calls > 0 {
				name = "second"
			}
			return []ToolDefinition{{Name: name}}, nil
		},
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			calls++
			tools := GetCurrentTools(request.Messages)
			expected := "first"
			if calls > 1 {
				expected = "second"
			}
			if len(tools) != 1 || tools[0].Name != expected {
				t.Fatal(tools)
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
		FinishTurn: func(context.Context, AgentTurnContext) (AgentTurnDecision, error) {
			if calls == 1 {
				return AgentTurnDecision{Action: TurnActionContinue}, nil
			}
			return AgentTurnDecision{}, nil
		},
	}
	var declarations []SystemMessagePayload
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventMessageEnd && event.Message.Role == RoleSystem {
			declarations = append(declarations, *event.Message.System)
		}
	})
	if err != nil || calls != 2 || len(declarations) != 2 || !reflect.DeepEqual(declarations[1].ToolsRemoved, []ToolReference{{Name: "first"}}) || GetCurrentSystemPrompt(next.Messages) != "base" || GetCurrentTools(next.Messages)[0].Name != "second" {
		t.Fatalf("calls=%d deltas=%+v snapshot=%+v err=%v", calls, declarations, next, err)
	}
	if next.Messages[len(next.Messages)-1].ThinkingLevel != ThinkingOff {
		t.Fatal(next.Messages)
	}
}

func TestToolOutputDeclarationRejectsNonDurableSchema(t *testing.T) {
	_, err := NewAgent(AgentDefinition{Tools: []ToolDefinition{{Name: "work", OutputSchema: map[string]any{"invalid": func() {}}}}})
	if err == nil {
		t.Fatal("expected non-JSON output declaration to fail before runtime")
	}
}
