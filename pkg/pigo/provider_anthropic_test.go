package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteSimpleAnthropicBuildsAPIKeyRequestWithThinkingDisabled(t *testing.T) {
	var (
		requestBody anthropicRequest
		headers     http.Header
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid anthropic request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_1",
					"usage": map[string]any{
						"input_tokens":                10,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "hello from anthropic",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 3,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected anthropic model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply tersely.",
		Messages: []Message{
			UserMessage{Content: "Say hello"},
		},
	}, SimpleStreamOptions{
		APIKey: "anthropic-test-key",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %+v", response)
	}
	if got := headers.Get("x-api-key"); got != "anthropic-test-key" {
		t.Fatalf("expected x-api-key header, got %q", got)
	}
	if got := headers.Get("authorization"); got != "" {
		t.Fatalf("expected no bearer auth for api key path, got %q", got)
	}
	if beta := headers.Get("anthropic-beta"); !strings.Contains(beta, "fine-grained-tool-streaming-2025-05-14") || !strings.Contains(beta, "interleaved-thinking-2025-05-14") {
		t.Fatalf("expected anthropic beta headers for api key path, got %q", beta)
	}

	thinking, ok := requestBody.Thinking.(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("expected disabled thinking payload, got %#v", requestBody.Thinking)
	}
	if requestBody.OutputConfig != nil {
		t.Fatalf("expected no output_config when thinking disabled, got %+v", requestBody.OutputConfig)
	}
}

func TestCompleteSimpleAnthropicAdaptiveThinkingUsesEffort(t *testing.T) {
	var (
		requestBody anthropicRequest
		headers     http.Header
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid anthropic request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_2",
					"usage": map[string]any{
						"input_tokens":                12,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "adaptive",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 2,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("anthropic", "claude-opus-4-6")
	if model == nil {
		t.Fatal("expected anthropic model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Hello"},
		},
	}, SimpleStreamOptions{
		APIKey:    "anthropic-test-key",
		Reasoning: ThinkingLevelXHigh,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %+v", response)
	}

	thinking, ok := requestBody.Thinking.(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("expected adaptive thinking payload, got %#v", requestBody.Thinking)
	}
	if requestBody.OutputConfig == nil || requestBody.OutputConfig.Effort != "max" {
		t.Fatalf("expected adaptive output_config effort=max, got %+v", requestBody.OutputConfig)
	}
	if beta := headers.Get("anthropic-beta"); strings.Contains(beta, "interleaved-thinking-2025-05-14") {
		t.Fatalf("expected adaptive model to omit interleaved beta, got %q", beta)
	}
}

func TestCompleteAnthropicBuildsMetadataAndNamedToolChoice(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid anthropic request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_3",
					"usage": map[string]any{
						"input_tokens":                10,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "ok",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 2,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected anthropic model")
	}
	model.BaseURL = server.URL

	response := Complete(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Use the calculator"},
		},
		Tools: []Tool{
			{
				Name:        "calculator",
				Description: "Add numbers",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		},
	}, ProviderStreamOptions{
		APIKey:     "anthropic-test-key",
		ToolChoice: "calculator",
		Metadata: map[string]any{
			"user_id": "user-123",
		},
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %+v", response)
	}
	if requestBody.Metadata == nil || requestBody.Metadata.UserID != "user-123" {
		t.Fatalf("expected anthropic metadata user_id, got %+v", requestBody.Metadata)
	}
	toolChoice, ok := requestBody.ToolChoice.(map[string]any)
	if !ok || toolChoice["type"] != "tool" || toolChoice["name"] != "calculator" {
		t.Fatalf("expected named tool_choice payload, got %#v", requestBody.ToolChoice)
	}
}

func TestCompleteSimpleAnthropicOAuthUsesBearerHeadersAndNormalizesToolNames(t *testing.T) {
	var (
		requestBody anthropicRequest
		headers     http.Header
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid anthropic request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_oauth",
					"usage": map[string]any{
						"input_tokens":                8,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    "call_1",
					"name":  "Read",
					"input": map[string]any{"path": "/tmp/test.txt"},
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "tool_use",
				},
				"usage": map[string]any{
					"output_tokens": 1,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected anthropic model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Use the read tool.",
		Messages: []Message{
			UserMessage{Content: "Read /tmp/test.txt"},
		},
		Tools: []Tool{
			{
				Name:        "read",
				Description: "Read a file",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		},
	}, SimpleStreamOptions{
		Auth: map[Provider]AuthConfig{
			"anthropic": {
				Type: AuthTypeOAuth,
				OAuth: &OAuthCredentials{
					AccessToken: "sk-ant-oat-test-token",
				},
			},
		},
	})

	if response.StopReason != StopReasonToolUse {
		t.Fatalf("expected toolUse response, got %+v", response)
	}
	if got := headers.Get("authorization"); got != "Bearer sk-ant-oat-test-token" {
		t.Fatalf("expected bearer oauth header, got %q", got)
	}
	if got := headers.Get("x-api-key"); got != "" {
		t.Fatalf("expected no x-api-key for oauth path, got %q", got)
	}
	if got := headers.Get("user-agent"); got != "claude-cli/"+anthropicClaudeCodeVersion {
		t.Fatalf("expected claude cli user-agent, got %q", got)
	}
	if got := headers.Get("x-app"); got != "cli" {
		t.Fatalf("expected x-app header, got %q", got)
	}
	if beta := headers.Get("anthropic-beta"); !strings.Contains(beta, "claude-code-20250219") || !strings.Contains(beta, "oauth-2025-04-20") {
		t.Fatalf("expected oauth anthropic-beta headers, got %q", beta)
	}
	if len(requestBody.System) < 2 || requestBody.System[0].Text != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Fatalf("expected claude code identity system prompt, got %+v", requestBody.System)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Name != "Read" {
		t.Fatalf("expected outbound tool name to use Claude Code casing, got %+v", requestBody.Tools)
	}
	if len(response.Content) != 1 {
		t.Fatalf("expected single tool call content, got %+v", response.Content)
	}
	call, ok := response.Content[0].(ToolCall)
	if !ok || call.Name != "read" {
		t.Fatalf("expected inbound tool name to map back to original casing, got %#v", response.Content[0])
	}
}
