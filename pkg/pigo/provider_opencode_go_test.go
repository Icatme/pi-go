package pigo

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestOpenCodeGoProviderCatalogAndProtocolRouting(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "opencode-go-env-key")

	module := resolveProviderModule("opencode-go")
	if module == nil {
		t.Fatal("expected opencode-go provider module")
	}
	if module.Auth.EnvAPIKeyName != "OPENCODE_API_KEY" || module.Auth.RequiresOAuth {
		t.Fatalf("unexpected OpenCode Go auth metadata: %+v", module.Auth)
	}
	if got := GetEnvAPIKey("opencode-go"); got != "opencode-go-env-key" {
		t.Fatalf("expected OPENCODE_API_KEY, got %q", got)
	}
	if len(module.Models) != 18 {
		t.Fatalf("expected 18 active OpenCode Go models, got %d", len(module.Models))
	}
	if !module.Capabilities.SupportsStreaming || !module.Capabilities.SupportsToolChoice {
		t.Fatalf("expected capabilities shared by every OpenCode Go route, got %+v", module.Capabilities)
	}
	if module.Capabilities.SupportsSession || module.Capabilities.SupportsThinkingBudget || module.Capabilities.SupportsReasoningSummary {
		t.Fatalf("mixed OpenCode Go protocols must not advertise route-specific capabilities globally: %+v", module.Capabilities)
	}

	tests := []struct {
		id      string
		api     API
		baseURL string
		image   bool
	}{
		{id: "glm-5.2", api: "openai-completions", baseURL: openCodeGoBaseURL},
		{id: "gpt-5.6-luna", api: "openai-responses", baseURL: openCodeGoBaseURL, image: true},
		{id: "minimax-m2.7", api: "openai-completions", baseURL: openCodeGoBaseURL},
		{id: "qwen3.6-plus", api: "openai-completions", baseURL: openCodeGoBaseURL, image: true},
		{id: "qwen3.8-max", api: "anthropic-messages", baseURL: openCodeGoAnthropicBaseURL, image: true},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			model := GetModel("opencode-go", test.id)
			if model == nil {
				t.Fatalf("expected model %s", test.id)
			}
			if model.Provider != "opencode-go" || model.API != test.api || model.BaseURL != test.baseURL {
				t.Fatalf("unexpected model routing: %+v", model)
			}
			if modelSupportsImages(*model) != test.image {
				t.Fatalf("unexpected image capability for %s: %+v", test.id, model.Input)
			}
		})
	}

	glm := GetModel("opencode-go", "glm-5.2")
	if glm == nil || glm.ContextWindow != 1_000_000 || glm.MaxTokens != 131_072 || glm.Cost.CacheRead != 0.26 {
		t.Fatalf("unexpected GLM-5.2 metadata: %+v", glm)
	}
	if !SupportsXHigh(*glm) || glm.ThinkingLevelMap[ModelThinkingLevelXHigh] != "max" {
		t.Fatalf("expected GLM-5.2 xhigh to map to max: %+v", glm.ThinkingLevelMap)
	}
	deepseek := GetModel("opencode-go", "deepseek-v4-flash")
	if deepseek == nil {
		t.Fatal("expected DeepSeek V4 Flash model")
	}
	if got := GetSupportedThinkingLevels(*deepseek); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelLow, ModelThinkingLevelHigh, ModelThinkingLevelXHigh}) {
		t.Fatalf("expected DeepSeek V4 Flash off/low/high/max-equivalent levels, got %v", got)
	}
	kimi := GetModel("opencode-go", "kimi-k2.6")
	if kimi == nil {
		t.Fatal("expected Kimi K2.6 model")
	}
	if got := GetSupportedThinkingLevels(*kimi); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh}) {
		t.Fatalf("expected Kimi K2.6 on/off levels, got %v", got)
	}

	qwen := GetModel("opencode-go", "qwen3.6-plus")
	compat, ok := qwen.Compat.(*OpenAICompletionsCompat)
	if !ok || compat.ThinkingFormat != "qwen" || compat.MaxTokensField != "max_tokens" {
		t.Fatalf("unexpected Qwen compatibility metadata: %#v", qwen.Compat)
	}

	luna := GetModel("opencode-go", "gpt-5.6-luna")
	if len(luna.CostTiers) != 1 || luna.CostTiers[0].InputTokensAbove != 272_000 || luna.CostTiers[0].Rates.Input != 0.2 {
		t.Fatalf("unexpected Luna long-context pricing: %+v", luna.CostTiers)
	}
	options := BuildProviderStreamOptions(*luna, SimpleStreamOptions{Reasoning: ThinkingLevelXHigh})
	if options.Reasoning != ThinkingLevelXHigh {
		t.Fatalf("expected Luna xhigh reasoning to survive provider dispatch, got %q", options.Reasoning)
	}
}

func TestOpenCodeGoLongContextCostTierAndCloneIsolation(t *testing.T) {
	model := GetModel("opencode-go", "qwen3.6-plus")
	if model == nil || len(model.CostTiers) != 1 {
		t.Fatalf("expected Qwen3.6 Plus long-context pricing, got %+v", model)
	}

	atThreshold := CalculateCost(*model, Usage{Input: 246_000, Output: 1_000, CacheRead: 10_000})
	if math.Abs(atThreshold.Total-0.1265) > 1e-12 {
		t.Fatalf("expected base cost at threshold, got %+v", atThreshold)
	}
	aboveThreshold := CalculateCost(*model, Usage{Input: 246_000, Output: 1_000, CacheRead: 10_000, CacheWrite: 1})
	if math.Abs(aboveThreshold.Total-0.5000025) > 1e-12 {
		t.Fatalf("expected long-context tier above threshold, got %+v", aboveThreshold)
	}

	model.CostTiers[0].Rates.Input = 999
	fresh := GetModel("opencode-go", "qwen3.6-plus")
	if fresh.CostTiers[0].Rates.Input != 2 {
		t.Fatalf("expected cloned cost tiers, got %+v", fresh.CostTiers)
	}
}

func TestOpenCodeGoChatCompletionsStreamsReasoningToolsAndUsage(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/v1/chat/completions" {
			t.Fatalf("expected /v1/chat/completions, got %s", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer request-override" {
			t.Fatalf("expected request header override, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-go","model":"kimi-k3","choices":[{"index":0,"delta":{"reasoning":"plan"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-go","model":"kimi-k3","choices":[{"index":0,"delta":{"content":"checking "},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-go","model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\""}}]},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-go","model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"Paris\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":4}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := GetModel("opencode-go", "kimi-k3")
	if model == nil {
		t.Fatal("expected Kimi K3 model")
	}
	model.BaseURL = server.URL + "/v1"
	ctx := Context{
		SystemPrompt: "Use tools when needed.",
		Messages: []Message{UserMessage{Content: []ContentBlock{
			TextContent{Text: "weather"},
			ImageContent{Data: "aW1hZ2U=", MIMEType: "image/png"},
		}}},
		Tools: []Tool{{
			Name:        "lookup",
			Description: "look up weather",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
			},
		}},
	}
	options := BuildProviderStreamOptions(*model, SimpleStreamOptions{
		APIKey:         "base-key",
		Headers:        map[string]string{"authorization": "Bearer request-override"},
		MaxTokens:      4096,
		Reasoning:      ThinkingLevelXHigh,
		SessionID:      "session-1",
		CacheRetention: CacheRetentionLong,
	})
	options.ToolChoice = "lookup"
	response := Complete(*model, ctx, options)

	if response.StopReason != StopReasonToolUse || response.ResponseID != "chatcmpl-go" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if len(response.Content) != 3 {
		t.Fatalf("expected thinking, text, and tool call blocks, got %#v", response.Content)
	}
	thinking, ok := response.Content[0].(ThinkingContent)
	if !ok || thinking.Thinking != "plan" || thinking.ThinkingSignature != "reasoning_content" {
		t.Fatalf("unexpected thinking block: %#v", response.Content[0])
	}
	text, ok := response.Content[1].(TextContent)
	if !ok || text.Text != "checking " {
		t.Fatalf("unexpected text block: %#v", response.Content[1])
	}
	toolCall, ok := response.Content[2].(ToolCall)
	if !ok || toolCall.ID != "call_1" || toolCall.Name != "lookup" || toolCall.Arguments["city"] != "Paris" {
		t.Fatalf("unexpected tool call: %#v", response.Content[2])
	}
	if response.Usage.Input != 6 || response.Usage.CacheRead != 4 || response.Usage.Output != 3 || response.Usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
	if math.Abs(response.Usage.Cost.Total-0.0000642) > 1e-12 {
		t.Fatalf("unexpected calculated cost: %+v", response.Usage.Cost)
	}

	if captured["model"] != "kimi-k3" || captured["max_tokens"] != float64(4096) {
		t.Fatalf("unexpected model/max_tokens payload: %#v", captured)
	}
	if _, exists := captured["max_completion_tokens"]; exists {
		t.Fatalf("OpenCode Go must use max_tokens: %#v", captured)
	}
	if captured["reasoning_effort"] != "max" {
		t.Fatalf("expected xhigh to map to max, got %#v", captured["reasoning_effort"])
	}
	if captured["prompt_cache_key"] != "session-1" || captured["prompt_cache_retention"] != "24h" {
		t.Fatalf("expected long-cache session fields, got %#v", captured)
	}
	toolChoice, ok := captured["tool_choice"].(map[string]any)
	if !ok || toolChoice["type"] != "function" {
		t.Fatalf("expected named tool choice object, got %#v", captured["tool_choice"])
	}
	messages, ok := captured["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("expected system and user messages, got %#v", captured["messages"])
	}
	systemMessage := messages[0].(map[string]any)
	if systemMessage["role"] != "system" {
		t.Fatalf("OpenCode Go must not use developer role, got %#v", systemMessage)
	}
	userMessage := messages[1].(map[string]any)
	parts, ok := userMessage["content"].([]any)
	if !ok || len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("expected image input to stay in the request, got %#v", userMessage)
	}
}

func TestOpenCodeGoChatCompletionsThinkingFormats(t *testing.T) {
	kimi := GetModel("opencode-go", "kimi-k2.6")
	if kimi == nil {
		t.Fatal("expected Kimi K2.6 model")
	}
	kimiEnabled := buildOpenAICompletionsRequest(*kimi, Context{}, BuildProviderStreamOptions(*kimi, SimpleStreamOptions{Reasoning: ThinkingLevelLow}))
	if kimiEnabled.Thinking == nil || kimiEnabled.Thinking.Type != "enabled" || kimiEnabled.ReasoningEffort != "" {
		t.Fatalf("Kimi K2.6 must use thinking toggle without reasoning_effort: %+v", kimiEnabled)
	}
	kimiDisabled := buildOpenAICompletionsRequest(*kimi, Context{}, BuildProviderStreamOptions(*kimi, SimpleStreamOptions{}))
	if kimiDisabled.Thinking == nil || kimiDisabled.Thinking.Type != "disabled" {
		t.Fatalf("Kimi K2.6 must explicitly disable thinking: %+v", kimiDisabled)
	}
	kimiLongCache := buildOpenAICompletionsRequest(*kimi, Context{}, BuildProviderStreamOptions(*kimi, SimpleStreamOptions{
		CacheRetention: CacheRetentionLong,
		SessionID:      "session-unsupported",
	}))
	if kimiLongCache.PromptCacheKey != "" || kimiLongCache.PromptCacheRetention != "" {
		t.Fatalf("Kimi K2.6 must omit unsupported long-cache fields: %+v", kimiLongCache)
	}
	deepseek := GetModel("opencode-go", "deepseek-v4-flash")
	if deepseek == nil {
		t.Fatal("expected DeepSeek V4 Flash model")
	}
	deepseekOff := buildOpenAICompletionsRequest(*deepseek, Context{}, BuildProviderStreamOptions(*deepseek, SimpleStreamOptions{}))
	if deepseekOff.Thinking == nil || deepseekOff.Thinking.Type != "disabled" {
		t.Fatalf("DeepSeek V4 must explicitly disable thinking when off: %+v", deepseekOff)
	}

	qwen := GetModel("opencode-go", "qwen3.6-plus")
	if qwen == nil {
		t.Fatal("expected Qwen3.6 Plus model")
	}
	qwenRequest := buildOpenAICompletionsRequest(*qwen, Context{}, BuildProviderStreamOptions(*qwen, SimpleStreamOptions{Reasoning: ThinkingLevelHigh}))
	if qwenRequest.EnableThinking == nil || !*qwenRequest.EnableThinking || qwenRequest.ReasoningEffort != "high" {
		t.Fatalf("Qwen3.6 Plus must use enable_thinking: %+v", qwenRequest)
	}
}

func TestOpenCodeGoDeepSeekV4FlashSendsLowReasoningEffort(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-deepseek","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := GetModel("opencode-go", "deepseek-v4-flash")
	if model == nil {
		t.Fatal("expected DeepSeek V4 Flash model")
	}
	model.BaseURL = server.URL + "/v1"
	response := Complete(*model, Context{Messages: []Message{UserMessage{Content: "solve"}}}, BuildProviderStreamOptions(*model, SimpleStreamOptions{
		APIKey:    "test-key",
		Reasoning: ThinkingLevelLow,
	}))
	if response.StopReason != StopReasonStop {
		t.Fatalf("unexpected response: %+v", response)
	}
	if captured["reasoning_effort"] != "low" {
		t.Fatalf("expected low reasoning_effort on the wire, got %#v", captured["reasoning_effort"])
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("expected DeepSeek thinking to be enabled, got %#v", captured["thinking"])
	}
}

func TestOpenCodeGoChatCompletionsReplaysReasoningContent(t *testing.T) {
	model := GetModel("opencode-go", "deepseek-v4-pro")
	if model == nil {
		t.Fatal("expected DeepSeek V4 Pro model")
	}
	request := buildOpenAICompletionsRequest(*model, Context{Messages: []Message{
		AssistantMessage{
			API:      model.API,
			Provider: model.Provider,
			Model:    model.ID,
			Content: []ContentBlock{
				ThinkingContent{Thinking: "prior thought", ThinkingSignature: "reasoning"},
				ToolCall{ID: "call_1", Name: "lookup", Arguments: map[string]any{"q": "x"}},
			},
		},
		ToolResultMessage{ToolCallID: "call_1", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "ok"}}},
	}}, BuildProviderStreamOptions(*model, SimpleStreamOptions{}))
	if len(request.Messages) != 2 {
		t.Fatalf("expected assistant and tool messages, got %#v", request.Messages)
	}
	if request.Messages[0].ReasoningContent == nil || *request.Messages[0].ReasoningContent != "prior thought" || request.Messages[0].Reasoning != "" {
		t.Fatalf("expected OpenCode replay to normalize reasoning to reasoning_content: %#v", request.Messages[0])
	}
}

func TestOpenCodeGoChatCompletionsKeepsParallelToolResultsBeforeImages(t *testing.T) {
	model := GetModel("opencode-go", "kimi-k2.6")
	if model == nil {
		t.Fatal("expected Kimi K2.6 model")
	}
	request := buildOpenAICompletionsRequest(*model, Context{Messages: []Message{
		AssistantMessage{
			API:      model.API,
			Provider: model.Provider,
			Model:    model.ID,
			Content: []ContentBlock{
				ToolCall{ID: "call_image", Name: "screenshot", Arguments: map[string]any{}},
				ToolCall{ID: "call_text", Name: "inspect", Arguments: map[string]any{}},
			},
		},
		ToolResultMessage{
			ToolCallID: "call_image",
			ToolName:   "screenshot",
			Content:    []ContentBlock{ImageContent{Data: "aW1hZ2U=", MIMEType: "image/png"}},
		},
		ToolResultMessage{
			ToolCallID: "call_text",
			ToolName:   "inspect",
			Content:    []ContentBlock{TextContent{Text: "done"}},
		},
	}}, BuildProviderStreamOptions(*model, SimpleStreamOptions{}))

	if len(request.Messages) != 4 {
		t.Fatalf("expected assistant, two tool results, and attached image, got %#v", request.Messages)
	}
	roles := []string{request.Messages[0].Role, request.Messages[1].Role, request.Messages[2].Role, request.Messages[3].Role}
	if !slices.Equal(roles, []string{"assistant", "tool", "tool", "user"}) {
		t.Fatalf("expected consecutive tool results before image user message, got %v", roles)
	}
	if request.Messages[1].ToolCallID != "call_image" || request.Messages[2].ToolCallID != "call_text" {
		t.Fatalf("unexpected tool result order: %#v", request.Messages)
	}
	parts, ok := request.Messages[3].Content.([]any)
	if !ok || len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("expected grouped tool image attachment, got %#v", request.Messages[3])
	}
}

func TestOpenAICompletionsUsagePreservesCacheWriteTokens(t *testing.T) {
	model := GetModel("opencode-go", "qwen3.6-plus")
	if model == nil {
		t.Fatal("expected Qwen3.6 Plus model")
	}
	var usage openAICompletionsUsage
	if err := json.Unmarshal([]byte(`{
		"prompt_tokens": 100,
		"completion_tokens": 10,
		"total_tokens": 110,
		"prompt_tokens_details": {"cached_tokens": 20, "cache_write_tokens": 30}
	}`), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}

	response := AssistantMessage{}
	applyOpenAICompletionsUsage(&response, *model, usage)
	if response.Usage.Input != 50 || response.Usage.Output != 10 || response.Usage.CacheRead != 20 || response.Usage.CacheWrite != 30 || response.Usage.TotalTokens != 110 {
		t.Fatalf("unexpected cache-write usage: %+v", response.Usage)
	}
	if math.Abs(response.Usage.Cost.Total-0.00007475) > 1e-12 {
		t.Fatalf("unexpected cache-write cost: %+v", response.Usage.Cost)
	}
}

func TestOpenCodeGoDeepSeekReplayIncludesEmptyReasoningContent(t *testing.T) {
	model := GetModel("opencode-go", "deepseek-v4-flash")
	if model == nil {
		t.Fatal("expected DeepSeek V4 Flash model")
	}
	request := buildOpenAICompletionsRequest(*model, Context{Messages: []Message{
		AssistantMessage{
			API:      model.API,
			Provider: model.Provider,
			Model:    model.ID,
			Content:  []ContentBlock{ToolCall{ID: "call_1", Name: "lookup", Arguments: map[string]any{"q": "x"}}},
		},
	}}, BuildProviderStreamOptions(*model, SimpleStreamOptions{}))
	if request.Messages[0].ReasoningContent == nil || *request.Messages[0].ReasoningContent != "" {
		t.Fatalf("expected explicit empty reasoning_content, got %#v", request.Messages[0])
	}
	payload, err := json.Marshal(request.Messages[0])
	if err != nil {
		t.Fatalf("marshal assistant replay: %v", err)
	}
	if !strings.Contains(string(payload), `"reasoning_content":""`) {
		t.Fatalf("expected empty reasoning_content in JSON, got %s", payload)
	}
}

func TestOpenCodeGoChatCompletionsRejectsMissingFinishReason(t *testing.T) {
	model := GetModel("opencode-go", "glm-5.1")
	if model == nil {
		t.Fatal("expected GLM-5.1 model")
	}
	response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, SimpleStreamOptions{
		APIKey: "test-key",
		HTTPClient: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return sseResponse(`data: {"choices":[{"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n" + "data: [DONE]\n\n"), nil
		}).Client(),
	})
	if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, "without finish_reason") {
		t.Fatalf("expected missing-terminal error, got %+v", response)
	}
}

func TestOpenAICompletionsFinishReasonMapping(t *testing.T) {
	tests := map[string]StopReason{
		"stop":           StopReasonStop,
		"end":            StopReasonStop,
		"length":         StopReasonLength,
		"tool_calls":     StopReasonToolUse,
		"content_filter": StopReasonError,
	}
	for reason, expected := range tests {
		if got := mapOpenAICompletionsFinishReason(reason, nil); got != expected {
			t.Fatalf("finish_reason %q: expected %q, got %q", reason, expected, got)
		}
	}
}

func TestOpenCodeGoRoutesAnthropicAndResponsesModels(t *testing.T) {
	t.Run("anthropic messages", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "go-key" {
				t.Fatalf("unexpected Anthropic route: path=%s headers=%v", r.URL.Path, r.Header)
			}
			w.Header().Set("content-type", "text/event-stream")
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_go", "usage": map[string]any{"input_tokens": 2}}},
				map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
				map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "anthropic ok"}},
				map[string]any{"type": "content_block_stop", "index": 0},
				map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}},
				map[string]any{"type": "message_stop"},
			)))
		}))
		defer server.Close()

		model := GetModel("opencode-go", "qwen3.8-max")
		model.BaseURL = server.URL
		response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, SimpleStreamOptions{APIKey: "go-key"})
		if response.StopReason != StopReasonStop || response.Content[0].(TextContent).Text != "anthropic ok" {
			t.Fatalf("unexpected Anthropic response: %+v", response)
		}
	})

	t.Run("openai responses", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" || r.Header.Get("authorization") != "Bearer go-key" {
				t.Fatalf("unexpected Responses route: path=%s headers=%v", r.URL.Path, r.Header)
			}
			w.Header().Set("content-type", "text/event-stream")
			_, _ = w.Write([]byte(buildOpenAICodexSSE(
				map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg_go", "content": []map[string]any{{"type": "output_text", "text": "responses ok"}}}},
				map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_go", "status": "completed", "usage": map[string]any{"input_tokens": 2, "output_tokens": 2, "total_tokens": 4, "input_tokens_details": map[string]any{"cached_tokens": 0}}}},
			)))
		}))
		defer server.Close()

		model := GetModel("opencode-go", "gpt-5.6-luna")
		model.BaseURL = server.URL
		response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, SimpleStreamOptions{APIKey: "go-key"})
		if response.StopReason != StopReasonStop || response.Content[0].(TextContent).Text != "responses ok" {
			t.Fatalf("unexpected Responses response: %+v", response)
		}
	})
}
