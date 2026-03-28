package pigo

import (
	"os"
	"strings"
	"testing"
)

func TestCrossProviderHandoffCodexToKimiLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live handoff tests")
	}

	kimiAPIKey := GetEnvAPIKey("kimi-coding")
	if kimiAPIKey == "" {
		t.Skip("missing KIMI_API_KEY for live handoff test")
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Answer concisely.",
		Messages: []Message{
			UserMessage{Content: "Use the tool to double 21."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "I will use the tool.",
						ThinkingSignature: `{"type":"reasoning","summary":[{"type":"summary_text","text":"I will use the tool."}]}`,
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
			UserMessage{Content: "What was the result? Reply with just the number."},
		},
	}, SimpleStreamOptions{
		APIKey: kimiAPIKey,
	})

	if response.StopReason == StopReasonError {
		t.Fatalf("expected no handoff error, got %q", response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatal("expected content from kimi handoff response")
	}
	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", response.Content[0])
	}
	if !strings.Contains(text.Text, "42") {
		t.Fatalf("expected kimi handoff response to contain 42, got %q", text.Text)
	}
}

func TestCrossProviderHandoffKimiToCodexLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live handoff tests")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.2-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Answer concisely.",
		Messages: []Message{
			UserMessage{Content: "Use the tool to double 21."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "I will use the tool.",
						ThinkingSignature: "signed-thinking",
					},
					ToolCall{
						ID:        "toolu_123",
						Name:      "double_number",
						Arguments: map[string]any{"value": 21},
					},
				},
				API:        "anthropic-messages",
				Provider:   "kimi-coding",
				Model:      "kimi-k2-thinking",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "toolu_123",
				ToolName:   "double_number",
				Content:    []ContentBlock{TextContent{Text: "42"}},
			},
			UserMessage{Content: "What was the result? Reply with just the number."},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason == StopReasonError {
		t.Fatalf("expected no handoff error, got %q", response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatal("expected content from codex handoff response")
	}
	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", response.Content[0])
	}
	if !strings.Contains(text.Text, "42") {
		t.Fatalf("expected codex handoff response to contain 42, got %q", text.Text)
	}
}

