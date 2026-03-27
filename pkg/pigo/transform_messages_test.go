package pigo

import (
	"strings"
	"testing"
	"time"
)

func anthropicNormalizeToolCallID(id string, _ Model, _ AssistantMessage) string {
	replacer := strings.NewReplacer(
		"|", "_",
		"+", "_",
		"/", "_",
		"=", "_",
		" ", "_",
	)
	normalized := replacer.Replace(id)
	if len(normalized) > 64 {
		return normalized[:64]
	}
	return normalized
}

func makeCopilotClaudeModel() Model {
	return Model{
		ID:            "claude-sonnet-4",
		Name:          "Claude Sonnet 4",
		API:           "anthropic-messages",
		Provider:      "github-copilot",
		BaseURL:       "https://api.individual.githubcopilot.com",
		Reasoning:     true,
		Input:         []InputType{InputText, InputImage},
		ContextWindow: 128000,
		MaxTokens:     16000,
	}
}

func TestTransformMessagesConvertsThinkingBlocksToPlainTextWhenSourceModelDiffers(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		UserMessage{Content: "hello", Timestamp: time.Now().UTC()},
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{
					Thinking:          "Let me think about this...",
					ThinkingSignature: "reasoning_content",
				},
				TextContent{Text: "Hi there!"},
			},
			API:        "openai-completions",
			Provider:   "github-copilot",
			Model:      "gpt-4o",
			Usage:      Usage{},
			StopReason: StopReasonStop,
			Timestamp:  time.Now().UTC(),
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)

	var textBlocks int
	var thinkingBlocks int
	for _, block := range assistant.Content {
		switch block.(type) {
		case TextContent:
			textBlocks++
		case ThinkingContent:
			thinkingBlocks++
		}
	}

	if thinkingBlocks != 0 {
		t.Fatalf("expected no thinking blocks, got %d", thinkingBlocks)
	}
	if textBlocks < 2 {
		t.Fatalf("expected at least 2 text blocks, got %d", textBlocks)
	}
}

func TestTransformMessagesRemovesThoughtSignatureFromToolCallsWhenMigratingBetweenModels(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		UserMessage{Content: "run a command", Timestamp: time.Now().UTC()},
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{
					ID:               "call_123",
					Name:             "bash",
					Arguments:        map[string]any{"command": "ls"},
					ThoughtSignature: `{"type":"reasoning.encrypted","id":"call_123","data":"encrypted"}`,
				},
			},
			API:        "openai-responses",
			Provider:   "github-copilot",
			Model:      "gpt-5",
			Usage:      Usage{},
			StopReason: StopReasonToolUse,
			Timestamp:  time.Now().UTC(),
		},
		ToolResultMessage{
			ToolCallID: "call_123",
			ToolName:   "bash",
			Content:    []ContentBlock{TextContent{Text: "output"}},
			IsError:    false,
			Timestamp:  time.Now().UTC(),
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := findAssistantMessage(t, result)

	for _, block := range assistant.Content {
		call, ok := block.(ToolCall)
		if !ok {
			continue
		}
		if call.ThoughtSignature != "" {
			t.Fatal("expected thought signature to be removed on migration")
		}
		return
	}

	t.Fatal("expected tool call in transformed assistant message")
}

func TestTransformMessagesInsertsSyntheticToolResultForOrphanedToolCall(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		UserMessage{Content: "use a tool", Timestamp: time.Now().UTC()},
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{
					ID:        "call_456",
					Name:      "echo",
					Arguments: map[string]any{"message": "hello"},
				},
			},
			API:        "openai-responses",
			Provider:   "github-copilot",
			Model:      "gpt-5",
			Usage:      Usage{},
			StopReason: StopReasonToolUse,
			Timestamp:  time.Now().UTC(),
		},
		UserMessage{Content: "never mind, just answer directly", Timestamp: time.Now().UTC()},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	if len(result) != 4 {
		t.Fatalf("expected 4 messages after synthetic tool result insertion, got %d", len(result))
	}

	toolResult, ok := result[2].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected message 3 to be a tool result, got %T", result[2])
	}
	if !toolResult.IsError {
		t.Fatal("expected synthetic tool result to be marked as error")
	}
	if toolResult.ToolCallID != "call_456" {
		t.Fatalf("expected synthetic tool result to keep tool call id, got %q", toolResult.ToolCallID)
	}
}

func findAssistantMessage(t *testing.T, messages []Message) AssistantMessage {
	t.Helper()
	for _, message := range messages {
		assistant, ok := message.(AssistantMessage)
		if ok {
			return assistant
		}
	}
	t.Fatal("expected assistant message in result")
	return AssistantMessage{}
}
