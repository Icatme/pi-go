package pigo

import (
	"os"
	"strings"
	"testing"
)

func TestCompleteSimpleKimiCodingLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live kimi-coding test")
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	apiKey := GetEnvAPIKey("kimi-coding")
	if apiKey == "" {
		t.Skip("missing KIMI_API_KEY for live test")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, SimpleStreamOptions{
		APIKey: apiKey,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatal("expected content from live kimi response")
	}
	if response.ResponseID == "" {
		t.Fatal("expected live kimi response id")
	}

	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected first content block to be text, got %T", response.Content[0])
	}

	if !strings.Contains(strings.ToUpper(text.Text), "OK") {
		t.Fatalf("expected live kimi response to contain OK, got %q", text.Text)
	}
}

func TestCompleteSimpleKimiCodingLiveWithThinkingAndCache(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live kimi-coding test")
	}

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	apiKey := GetEnvAPIKey("kimi-coding")
	if apiKey == "" {
		t.Skip("missing KIMI_API_KEY for live test")
	}

	response := Complete(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, ProviderStreamOptions{
		APIKey:               apiKey,
		Reasoning:            ThinkingLevelHigh,
		ThinkingBudgetTokens: 2048,
		CacheRetention:       CacheRetentionShort,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live kimi response id")
	}
}

func TestCompleteSimpleKimiCodingLiveSkipsEmptyAssistantHistory(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live kimi-coding empty-history test")
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	apiKey := GetEnvAPIKey("kimi-coding")
	if apiKey == "" {
		t.Skip("missing KIMI_API_KEY for live test")
	}

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Hello"},
			AssistantMessage{
				Content:    nil,
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonStop,
			},
			UserMessage{Content: "Reply with exactly OK."},
		},
	}, SimpleStreamOptions{
		APIKey: apiKey,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected empty assistant history live call to succeed, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatal("expected content from live kimi empty-history response")
	}
}
