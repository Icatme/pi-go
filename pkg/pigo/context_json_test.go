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
				HostedToolExecutions: []HostedToolExecution{{
					ID:               "hosted_1",
					Type:             HostedToolTypeWebSearch,
					Name:             "search",
					ProviderToolName: "$web_search",
					Arguments:        map[string]any{"query": "pigo"},
					Result:           map[string]any{"count": float64(1)},
				}},
				API:           "openai-codex-responses",
				Provider:      "openai-codex",
				Model:         "gpt-5.4",
				ResponseModel: "gpt-5.4-2026-01-01",
				ResponseID:    "resp_123",
				Usage:         Usage{Input: 10, Output: 5, CacheRead: 2, TotalTokens: 17},
				StopReason:    StopReasonToolUse,
				Diagnostics: []AssistantMessageDiagnostic{{
					Type:      "retry",
					Timestamp: now,
					Error:     &DiagnosticErrorInfo{Name: "temporary", Message: "retrying", Code: map[string]any{"status": float64(429)}},
					Details:   map[string]any{"attempt": float64(2)},
				}},
				Timestamp: now.Add(time.Second),
			},
			ToolResultMessage{
				ToolCallID: "call_1|item_1",
				ToolName:   "edit_file",
				Content:    []ContentBlock{TextContent{Text: `{"ok":true}`}},
				Details:    map[string]any{"status": "written"},
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
		HostedTools: []HostedTool{{Type: HostedToolTypeWebSearch, Name: "search"}},
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
	if len(restored.HostedTools) != 1 || restored.HostedTools[0].Type != HostedToolTypeWebSearch || restored.HostedTools[0].Name != "search" {
		t.Fatalf("expected hosted tools to round-trip, got %+v", restored.HostedTools)
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
	if assistant.ResponseModel != "gpt-5.4-2026-01-01" {
		t.Fatalf("expected response model to round-trip, got %q", assistant.ResponseModel)
	}
	if len(assistant.HostedToolExecutions) != 1 || assistant.HostedToolExecutions[0].ProviderToolName != "$web_search" {
		t.Fatalf("expected hosted tool executions to round-trip, got %+v", assistant.HostedToolExecutions)
	}
	if len(assistant.Diagnostics) != 1 || assistant.Diagnostics[0].Error == nil || assistant.Diagnostics[0].Error.Message != "retrying" {
		t.Fatalf("expected diagnostics to round-trip, got %+v", assistant.Diagnostics)
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
	if details, ok := toolResult.Details.(map[string]any); !ok || details["status"] != "written" {
		t.Fatalf("expected tool result details to round-trip, got %#v", toolResult.Details)
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

func TestContextJSONTimestampRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 30, 45, 123456789, time.UTC)
	context := Context{
		Messages: []Message{
			UserMessage{Content: "hello", Timestamp: now},
			AssistantMessage{Content: []ContentBlock{TextContent{Text: "world"}}, StopReason: StopReasonStop, Timestamp: now.Add(time.Second)},
			ToolResultMessage{ToolCallID: "call_1", ToolName: "test", Content: []ContentBlock{TextContent{Text: "result"}}, Timestamp: now.Add(2 * time.Second)},
		},
	}

	payload, err := json.Marshal(context)
	if err != nil {
		t.Fatalf("expected context to marshal, got %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("expected payload to unmarshal to generic map, got %v", err)
	}
	messages, ok := wire["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("expected 3 messages in wire format, got %#v", wire["messages"])
	}

	userMsg := messages[0].(map[string]any)
	timestampStr, ok := userMsg["timestamp"].(string)
	if !ok {
		t.Fatalf("expected timestamp to be serialized as string, got %T", userMsg["timestamp"])
	}
	if timestampStr == "" {
		t.Fatal("expected non-empty timestamp string")
	}

	var restored Context
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("expected context to unmarshal, got %v", err)
	}

	user, ok := restored.Messages[0].(UserMessage)
	if !ok {
		t.Fatalf("expected first message to be user, got %T", restored.Messages[0])
	}
	if !user.Timestamp.Equal(now) {
		t.Fatalf("expected user timestamp to round-trip, got %v, want %v", user.Timestamp, now)
	}

	assistant, ok := restored.Messages[1].(AssistantMessage)
	if !ok {
		t.Fatalf("expected second message to be assistant, got %T", restored.Messages[1])
	}
	if !assistant.Timestamp.Equal(now.Add(time.Second)) {
		t.Fatalf("expected assistant timestamp to round-trip, got %v, want %v", assistant.Timestamp, now.Add(time.Second))
	}

	toolResult, ok := restored.Messages[2].(ToolResultMessage)
	if !ok {
		t.Fatalf("expected third message to be toolResult, got %T", restored.Messages[2])
	}
	if !toolResult.Timestamp.Equal(now.Add(2 * time.Second)) {
		t.Fatalf("expected tool result timestamp to round-trip, got %v, want %v", toolResult.Timestamp, now.Add(2*time.Second))
	}
}

func TestContextJSONTimestampHandlesUnixMillisInput(t *testing.T) {
	// TS side sends timestamp as Unix milliseconds (number).
	// Verify Go can handle numeric timestamp input gracefully.
	payload := []byte(`{"messages":[{"role":"user","content":"hello","timestamp":1746791445123}]}`)

	var restored Context
	err := json.Unmarshal(payload, &restored)
	// Current implementation uses RFC3339Nano string format.
	// Numeric timestamps from TS will fail to parse with the current layout.
	// This test documents the current behavior.
	if err == nil {
		user, ok := restored.Messages[0].(UserMessage)
		if ok && !user.Timestamp.IsZero() {
			t.Fatalf("expected numeric timestamp to be rejected or zero, got %v", user.Timestamp)
		}
	}
}
