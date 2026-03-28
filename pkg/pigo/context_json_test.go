package pigo

import (
	"encoding/json"
	"testing"
	"time"
)

func TestContextJSONRoundTripPreservesMessagesAndTools(t *testing.T) {
	now := time.Date(2026, 3, 29, 1, 2, 3, 0, time.UTC)
	context := Context{
		SystemPrompt: "Be precise.",
		Messages: []Message{
			UserMessage{
				Content: []ContentBlock{
					TextContent{Text: "hello"},
					ImageContent{Data: "base64-image", MIMEType: "image/png"},
				},
				Timestamp: now,
			},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{Thinking: "plan", ThinkingSignature: `{"sig":1}`},
					TextContent{Text: "done", TextSignature: `{"text":"sig"}`},
					ToolCall{
						ID:               "call_1|item_1",
						Name:             "edit_file",
						Arguments:        map[string]any{"path": "README.md", "count": float64(2)},
						ThoughtSignature: "thought-sig",
					},
				},
				API:        "openai-codex-responses",
				Provider:   "openai-codex",
				Model:      "gpt-5.4",
				ResponseID: "resp_123",
				Usage:      Usage{Input: 10, Output: 5, CacheRead: 2, TotalTokens: 17},
				StopReason: StopReasonToolUse,
				Timestamp:  now.Add(time.Second),
			},
			ToolResultMessage{
				ToolCallID: "call_1|item_1",
				ToolName:   "edit_file",
				Content:    []ContentBlock{TextContent{Text: `{"ok":true}`}},
				IsError:    true,
				Timestamp:  now.Add(2 * time.Second),
			},
		},
		Tools: []Tool{
			{
				Name:        "edit_file",
				Description: "Edit a file",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
				Validator: ToolArgumentsValidatorFunc(func(args map[string]any) (map[string]any, error) {
					return args, nil
				}),
			},
		},
	}

	payload, err := json.Marshal(context)
	if err != nil {
		t.Fatalf("expected context to marshal, got %v", err)
	}

	var restored Context
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("expected context to unmarshal, got %v", err)
	}

	if restored.SystemPrompt != "Be precise." {
		t.Fatalf("expected system prompt to round-trip, got %q", restored.SystemPrompt)
	}
	if len(restored.Messages) != 3 {
		t.Fatalf("expected 3 restored messages, got %d", len(restored.Messages))
	}
	if len(restored.Tools) != 1 {
		t.Fatalf("expected 1 restored tool, got %d", len(restored.Tools))
	}
	if restored.Tools[0].Validator != nil {
		t.Fatalf("expected validator to be omitted from serialized context, got %+v", restored.Tools[0].Validator)
	}

	user, ok := restored.Messages[0].(UserMessage)
	if !ok {
		t.Fatalf("expected first restored message to be user, got %T", restored.Messages[0])
	}
	userBlocks, ok := user.Content.([]ContentBlock)
	if !ok || len(userBlocks) != 2 {
		t.Fatalf("expected user multimodal content to round-trip, got %#v", user.Content)
	}
	if text, ok := userBlocks[0].(TextContent); !ok || text.Text != "hello" {
		t.Fatalf("expected first user block text, got %#v", userBlocks[0])
	}
	if image, ok := userBlocks[1].(ImageContent); !ok || image.MIMEType != "image/png" || image.Data != "base64-image" {
		t.Fatalf("expected image block to round-trip, got %#v", userBlocks[1])
	}

	assistant, ok := restored.Messages[1].(AssistantMessage)
	if !ok {
		t.Fatalf("expected second restored message to be assistant, got %T", restored.Messages[1])
	}
	if assistant.Provider != "openai-codex" || assistant.ResponseID != "resp_123" || assistant.StopReason != StopReasonToolUse {
		t.Fatalf("expected assistant metadata to round-trip, got %+v", assistant)
	}
	if len(assistant.Content) != 3 {
		t.Fatalf("expected assistant content blocks to round-trip, got %+v", assistant.Content)
	}
	if thinking, ok := assistant.Content[0].(ThinkingContent); !ok || thinking.Thinking != "plan" || thinking.ThinkingSignature != `{"sig":1}` {
		t.Fatalf("expected thinking block to round-trip, got %#v", assistant.Content[0])
	}
	if toolCall, ok := assistant.Content[2].(ToolCall); !ok || toolCall.ID != "call_1|item_1" || toolCall.Name != "edit_file" {
		t.Fatalf("expected tool call block to round-trip, got %#v", assistant.Content[2])
	}

	toolResult, ok := restored.Messages[2].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected third restored message to be toolResult, got %T", restored.Messages[2])
	}
	if toolResult.ToolCallID != "call_1|item_1" || !toolResult.IsError {
		t.Fatalf("expected tool result metadata to round-trip, got %+v", toolResult)
	}
}

func TestSerializeDeserializeContextHelpersRoundTrip(t *testing.T) {
	source := Context{
		SystemPrompt: "Keep going.",
		Messages: []Message{
			UserMessage{Content: "hello"},
			AssistantMessage{
				Content:    []ContentBlock{TextContent{Text: "world"}},
				StopReason: StopReasonStop,
			},
		},
	}

	payload, err := SerializeContext(source)
	if err != nil {
		t.Fatalf("expected SerializeContext to succeed, got %v", err)
	}

	restored, err := DeserializeContext(payload)
	if err != nil {
		t.Fatalf("expected DeserializeContext to succeed, got %v", err)
	}

	if restored.SystemPrompt != source.SystemPrompt {
		t.Fatalf("expected system prompt to round-trip, got %q", restored.SystemPrompt)
	}
	if len(restored.Messages) != 2 {
		t.Fatalf("expected 2 restored messages, got %d", len(restored.Messages))
	}
}

func TestDeserializeContextErrorsOnUnknownRole(t *testing.T) {
	_, err := DeserializeContext([]byte(`{"messages":[{"role":"unknown"}]}`))
	if err == nil {
		t.Fatal("expected unknown message role error")
	}
}
