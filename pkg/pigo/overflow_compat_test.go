package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Public protocol examples from Pi v1.0.1's overflow tests; no provider calls.
var compatibleOverflowErrors = []struct {
	name string
	text string
}{
	{"LiteLLM", "Error: 503 litellm.ServiceUnavailableError: litellm.MidStreamFallbackError: litellm.APIConnectionError: APIConnectionError: OpenAIException - Requested token count exceeds the model's maximum context length of 131072 tokens."},
	{"parenthesized limit", "Error: 400 Input length (265330) exceeds model's maximum context length (262144)."},
	{"comma limit", "Requested token count exceeds the maximum context length of 131,072 tokens."},
	{"Poolside", "Provider returned error: Input length 131393 exceeds the maximum allowed input length of 131040 tokens."},
	{"Together", "400 The input (516368 tokens) is longer than the model's context length (262144 tokens)."},
	{"DS4", "400 Prompt has 256468 tokens, but the configured context size is 256000 tokens"},
	{"DS4 comma counts", "Prompt has 5,958,968 tokens, but the configured context size is 256,000 tokens"},
}

func TestIsContextOverflowCompatibleErrorForms(t *testing.T) {
	for _, test := range compatibleOverflowErrors {
		t.Run(test.name, func(t *testing.T) {
			message := createErrorMessage(test.text)
			message.Provider = "fixture-gateway"
			if !IsContextOverflow(message, 0) {
				t.Fatalf("missed explicit context overflow without model window metadata: %q", test.text)
			}
			message.ErrorMessage = strings.ToUpper(test.text)
			if !IsContextOverflow(message, 0) {
				t.Fatal("overflow detection should remain case insensitive")
			}
			for _, prefix := range []string{"Rate limit exceeded: ", "Too many requests: ", "Throttling error: ", "Service unavailable: "} {
				message.ErrorMessage = prefix + test.text
				if IsContextOverflow(message, 0) {
					t.Fatalf("non-overflow exclusion lost precedence: %q", message.ErrorMessage)
				}
			}
			message.ErrorMessage = test.text
			for _, stop := range []StopReason{StopReasonStop, StopReasonLength, StopReasonToolUse, StopReasonAborted} {
				message.StopReason = stop
				if IsContextOverflow(message, 0) {
					t.Fatalf("error text was classified on non-error stop %q", stop)
				}
			}
		})
	}
}

func TestIsContextOverflowCompatibleErrorsRequireContextEvidence(t *testing.T) {
	for _, text := range []string{
		"maximum context length of 131072 tokens",
		"input length (1) is within the model's maximum context length (262144)",
		"request exceeds maximum allowed file length of 131040 bytes",
		"input (516368 bytes) is longer than the model's context length (262144 bytes)",
		"prompt has 256468 characters, but the configured context size is 256000 characters",
		"selected model is at capacity",
		"Range of input length should be [1, 131072]", // Could also mean empty input.
	} {
		if IsContextOverflow(createErrorMessage(text), 0) {
			t.Errorf("unrelated or ambiguous error classified as context overflow: %q", text)
		}
	}
}

func TestCompatibleOverflowHTTPResults(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		for _, test := range compatibleOverflowErrors {
			t.Run(string(api)+"/"+test.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": test.text}})
				}))
				defer server.Close()
				model := Model{ID: "fixture", API: api, Provider: "openai", BaseURL: server.URL, MaxTokens: 256}
				result := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{
					APIKey: "fixture", MaxRetries: 0,
				})
				if result.StopReason != StopReasonError || !IsContextOverflow(result, 0) {
					t.Fatalf("HTTP error did not reach the host as context overflow: %+v", result)
				}
				if requests.Load() != 1 {
					t.Fatalf("classification must not replay the request: got %d requests", requests.Load())
				}
			})
		}
	}
}
