package pigo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAICodexRequestMarshalsEmojiAndUnpairedSurrogateToolResultText(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.2-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}

	toolCallID := "call_unicode|fc_unicode"
	request := buildOpenAICodexRequest(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Use the test tool."},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        toolCallID,
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
				ToolCallID: toolCallID,
				ToolName:   "test_tool",
				Content: []ContentBlock{
					TextContent{
						Text: "Mario Zechner wann? 🙈\nText with unpaired surrogate: " + string(rune(0xd83d)),
					},
				},
			},
			UserMessage{Content: "Summarize the tool result briefly."},
		},
	}, ProviderStreamOptions{})

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("expected codex request to marshal, got %v", err)
	}
	if !strings.Contains(string(payload), "Mario Zechner wann?") {
		t.Fatalf("expected marshaled payload to preserve unicode text, got %s", string(payload))
	}
}

func TestAnthropicRequestMarshalsEmojiAndUnpairedSurrogateToolResultText(t *testing.T) {
	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	request := anthropicRequest{
		Model: model.ID,
		Messages: convertAnthropicMessages([]Message{
			UserMessage{Content: "Use the test tool."},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_unicode",
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
				ToolCallID: "call_unicode",
				ToolName:   "test_tool",
				Content: []ContentBlock{
					TextContent{
						Text: "Mario Zechner wann? 🙈\nText with unpaired surrogate: " + string(rune(0xd83d)),
					},
				},
			},
			UserMessage{Content: "Summarize the tool result briefly."},
		}, *model),
		MaxTokens: 128,
	}

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("expected anthropic-style request to marshal, got %v", err)
	}
	if !strings.Contains(string(payload), "Mario Zechner wann?") {
		t.Fatalf("expected marshaled payload to preserve unicode text, got %s", string(payload))
	}
}

