package pigo

import "testing"

func TestConvertOpenAICodexMessagesSkipsReasoningOnlyAbortedTurn(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	input := convertOpenAIResponsesMessages(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Use the tool."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "",
						ThinkingSignature: `{"type":"reasoning","summary":[{"type":"summary_text","text":"partial"}]}`,
					},
				},
				API:        "openai-codex-responses",
				Provider:   "openai-codex",
				Model:      "gpt-5.4",
				StopReason: StopReasonAborted,
			},
			UserMessage{Content: "Say hello to confirm you can continue."},
		},
	}, false)

	if len(input) != 2 {
		t.Fatalf("expected aborted reasoning-only turn to be dropped, got %d input items", len(input))
	}
	for _, item := range input {
		if item["type"] == "reasoning" {
			t.Fatalf("expected no reasoning item in replay payload, got %#v", item)
		}
	}
}

func TestConvertOpenAICodexMessagesDropsFunctionCallItemIDForDifferentModelReplay(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.2-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}

	input := convertOpenAIResponsesMessages(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Use the tool."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "I should call the tool.",
						ThinkingSignature: `{"type":"reasoning","summary":[{"type":"summary_text","text":"I should call the tool."}]}`,
					},
					ToolCall{
						ID:        "call_test|fc_test",
						Name:      "double_number",
						Arguments: map[string]any{"value": 21},
					},
				},
				API:        "openai-codex-responses",
				Provider:   "openai-codex",
				Model:      "gpt-5.4",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call_test|fc_test",
				ToolName:   "double_number",
				Content:    []ContentBlock{TextContent{Text: "42"}},
			},
			UserMessage{Content: "What was the result?"},
		},
	}, false)

	var (
		sawAssistantText  bool
		sawFunctionCall   bool
		sawReasoningItem  bool
		sawFunctionOutput bool
	)

	for _, item := range input {
		switch item["type"] {
		case "message":
			sawAssistantText = true
		case "function_call":
			sawFunctionCall = true
			if item["call_id"] != "call_test" {
				t.Fatalf("expected call_id call_test, got %#v", item["call_id"])
			}
			if _, exists := item["id"]; exists {
				t.Fatalf("expected item id to be omitted for different-model replay, got %#v", item["id"])
			}
		case "function_call_output":
			sawFunctionOutput = true
			if item["call_id"] != "call_test" {
				t.Fatalf("expected function_call_output call_id call_test, got %#v", item["call_id"])
			}
		case "reasoning":
			sawReasoningItem = true
		}
	}

	if !sawAssistantText {
		t.Fatal("expected replay payload to include assistant text converted from thinking")
	}
	if !sawFunctionCall || !sawFunctionOutput {
		t.Fatal("expected replay payload to include function call and result")
	}
	if sawReasoningItem {
		t.Fatal("expected different-model replay to avoid raw reasoning items")
	}
}
