package pigo

import (
	"testing"
	"time"
)

func createErrorMessage(errorMessage string) AssistantMessage {
	return AssistantMessage{
		Content:      nil,
		API:          "openai-completions",
		Provider:     "ollama",
		Model:        "qwen3.5:35b",
		Usage:        Usage{},
		StopReason:   StopReasonError,
		ErrorMessage: errorMessage,
		Timestamp:    time.Now().UTC(),
	}
}

func TestIsContextOverflowDetectsExplicitOllamaOverflow(t *testing.T) {
	message := createErrorMessage("400 `prompt too long; exceeded max context length by 100918 tokens`")
	if !IsContextOverflow(message, 32768) {
		t.Fatal("expected explicit ollama overflow to be detected")
	}
}

func TestIsContextOverflowIgnoresGenericOllamaError(t *testing.T) {
	message := createErrorMessage("500 `model runner crashed unexpectedly`")
	if IsContextOverflow(message, 32768) {
		t.Fatal("expected generic ollama error to not be treated as overflow")
	}
}

func TestIsContextOverflowDetectsAnthropicPromptTooLong(t *testing.T) {
	message := createErrorMessage("prompt is too long: 200001 tokens > 200000 maximum")
	if !IsContextOverflow(message, 200000) {
		t.Fatal("expected anthropic prompt-too-long error to be detected")
	}
}

func TestIsContextOverflowDetects413WithoutBody(t *testing.T) {
	message := createErrorMessage("413 status code (no body)")
	if !IsContextOverflow(message, 32768) {
		t.Fatal("expected 413 without body to be treated as overflow")
	}
}

func TestIsContextOverflowDetectsUsageOverflowOnSuccessfulResponse(t *testing.T) {
	message := AssistantMessage{
		StopReason: StopReasonStop,
		Usage: Usage{
			Input:     32000,
			CacheRead: 2000,
		},
		Timestamp: time.Now().UTC(),
	}

	if !IsContextOverflow(message, 32768) {
		t.Fatal("expected successful response with input usage above context window to be treated as overflow")
	}
}
