package prebuilt

import (
	"context"
	"errors"
	"sync"
	"testing"

	core "github.com/Icatme/pi-go/agent"
)

type scriptedStreamModel struct {
	mu        sync.Mutex
	responses []core.Message
	callCount int
}

func (m *scriptedStreamModel) Stream(_ context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.responses) == 0 {
		return nil, errors.New("no scripted responses")
	}

	index := m.callCount
	if index >= len(m.responses) {
		index = len(m.responses) - 1
	}
	m.callCount++

	final := m.responses[index]
	stream := &staticAssistantStream{
		events: make(chan core.AssistantEvent, 2),
		final:  final,
	}
	stream.events <- core.AssistantEvent{
		Type:    core.AssistantEventStart,
		Message: core.Message{Role: core.RoleAssistant},
	}
	stream.events <- core.AssistantEvent{
		Type:    core.AssistantEventDone,
		Message: final,
	}
	close(stream.events)
	return stream, nil
}

type staticAssistantStream struct {
	events chan core.AssistantEvent
	final  core.Message
	err    error
}

func (s *staticAssistantStream) Events() <-chan core.AssistantEvent {
	return s.events
}

func (s *staticAssistantStream) Wait() (core.Message, error) {
	return s.final, s.err
}

func (s *staticAssistantStream) Close() error {
	return nil
}

func TestPiAgentMessageDuplication(t *testing.T) {
	model := &scriptedStreamModel{
		responses: []core.Message{
			{
				Role: core.RoleAssistant,
				ToolCalls: []core.ToolCall{
					{
						ID:        "call_123",
						Name:      "calculator",
						Arguments: []byte(`{"expression":"25+17"}`),
					},
				},
				StopReason: core.StopReasonToolUse,
			},
			{
				Role:       core.RoleAssistant,
				Parts:      []core.Part{{Type: core.PartTypeText, Text: "The answer is 42"}},
				StopReason: core.StopReasonStop,
			},
		},
	}

	definition := core.AgentDefinition{
		Model:        model,
		SystemPrompt: "You are a helpful assistant.",
		MaxTurns:     20,
		Tools: []core.ToolDefinition{
			{
				Name:        "calculator",
				Description: "Performs basic arithmetic calculations",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expression": map[string]any{
							"type":        "string",
							"description": "The mathematical expression to evaluate",
						},
					},
					"required": []string{"expression"},
				},
				Execute: func(_ context.Context, _ core.ToolExecutionContext) (core.ToolResult, error) {
					return core.ToolResult{
						Content: []core.Part{{Type: core.PartTypeText, Text: "Result: 42"}},
					}, nil
				},
			},
		},
	}

	agent, err := NewPiAgent(definition)
	if err != nil {
		t.Fatalf("NewPiAgent returned error: %v", err)
	}

	if err := agent.Prompt(context.Background(), core.NewUserTextMessage("What is 25 + 17?")); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	state := agent.State()
	if len(state.Messages) != 5 {
		t.Fatalf("expected system head plus 4 messages after one tool round-trip, got %d", len(state.Messages))
	}
	assertPrebuiltSystemHead(t, state.Messages, definition.SystemPrompt, "calculator")
	for index, role := range []core.MessageRole{core.RoleSystem, core.RoleUser, core.RoleAssistant, core.RoleTool, core.RoleAssistant} {
		if state.Messages[index].Role != role {
			t.Fatalf("message %d role=%q, want %q", index, state.Messages[index].Role, role)
		}
	}
}

func TestNewPiAgentExposesNativeAgent(t *testing.T) {
	model := &scriptedStreamModel{
		responses: []core.Message{
			{
				Role:       core.RoleAssistant,
				Parts:      []core.Part{{Type: core.PartTypeText, Text: "done"}},
				StopReason: core.StopReasonStop,
			},
		},
	}

	agent, err := NewPiAgent(core.AgentDefinition{Model: model}, WithPiSystemPrompt("system"), WithPiMaxIterations(3))
	if err != nil {
		t.Fatalf("NewPiAgent returned error: %v", err)
	}

	if err := agent.PromptText(context.Background(), "hello"); err != nil {
		t.Fatalf("PromptText returned error: %v", err)
	}

	state := agent.State()
	if got := len(state.Messages); got != 3 {
		t.Fatalf("expected native agent state with system head plus 2 messages, got %d", got)
	}
	assertPrebuiltSystemHead(t, state.Messages, "system")
	if state.SystemPrompt != "system" {
		t.Fatalf("expected system prompt to be applied, got %q", state.SystemPrompt)
	}
	if state.Messages[1].Role != core.RoleUser || state.Messages[1].Parts[0].Text != "hello" {
		t.Fatalf("expected unchanged user prompt after system head, got %+v", state.Messages[1])
	}
	if state.Messages[2].Role != core.RoleAssistant || state.Messages[2].Parts[0].Text != "done" {
		t.Fatalf("expected assistant text %q, got %+v", "done", state.Messages[2])
	}
}

func assertPrebuiltSystemHead(t *testing.T, messages []core.Message, prompt string, toolNames ...string) {
	t.Helper()
	if len(messages) == 0 || messages[0].Role != core.RoleSystem || messages[0].System == nil {
		t.Fatalf("expected leading system message, got %+v", messages)
	}
	head := messages[0].System
	if head.Content != prompt || len(head.ToolsAdded) != len(toolNames) {
		t.Fatalf("unexpected leading prompt or tool declarations: %+v", head)
	}
	for index, name := range toolNames {
		if head.ToolsAdded[index].Name != name {
			t.Fatalf("leading tool %d=%q, want %q", index, head.ToolsAdded[index].Name, name)
		}
	}
	for index, message := range messages[1:] {
		if message.Role == core.RoleSystem {
			t.Fatalf("static request duplicated system prompt or tool declarations at message %d", index+1)
		}
	}
}
