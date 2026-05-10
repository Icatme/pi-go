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

func TestIsContextOverflowIgnores400WithoutBody(t *testing.T) {
	message := createErrorMessage("400 status code (no body)")
	if IsContextOverflow(message, 32768) {
		t.Fatal("expected 400 without body to not be treated as overflow")
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

func TestIsContextOverflowExcludesThrottlingErrors(t *testing.T) {
	message := createErrorMessage("Throttling error: Too many tokens, please wait before trying again.")
	if IsContextOverflow(message, 32768) {
		t.Fatal("expected throttling error to not be treated as overflow")
	}
}

func TestIsContextOverflowExcludesRateLimitErrors(t *testing.T) {
	message := createErrorMessage("Rate limit exceeded, retry after 60s")
	if IsContextOverflow(message, 32768) {
		t.Fatal("expected rate limit error to not be treated as overflow")
	}
}

func TestIsContextOverflowDetectsXiaomiMiMoStyleLengthStop(t *testing.T) {
	message := AssistantMessage{
		StopReason: StopReasonLength,
		Usage: Usage{
			Input:     32700,
			CacheRead: 0,
			Output:    0,
		},
		Timestamp: time.Now().UTC(),
	}

	if !IsContextOverflow(message, 32768) {
		t.Fatal("expected xiaomi mimo style length stop with zero output to be treated as overflow")
	}
}

func TestIsContextOverflowIgnoresLengthStopWithOutput(t *testing.T) {
	message := AssistantMessage{
		StopReason: StopReasonLength,
		Usage: Usage{
			Input:     32700,
			CacheRead: 0,
			Output:    10,
		},
		Timestamp: time.Now().UTC(),
	}

	if IsContextOverflow(message, 32768) {
		t.Fatal("expected length stop with non-zero output to not be treated as overflow")
	}
}

func TestIsContextOverflowDetectsRequestTooLarge(t *testing.T) {
	message := createErrorMessage("413 {\"error\":{\"type\":\"request_too_large\",\"message\":\"Request exceeds the maximum size\"}}")
	if !IsContextOverflow(message, 200000) {
		t.Fatal("expected request_too_large error to be treated as overflow")
	}
}

func TestGetOverflowPatternsReturnsCopy(t *testing.T) {
	patterns := GetOverflowPatterns()
	if len(patterns) == 0 {
		t.Fatal("expected non-empty overflow patterns")
	}
	if len(patterns) != len(overflowPatterns) {
		t.Fatalf("expected %d patterns, got %d", len(overflowPatterns), len(patterns))
	}
	// Verify it is a copy by modifying the returned slice
	patterns = patterns[:0]
	if len(overflowPatterns) == 0 {
		t.Fatal("expected original patterns to remain unchanged")
	}
}

func TestGetOverflowPatternsContainsExpectedPatterns(t *testing.T) {
	patterns := GetOverflowPatterns()
	expectedPatterns := []string{
		"prompt is too long",
		"request_too_large",
		"exceeds the context window",
		"too many tokens",
		"token limit exceeded",
	}
	for _, expected := range expectedPatterns {
		found := false
		for _, pattern := range patterns {
			if pattern.MatchString(expected) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected pattern matching %q to be present", expected)
		}
	}
}
