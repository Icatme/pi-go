package pigo

import (
	"encoding/json"
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
	if len(module.Models) != 20 {
		t.Fatalf("expected 20 documented active OpenCode Go models, got %d", len(module.Models))
	}
	if !module.Capabilities.SupportsStreaming || !module.Capabilities.SupportsToolChoice {
		t.Fatalf("expected capabilities shared by every OpenCode Go route, got %+v", module.Capabilities)
	}
	if module.Capabilities.SupportsSession || module.Capabilities.SupportsThinkingBudget || module.Capabilities.SupportsReasoningSummary {
		t.Fatalf("mixed OpenCode Go protocols must not advertise route-specific capabilities globally: %+v", module.Capabilities)
	}

	expectedAPIs := map[string]API{
		"deepseek-v4-flash":          "openai-completions",
		"deepseek-v4-pro":            "openai-completions",
		"glm-5.1":                    "openai-completions",
		"glm-5.2":                    "openai-completions",
		"glm-5.3":                    "openai-completions",
		"gpt-5.6-luna":               "openai-responses",
		"grok-4.5":                   "openai-responses",
		"hy3":                        "openai-completions",
		"kimi-k2.6":                  "openai-completions",
		"kimi-k2.7-code":             "openai-completions",
		"kimi-k3":                    "openai-completions",
		"mimo-v2.5":                  "openai-completions",
		"mimo-v2.5-pro":              "openai-completions",
		"minimax-m2.7":               "anthropic-messages",
		"minimax-m3":                 "anthropic-messages",
		"muse-spark-1.2-contributor": "openai-responses",
		"qwen3.6-plus":               "anthropic-messages",
		"qwen3.7-max":                "anthropic-messages",
		"qwen3.7-plus":               "anthropic-messages",
		"qwen3.8-max":                "anthropic-messages",
	}
	for id, api := range expectedAPIs {
		t.Run(id, func(t *testing.T) {
			model := GetModel("opencode-go", id)
			if model == nil {
				t.Fatalf("expected model %s", id)
			}
			baseURL := openCodeGoBaseURL
			if api == "anthropic-messages" {
				baseURL = openCodeGoAnthropicBaseURL
			}
			if model.Provider != "opencode-go" || model.API != api || model.BaseURL != baseURL {
				t.Fatalf("unexpected model routing: %+v", model)
			}
			if model.Cost != (UsageCost{}) || len(model.CostTiers) != 0 {
				t.Fatalf("OpenCode Go model %s must not carry price metadata: %+v", id, model)
			}
		})
	}
	for _, excluded := range []string{
		"glm-5",
		"hy3-preview",
		"kimi-k2.5",
		"mimo-v2-omni",
		"mimo-v2-pro",
		"minimax-m2.5",
		"muse-spark-1.2",
		"qwen3.5-plus",
	} {
		if model := GetModel("opencode-go", excluded); model != nil {
			t.Fatalf("deprecated or live-only model %s must not enter the stable catalog: %+v", excluded, model)
		}
	}

	glm := GetModel("opencode-go", "glm-5.2")
	if glm == nil || glm.ContextWindow != 1_000_000 || glm.MaxTokens != 131_072 {
		t.Fatalf("unexpected GLM-5.2 metadata: %+v", glm)
	}
	if got := GetSupportedThinkingLevels(*glm); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelHigh, ModelThinkingLevelMax}) {
		t.Fatalf("expected GLM-5.2 high/max levels, got %v", got)
	}
	glm53 := GetModel("opencode-go", "glm-5.3")
	if glm53 == nil || glm53.ContextWindow != 1_000_000 || glm53.MaxTokens != 131_072 {
		t.Fatalf("unexpected GLM-5.3 metadata: %+v", glm53)
	}
	if got := GetSupportedThinkingLevels(*glm53); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelLow, ModelThinkingLevelHigh, ModelThinkingLevelMax}) {
		t.Fatalf("expected GLM-5.3 low/high/max levels, got %v", got)
	}
	deepseek := GetModel("opencode-go", "deepseek-v4-flash")
	if deepseek == nil {
		t.Fatal("expected DeepSeek V4 Flash model")
	}
	if got := GetSupportedThinkingLevels(*deepseek); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelLow, ModelThinkingLevelHigh, ModelThinkingLevelMax}) {
		t.Fatalf("expected DeepSeek V4 Flash low/high/max levels, got %v", got)
	}
	deepseekPro := GetModel("opencode-go", "deepseek-v4-pro")
	if deepseekPro == nil {
		t.Fatal("expected DeepSeek V4 Pro model")
	}
	if got := GetSupportedThinkingLevels(*deepseekPro); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelHigh, ModelThinkingLevelMax}) {
		t.Fatalf("expected DeepSeek V4 Pro high/max levels, got %v", got)
	}
	kimi := GetModel("opencode-go", "kimi-k2.6")
	if kimi == nil {
		t.Fatal("expected Kimi K2.6 model")
	}
	if got := GetSupportedThinkingLevels(*kimi); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh}) {
		t.Fatalf("expected Kimi K2.6 on/off levels, got %v", got)
	}
	kimi3 := GetModel("opencode-go", "kimi-k3")
	if kimi3 == nil {
		t.Fatal("expected Kimi K3 model")
	}
	if got := GetSupportedThinkingLevels(*kimi3); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelMax}) {
		t.Fatalf("expected Kimi K3 max-only reasoning, got %v", got)
	}
	for _, id := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "glm-5.3", "kimi-k3"} {
		model := GetModel("opencode-go", id)
		options := BuildProviderStreamOptions(*model, SimpleStreamOptions{Reasoning: ThinkingLevelMax})
		request := buildOpenAICompletionsRequest(*model, Context{}, options)
		if options.Reasoning != ThinkingLevelMax || request.ReasoningEffort != "max" {
			t.Fatalf("expected %s max reasoning on the completions wire, options=%q request=%+v", id, options.Reasoning, request)
		}
	}
	muse := GetModel("opencode-go", "muse-spark-1.2-contributor")
	if muse == nil || muse.ContextWindow != 1_048_576 || muse.MaxTokens != 131_072 || !modelSupportsImages(*muse) {
		t.Fatalf("unexpected Muse Spark metadata: %+v", muse)
	}
	if got := GetSupportedThinkingLevels(*muse); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelMinimal, ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh}) {
		t.Fatalf("expected Muse Spark minimal through xhigh reasoning levels, got %v", got)
	}

	luna := GetModel("opencode-go", "gpt-5.6-luna")
	if luna == nil || !SupportsMax(*luna) || luna.ThinkingLevelMap[ModelThinkingLevelMax] != "max" {
		t.Fatalf("expected Luna max reasoning support: %+v", luna)
	}
	options := BuildProviderStreamOptions(*luna, SimpleStreamOptions{Reasoning: ThinkingLevelMax})
	if options.Reasoning != ThinkingLevelMax {
		t.Fatalf("expected Luna max reasoning to survive provider dispatch, got %q", options.Reasoning)
	}
	request := buildOpenAIResponsesRequest(*luna, Context{}, options)
	if request.Reasoning == nil || request.Reasoning.Effort != "max" {
		t.Fatalf("expected Luna max reasoning on the Responses wire, got %+v", request.Reasoning)
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
	model.Cost = UsageCost{Input: 1, Output: 1, CacheRead: 1, CacheWrite: 1}
	model.CostTiers = []ModelCostTier{{InputTokensAbove: 1, Rates: UsageCost{Input: 2, Output: 2}}}
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
		Reasoning:      ThinkingLevelMax,
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
	if response.Usage.Cost != (UsageCost{}) {
		t.Fatalf("OpenCode Go completions must report tokens without calculating price: %+v", response.Usage)
	}

	if captured["model"] != "kimi-k3" || captured["max_tokens"] != float64(4096) {
		t.Fatalf("unexpected model/max_tokens payload: %#v", captured)
	}
	if _, exists := captured["max_completion_tokens"]; exists {
		t.Fatalf("OpenCode Go must use max_tokens: %#v", captured)
	}
	if captured["reasoning_effort"] != "max" {
		t.Fatalf("expected max reasoning effort, got %#v", captured["reasoning_effort"])
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
	if deepseekOff.Thinking != nil || deepseekOff.ReasoningEffort != "" {
		t.Fatalf("DeepSeek V4 must omit unsupported off reasoning controls: %+v", deepseekOff)
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
	model := GetModel("opencode-go", "kimi-k3")
	if model == nil {
		t.Fatal("expected Kimi K3 model")
	}
	model.Cost = UsageCost{Input: 1, Output: 1, CacheRead: 1, CacheWrite: 1}
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
	if response.Usage.Cost != (UsageCost{}) {
		t.Fatalf("OpenCode Go cache usage must not calculate price: %+v", response.Usage)
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
		model.Cost = UsageCost{Input: 1, Output: 1}
		model.BaseURL = server.URL
		response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, SimpleStreamOptions{APIKey: "go-key"})
		if response.StopReason != StopReasonStop || response.Content[0].(TextContent).Text != "anthropic ok" {
			t.Fatalf("unexpected Anthropic response: %+v", response)
		}
		if response.Usage.Input != 2 || response.Usage.Output != 2 || response.Usage.TotalTokens != 4 || response.Usage.Cost != (UsageCost{}) {
			t.Fatalf("OpenCode Go Anthropic route must report tokens without price: %+v", response.Usage)
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
		model.Cost = UsageCost{Input: 1, Output: 1}
		model.BaseURL = server.URL
		response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, SimpleStreamOptions{APIKey: "go-key"})
		if response.StopReason != StopReasonStop || response.Content[0].(TextContent).Text != "responses ok" {
			t.Fatalf("unexpected Responses response: %+v", response)
		}
		if response.Usage.Input != 2 || response.Usage.Output != 2 || response.Usage.TotalTokens != 4 || response.Usage.Cost != (UsageCost{}) {
			t.Fatalf("OpenCode Go Responses route must report tokens without price: %+v", response.Usage)
		}
	})
}
