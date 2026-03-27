package pigo

import (
	"strings"
	"testing"
)

func TestTransformMessagesPreservesRedactedThinkingForSameModel(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Redacted: true},
				TextContent{Text: "answer"},
			},
			API:      model.API,
			Provider: model.Provider,
			Model:    model.ID,
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)

	if len(assistant.Content) != 2 {
		t.Fatalf("expected redacted thinking to be preserved, got %d blocks", len(assistant.Content))
	}
	if _, ok := assistant.Content[0].(ThinkingContent); !ok {
		t.Fatalf("expected first block to be redacted thinking, got %T", assistant.Content[0])
	}
}

func TestTransformMessagesDropsRedactedThinkingForDifferentModel(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Redacted: true},
				TextContent{Text: "answer"},
			},
			API:      "openai-responses",
			Provider: model.Provider,
			Model:    "gpt-5.4",
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)

	if len(assistant.Content) != 1 {
		t.Fatalf("expected redacted thinking to be dropped, got %d blocks", len(assistant.Content))
	}
	if _, ok := assistant.Content[0].(TextContent); !ok {
		t.Fatalf("expected remaining block to be text, got %T", assistant.Content[0])
	}
}

func TestTransformMessagesPreservesSignedEmptyThinkingForSameModel(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{
					Thinking:          "",
					ThinkingSignature: "encrypted-reasoning",
				},
			},
			API:      model.API,
			Provider: model.Provider,
			Model:    model.ID,
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)

	if len(assistant.Content) != 1 {
		t.Fatalf("expected signed empty thinking to be preserved, got %d blocks", len(assistant.Content))
	}
	thinking, ok := assistant.Content[0].(ThinkingContent)
	if !ok {
		t.Fatalf("expected thinking block, got %T", assistant.Content[0])
	}
	if thinking.ThinkingSignature != "encrypted-reasoning" {
		t.Fatalf("expected thinking signature to be preserved, got %q", thinking.ThinkingSignature)
	}
}

func TestTransformMessagesNormalizesToolResultIDAlongsideToolCall(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{
					ID:        "call|with/slashes",
					Name:      "edit",
					Arguments: map[string]any{"path": "README.md"},
				},
			},
			API:        "openai-responses",
			Provider:   "github-copilot",
			Model:      "gpt-5.4",
			StopReason: StopReasonToolUse,
		},
		ToolResultMessage{
			ToolCallID: "call|with/slashes",
			ToolName:   "edit",
			Content:    []ContentBlock{TextContent{Text: "ok"}},
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)

	call, ok := assistant.Content[0].(ToolCall)
	if !ok {
		t.Fatalf("expected tool call block, got %T", assistant.Content[0])
	}
	toolResult, ok := result[1].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected tool result message, got %T", result[1])
	}
	if toolResult.ToolCallID != call.ID {
		t.Fatalf("expected normalized tool result id %q, got %q", call.ID, toolResult.ToolCallID)
	}
}

func TestTransformMessagesSkipsAbortedAssistantMessages(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		UserMessage{Content: "first"},
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Thinking: "partial"},
			},
			API:        "openai-responses",
			Provider:   "openai-codex",
			Model:      "gpt-5.4",
			StopReason: StopReasonAborted,
		},
		UserMessage{Content: "second"},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	if len(result) != 2 {
		t.Fatalf("expected aborted assistant message to be skipped, got %d messages", len(result))
	}
	if _, ok := result[0].(UserMessage); !ok {
		t.Fatalf("expected first result to be user message, got %T", result[0])
	}
	if _, ok := result[1].(UserMessage); !ok {
		t.Fatalf("expected second result to be user message, got %T", result[1])
	}
}

func TestTransformMessagesNormalizesLongPipeSeparatedIDsForAnthropic(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{
					ID:        copilotRawToolCallID,
					Name:      "echo",
					Arguments: map[string]any{"message": "hello"},
				},
			},
			API:        "openai-responses",
			Provider:   "github-copilot",
			Model:      "gpt-5.3-codex",
			StopReason: StopReasonToolUse,
		},
		ToolResultMessage{
			ToolCallID: copilotRawToolCallID,
			ToolName:   "echo",
			Content:    []ContentBlock{TextContent{Text: "hello"}},
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)
	call, ok := assistant.Content[0].(ToolCall)
	if !ok {
		t.Fatalf("expected tool call block, got %T", assistant.Content[0])
	}
	if len(call.ID) > 64 {
		t.Fatalf("expected normalized tool call id length <= 64, got %d", len(call.ID))
	}
	if strings.ContainsAny(call.ID, "|+/= ") {
		t.Fatalf("expected anthropic-safe tool call id, got %q", call.ID)
	}

	toolResult, ok := result[1].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected tool result message, got %T", result[1])
	}
	if toolResult.ToolCallID != call.ID {
		t.Fatalf("expected normalized tool result id %q, got %q", call.ID, toolResult.ToolCallID)
	}
}
