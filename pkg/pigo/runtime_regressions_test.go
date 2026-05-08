package pigo

import (
	"encoding/json"
	"testing"
)

func TestConvertAnthropicMessagesInsertsMissingToolResultBeforeNextUserTurn(t *testing.T) {
	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	messages := convertAnthropicMessages([]Message{
		UserMessage{Content: "calculate"},
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{
					ID:        "call_1",
					Name:      "calculate",
					Arguments: map[string]any{"expression": "25*18"},
				},
			},
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			StopReason: StopReasonToolUse,
		},
		UserMessage{Content: "never mind, what is 2+2?"},
	}, *model)

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages after orphan tool call repair, got %d", len(messages))
	}
	toolResultBlocks, ok := messages[2].Content.([]anthropicToolResultBlock)
	if !ok || len(toolResultBlocks) != 1 {
		t.Fatalf("expected synthetic tool result block, got %#v", messages[2].Content)
	}
	if toolResultBlocks[0].ToolUseID != "call_1" || !toolResultBlocks[0].IsError {
		t.Fatalf("expected synthetic error tool result for call_1, got %+v", toolResultBlocks[0])
	}
}

func TestConvertOpenAICodexMessagesInsertsMissingToolResultBeforeNextUserTurn(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	input := convertOpenAIResponsesMessages(*model, Context{
		Messages: []Message{
			UserMessage{Content: "calculate"},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_1|fc_1",
						Name:      "calculate",
						Arguments: map[string]any{"expression": "25*18"},
					},
				},
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonToolUse,
			},
			UserMessage{Content: "never mind, what is 2+2?"},
		},
	}, false)

	if len(input) != 4 {
		t.Fatalf("expected 4 input items after orphan tool call repair, got %d", len(input))
	}
	if input[2]["type"] != "function_call_output" {
		t.Fatalf("expected synthetic function_call_output, got %#v", input[2])
	}
	if input[2]["call_id"] != "call_1" {
		t.Fatalf("expected synthetic tool result for call_1, got %#v", input[2]["call_id"])
	}
}

func TestConvertAnthropicMessagesSkipsEmptyTurnsAndPreservesFollowup(t *testing.T) {
	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	messages := convertAnthropicMessages([]Message{
		UserMessage{Content: "   \n\t"},
		AssistantMessage{
			Content:    nil,
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			StopReason: StopReasonStop,
		},
		UserMessage{Content: "respond this time"},
	}, *model)

	if len(messages) != 1 {
		t.Fatalf("expected only non-empty followup message, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "respond this time" {
		t.Fatalf("expected preserved followup user message, got %+v", messages[0])
	}
}

func TestConvertOpenAICodexMessagesSkipsEmptyTurnsAndPreservesFollowup(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	input := convertOpenAIResponsesMessages(*model, Context{
		Messages: []Message{
			UserMessage{Content: []ContentBlock{}},
			AssistantMessage{
				Content:    nil,
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonStop,
			},
			UserMessage{Content: "respond this time"},
		},
	}, false)

	if len(input) != 1 {
		t.Fatalf("expected only one input item, got %d", len(input))
	}
	if input[0]["role"] != "user" {
		t.Fatalf("expected preserved followup user item, got %#v", input[0])
	}
}

func TestOpenAICodexToolResultOutputSupportsImages(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	output := buildOpenAIResponsesToolResultOutput([]ContentBlock{
		ImageContent{Data: "abcd", MIMEType: "image/png"},
	}, *model)

	parts, ok := output.([]map[string]any)
	if !ok {
		t.Fatalf("expected structured image output, got %T", output)
	}
	if len(parts) != 2 {
		t.Fatalf("expected placeholder text + image parts, got %#v", parts)
	}
	if parts[0]["type"] != "input_text" || parts[1]["type"] != "input_image" {
		t.Fatalf("expected text + image output parts, got %#v", parts)
	}
}

func TestAnthropicToolResultContentSupportsImages(t *testing.T) {
	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	output := convertToolResultContent([]ContentBlock{
		ImageContent{Data: "abcd", MIMEType: "image/png"},
	}, *model)

	parts, ok := output.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected placeholder text + image content, got %#v", output)
	}
}

func TestOpenAICodexRequestMarshalsWithInvalidToolResultText(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	request := openAIResponsesRequest{
		Model:  model.ID,
		Stream: true,
		Input: convertOpenAIResponsesMessages(*model, Context{
			Messages: []Message{
				AssistantMessage{
					Content: []ContentBlock{
						ToolCall{
							ID:        "call_1|fc_1",
							Name:      "test_tool",
							Arguments: map[string]any{},
						},
					},
					API:        model.API,
					Provider:   model.Provider,
					Model:      model.ID,
					StopReason: StopReasonToolUse,
				},
				ToolResultMessage{
					ToolCallID: "call_1|fc_1",
					ToolName:   "test_tool",
					Content: []ContentBlock{
						TextContent{Text: string([]byte{0xff, 'b', 'a', 'd'})},
					},
				},
			},
		}, false),
	}

	if _, err := json.Marshal(request); err != nil {
		t.Fatalf("expected codex request to marshal with invalid text bytes, got %v", err)
	}
}

func TestAnthropicRequestMarshalsWithInvalidToolResultText(t *testing.T) {
	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	request := anthropicRequest{
		Model: model.ID,
		Messages: convertAnthropicMessages([]Message{
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_1",
						Name:      "test_tool",
						Arguments: map[string]any{},
					},
				},
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call_1",
				ToolName:   "test_tool",
				Content: []ContentBlock{
					TextContent{Text: string([]byte{0xff, 'b', 'a', 'd'})},
				},
			},
		}, *model),
		MaxTokens: 128,
	}

	if _, err := json.Marshal(request); err != nil {
		t.Fatalf("expected anthropic request to marshal with invalid text bytes, got %v", err)
	}
}
