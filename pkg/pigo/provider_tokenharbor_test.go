package pigo

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestTokenHarborBuiltinCatalog(t *testing.T) {
	if !slices.Contains(GetProviders(), Provider("tokenharbor")) {
		t.Fatal("Token Harbor must be available without importing ai-relay")
	}
	for _, test := range []struct {
		id              string
		context, output int
		image           bool
	}{
		{"deepseek-v4-flash", 1000000, 384000, false},
		{"gpt-5.6-luna", 1050000, 128000, true},
		{"glm-5.3-flash", 1000000, 128000, true},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := GetModel("tokenharbor", test.id)
			if model == nil || model.Provider != "tokenharbor" || model.API != "openai-completions" || model.BaseURL != "https://tokenharbor.ai/v1" || model.ContextWindow != test.context || model.MaxTokens != test.output || !model.Reasoning || slices.Contains(model.Input, InputImage) != test.image {
				t.Fatalf("model=%+v", model)
			}
			snapshot, ok := LookupModelCapabilities("tokenharbor", test.id)
			if !ok || snapshot.Capabilities.Tools != CapabilitySupported || snapshot.Capabilities.StrictTools != CapabilityUnsupported || snapshot.Capabilities.Temperature != CapabilityUnsupported || snapshot.Capabilities.TopP != CapabilityUnsupported || snapshot.Capabilities.ParallelToolCalls != CapabilityUnsupported {
				t.Fatalf("snapshot=%+v", snapshot)
			}
		})
	}
	if GetModel("tokenharbor", "unregistered-model") != nil {
		t.Fatal("catalog must not infer capabilities for unknown models")
	}
}

func TestTokenHarborReplaysSystemAndToolUpdates(t *testing.T) {
	for _, id := range []string{"deepseek-v4-flash", "gpt-5.6-luna", "glm-5.3-flash"} {
		t.Run(id, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				var payload struct {
					Messages []struct{ Role, Content string }
					Tools    []struct{ Function struct{ Name string } }
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					return nil, err
				}
				if len(payload.Messages) != 3 || payload.Messages[0].Role != "system" || payload.Messages[0].Content != "base\n\nlater\n\ncurrent policy" || payload.Messages[1].Content != "first" || payload.Messages[2].Content != "last" {
					t.Errorf("system transcript was not collapsed correctly: %+v", payload.Messages)
				}
				if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "current" {
					t.Errorf("tool declaration update was lost: %+v", payload.Tools)
				}
				return sseResponse("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
			})}
			policy := "current policy"
			ctx := Context{SystemPrompt: "base", Tools: []Tool{{Name: "retired"}}, Messages: []Message{
				UserMessage{Content: "first"},
				SystemMessage{Content: "later", Sections: map[string]*string{"policy": &policy}, ToolsRemoved: []ToolReference{{Name: "retired"}}, ToolsAdded: []Tool{{Name: "current", Parameters: map[string]any{"type": "object"}}}},
				UserMessage{Content: "last"},
			}}
			result := Complete(*GetModel("tokenharbor", id), ctx, ProviderStreamOptions{APIKey: "fixture-key", HTTPClient: client, RequestContext: t.Context()})
			if calls != 1 || result.StopReason != StopReasonStop || result.ErrorMessage != "" {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
			if len(ctx.Messages) != 3 || ctx.Tools[0].Name != "retired" || strings.Contains(ctx.SystemPrompt, "later") {
				t.Fatal("request conversion mutated the authoring context")
			}
		})
	}
}

func TestTokenHarborBuiltinAuthorizationAndChatTransport(t *testing.T) {
	t.Setenv("TOKENHARBOR_API_KEY", "test-tokenharbor-key")
	for _, id := range []string{"deepseek-v4-flash", "gpt-5.6-luna", "glm-5.3-flash"} {
		t.Run(id, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.String() != "https://tokenharbor.ai/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer test-tokenharbor-key" {
					t.Errorf("wrong URL or provider credential")
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload["model"] != id || payload["stream"] != true {
					t.Errorf("payload=%+v error=%v", payload, err)
				}
				return sseResponse("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"), nil
			})}
			result := Complete(*GetModel("tokenharbor", id), Context{Messages: []Message{UserMessage{Content: "Reply OK"}}}, ProviderStreamOptions{HTTPClient: client, RequestContext: t.Context(), MaxTokens: 2048})
			if result.StopReason != StopReasonStop || result.ErrorMessage != "" || !result.UsageReported || result.Usage.TotalTokens != 2 || calls != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
}
