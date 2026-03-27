package pigo

import (
	"os"
	"strings"
	"testing"
)

func TestStreamSimpleKimiCodingLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live kimi-coding stream test")
	}

	apiKey := GetEnvAPIKey("kimi-coding")
	if apiKey == "" {
		t.Skip("missing KIMI_API_KEY for live stream test")
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	stream := StreamSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, SimpleStreamOptions{
		APIKey: apiKey,
	})

	sawTextDelta := false
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextDelta && strings.TrimSpace(event.Delta) != "" {
			sawTextDelta = true
		}
	}

	response := stream.Result()
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live kimi stream response id")
	}
	if !sawTextDelta {
		t.Fatal("expected live kimi stream text delta")
	}
}

func TestStreamSimpleOpenAICodexLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex stream test")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	stream := StreamSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	sawTextDelta := false
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextDelta && strings.TrimSpace(event.Delta) != "" {
			sawTextDelta = true
		}
	}

	response := stream.Result()
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live codex stream response id")
	}
	if !sawTextDelta {
		t.Fatal("expected live codex stream text delta")
	}
}

