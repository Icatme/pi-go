package pigo

import (
	"testing"
	"time"
)

func TestTransformPipelineDowngradesUnsupportedImagesWithoutMutatingInput(t *testing.T) {
	model := Model{ID: "text-only", Provider: "anthropic", API: "anthropic-messages", Input: []InputType{InputText}}
	messages := []Message{
		UserMessage{
			Content: []ContentBlock{
				TextContent{Text: "before"},
				ImageContent{Data: "image-1", MIMEType: "image/png"},
			},
		},
	}

	result := downgradeUnsupportedImages(transformContext{model: model}, messages)
	user := result[0].(UserMessage)
	blocks := user.Content.([]ContentBlock)
	if len(blocks) != 2 {
		t.Fatalf("expected placeholder replacement, got %d blocks", len(blocks))
	}
	if text, _ := blocks[1].(TextContent); text.Text != nonVisionUserImagePlaceholder {
		t.Fatalf("expected image placeholder, got %+v", blocks[1])
	}

	original := messages[0].(UserMessage).Content.([]ContentBlock)
	if _, ok := original[1].(ImageContent); !ok {
		t.Fatalf("expected source message to remain unchanged, got %T", original[1])
	}
}

func TestTransformPipelineNormalizesThinkingContentForDifferentModel(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Thinking: "reasoning", ThinkingSignature: "sig"},
				TextContent{Text: "answer"},
			},
			Provider:   "github-copilot",
			API:        "openai-responses",
			Model:      "gpt-5.4",
			StopReason: StopReasonStop,
		},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	assistant := result[0].(AssistantMessage)
	if len(assistant.Content) != 2 {
		t.Fatalf("expected transformed assistant content, got %d blocks", len(assistant.Content))
	}
	first, ok := assistant.Content[0].(TextContent)
	if !ok || first.Text != "reasoning" {
		t.Fatalf("expected thinking to be downgraded to text, got %+v", assistant.Content[0])
	}
}

func TestNormalizeThinkingContentTransformer(t *testing.T) {
	ctx := transformContext{model: makeCopilotClaudeModel()}
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Thinking: "reasoning", ThinkingSignature: "sig"},
				TextContent{Text: "answer"},
			},
			Provider:   "github-copilot",
			API:        "openai-responses",
			Model:      "gpt-5.4",
			StopReason: StopReasonStop,
		},
	}

	result := normalizeThinkingContent(ctx, messages)
	assistant := result[0].(AssistantMessage)
	first, ok := assistant.Content[0].(TextContent)
	if !ok || first.Text != "reasoning" {
		t.Fatalf("expected transformer to normalize thinking content, got %+v", assistant.Content[0])
	}
}

func TestTransformPipelineNormalizesToolCallIDsWithoutMutatingSource(t *testing.T) {
	model := makeCopilotClaudeModel()
	assistant := AssistantMessage{
		Content: []ContentBlock{
			ToolCall{ID: "call|raw/id", Name: "echo", Arguments: map[string]any{"message": "hello"}},
		},
		Provider:   "github-copilot",
		API:        "openai-responses",
		Model:      "gpt-5.4",
		StopReason: StopReasonToolUse,
	}
	messages := []Message{
		assistant,
		ToolResultMessage{ToolCallID: "call|raw/id", ToolName: "echo", Content: []ContentBlock{TextContent{Text: "hello"}}},
	}

	callbackCalls := 0
	result := TransformMessages(messages, model, func(id string, _ Model, _ AssistantMessage) string {
		callbackCalls++
		return "normalized-id"
	})

	transformedAssistant := result[0].(AssistantMessage)
	call := transformedAssistant.Content[0].(ToolCall)
	if call.ID != "normalized-id" {
		t.Fatalf("expected normalized tool call id, got %q", call.ID)
	}
	toolResult := result[1].(ToolResultMessage)
	if toolResult.ToolCallID != "normalized-id" {
		t.Fatalf("expected normalized tool result id, got %q", toolResult.ToolCallID)
	}
	if callbackCalls != 1 {
		t.Fatalf("expected exactly one tool id normalization callback, got %d", callbackCalls)
	}
	sourceCall := assistant.Content[0].(ToolCall)
	if sourceCall.ID != "call|raw/id" {
		t.Fatalf("expected source tool call to remain unchanged, got %q", sourceCall.ID)
	}
}

func TestNormalizeToolCallIDsTransformer(t *testing.T) {
	ctx := transformContext{
		model: makeCopilotClaudeModel(),
		normalizeToolCallID: func(id string, _ Model, _ AssistantMessage) string {
			return id + "-normalized"
		},
	}
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"message": "hello"}},
			},
			Provider:   "github-copilot",
			API:        "openai-responses",
			Model:      "gpt-5.4",
			StopReason: StopReasonToolUse,
		},
		ToolResultMessage{ToolCallID: "call-1", ToolName: "echo", Content: []ContentBlock{TextContent{Text: "done"}}},
	}

	result := normalizeToolCallIDs(ctx, messages)
	assistant := result[0].(AssistantMessage)
	call := assistant.Content[0].(ToolCall)
	if call.ID != "call-1-normalized" {
		t.Fatalf("expected transformer to normalize tool call id, got %q", call.ID)
	}
	toolResult := result[1].(ToolResultMessage)
	if toolResult.ToolCallID != "call-1-normalized" {
		t.Fatalf("expected transformer to normalize tool result id, got %q", toolResult.ToolCallID)
	}
}

func TestTransformPipelineFillsMissingToolResults(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"message": "hello"}},
			},
			Provider:   "github-copilot",
			API:        "openai-responses",
			Model:      "gpt-5.4",
			StopReason: StopReasonToolUse,
			Timestamp:  time.Now().UTC(),
		},
		UserMessage{Content: "follow-up"},
	}

	result := TransformMessages(messages, model, anthropicNormalizeToolCallID)
	if len(result) != 3 {
		t.Fatalf("expected synthetic tool result insertion, got %d messages", len(result))
	}
	toolResult, ok := result[1].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected synthetic tool result in middle slot, got %T", result[1])
	}
	if !toolResult.IsError {
		t.Fatal("expected synthetic tool result to be marked as error")
	}
	if toolResult.ToolCallID != "call-1" {
		t.Fatalf("expected synthetic tool result to target missing call, got %q", toolResult.ToolCallID)
	}
	if tail, ok := result[2].(UserMessage); !ok || tail.Content != "follow-up" {
		t.Fatalf("expected trailing user message to be preserved, got %+v", result[2])
	}
}

func TestFillMissingToolResultsTransformer(t *testing.T) {
	ctx := transformContext{model: makeCopilotClaudeModel()}
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"message": "hello"}},
			},
			Provider:   "github-copilot",
			API:        "openai-responses",
			Model:      "gpt-5.4",
			StopReason: StopReasonToolUse,
			Timestamp:  time.Now().UTC(),
		},
		UserMessage{Content: "follow-up"},
	}

	result := fillMissingToolResults(ctx, messages)
	if len(result) != 3 {
		t.Fatalf("expected fillMissingToolResults to synthesize a missing tool result, got %d messages", len(result))
	}
	if _, ok := result[1].(ToolResultMessage); !ok {
		t.Fatalf("expected synthetic tool result in middle slot, got %T", result[1])
	}
}

func TestTransformMessagesPreservesMessageMetadata(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Thinking: "reasoning", ThinkingSignature: "sig"},
				ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"message": "hello"}},
			},
			HostedToolExecutions: []HostedToolExecution{{
				ID:        "execution-1",
				Name:      "search",
				Arguments: map[string]any{"query": "pigo"},
			}},
			Provider:      "github-copilot",
			API:           "openai-responses",
			Model:         "gpt-5.4",
			ResponseModel: "gpt-5.4-2026-01-01",
			ResponseID:    "response-1",
			StopReason:    StopReasonToolUse,
			Diagnostics: []AssistantMessageDiagnostic{{
				Type:    "warning",
				Details: map[string]any{"retry": true},
			}},
		},
		ToolResultMessage{
			ToolCallID: "call-1",
			ToolName:   "echo",
			Content:    []ContentBlock{TextContent{Text: "done"}},
			Details:    map[string]any{"status": "ok"},
		},
	}

	result := TransformMessages(messages, model, func(string, Model, AssistantMessage) string {
		return "normalized-call"
	})
	assistant := result[0].(AssistantMessage)
	if assistant.ResponseModel != "gpt-5.4-2026-01-01" || assistant.ResponseID != "response-1" {
		t.Fatalf("expected response metadata to survive transforms, got %+v", assistant)
	}
	if len(assistant.HostedToolExecutions) != 1 || assistant.HostedToolExecutions[0].ID != "execution-1" {
		t.Fatalf("expected hosted tool executions to survive transforms, got %+v", assistant.HostedToolExecutions)
	}
	if len(assistant.Diagnostics) != 1 || assistant.Diagnostics[0].Details["retry"] != true {
		t.Fatalf("expected diagnostics to survive transforms, got %+v", assistant.Diagnostics)
	}
	toolResult := result[1].(ToolResultMessage)
	if toolResult.ToolCallID != "normalized-call" || toolResult.Details.(map[string]any)["status"] != "ok" {
		t.Fatalf("expected tool result metadata to survive transforms, got %+v", toolResult)
	}
}

func TestFillMissingToolResultsFlushesTrailingToolCalls(t *testing.T) {
	messages := []Message{
		AssistantMessage{
			Content:    []ContentBlock{ToolCall{ID: "call-1", Name: "echo"}},
			StopReason: StopReasonToolUse,
		},
	}

	result := fillMissingToolResults(transformContext{}, messages)
	if len(result) != 2 {
		t.Fatalf("expected a trailing synthetic tool result, got %d messages", len(result))
	}
	toolResult, ok := result[1].(ToolResultMessage)
	if !ok || toolResult.ToolCallID != "call-1" || !toolResult.IsError {
		t.Fatalf("expected synthetic result for trailing tool call, got %#v", result[1])
	}
}

func TestDefaultTransformersMatchTransformMessages(t *testing.T) {
	model := makeCopilotClaudeModel()
	messages := []Message{
		UserMessage{Content: []ContentBlock{ImageContent{Data: "img", MIMEType: "image/png"}}},
		AssistantMessage{
			Content: []ContentBlock{
				ThinkingContent{Thinking: "reasoning", ThinkingSignature: "sig"},
				ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"message": "hello"}},
			},
			Provider:   "github-copilot",
			API:        "openai-responses",
			Model:      "gpt-5.4",
			StopReason: StopReasonToolUse,
		},
		UserMessage{Content: "follow-up"},
	}
	ctx := transformContext{
		model: model,
		normalizeToolCallID: func(id string, _ Model, _ AssistantMessage) string {
			return id + "-normalized"
		},
	}

	transformed := cloneMessages(messages)
	for _, transformer := range defaultTransformers {
		transformed = transformer(ctx, transformed)
	}
	direct := TransformMessages(messages, model, ctx.normalizeToolCallID)

	if len(transformed) != len(direct) {
		t.Fatalf("expected default transformer pipeline to match TransformMessages, got %d vs %d", len(transformed), len(direct))
	}
	for index := range transformed {
		if messageRole(transformed[index]) != messageRole(direct[index]) {
			t.Fatalf("expected message %d roles to match, got %q vs %q", index, messageRole(transformed[index]), messageRole(direct[index]))
		}
	}
}

func messageRole(message Message) string {
	if message == nil {
		return ""
	}
	return message.messageRole()
}
