package pigo

import "testing"

func TestOpenAIResponsesFastTierPricing(t *testing.T) {
	for _, test := range []struct {
		id, tier   string
		multiplier float64
	}{
		{"gpt-5.5", "fast", 2.5}, {"gpt-5.5", "priority", 2.5},
		{"gpt-6-sol", "fast", 2}, {"gpt-6-sol", "priority", 2},
		{"gpt-5.5", "flex", .5}, {"gpt-5.5", "default", 1},
	} {
		t.Run(test.id+"/"+test.tier, func(t *testing.T) {
			model := Model{ID: test.id, Provider: "openai", Cost: UsageCost{Input: 1, Output: 2, CacheRead: .1, CacheWrite: .2}}
			var response AssistantMessage
			applyOpenAIResponsesUsage(model, &response, openAIResponsesUsage{InputTokens: 100, OutputTokens: 10, InputDetails: openAIResponsesInputTokenDetails{CachedTokens: 20, CacheWriteTokens: 10}}, test.tier, "")
			base := calculateProviderUsageCost(model, response.Usage)
			want := base.Total * test.multiplier
			if diff := response.Usage.Cost.Total - want; diff < -1e-12 || diff > 1e-12 {
				t.Fatalf("cost=%v, want %v (%v x base)", response.Usage.Cost, want, test.multiplier)
			}
		})
	}
}

func TestAnthropicEmptyThinkingSignatureModelCapability(t *testing.T) {
	allowEmptySignature := true
	compat := &AnthropicMessagesCompat{AllowEmptySignature: &allowEmptySignature}
	model := Model{ID: "qwen3.8-flash", Provider: "opencode", API: "anthropic-messages", Compat: compat}
	for _, test := range []struct {
		name   string
		source Model
		allow  bool
	}{
		{"same model", model, true},
		{"foreign model", Model{ID: "other", Provider: model.Provider, API: model.API}, false},
		{"foreign provider", Model{ID: model.ID, Provider: "anthropic", API: model.API}, false},
		{"foreign api", Model{ID: model.ID, Provider: model.Provider, API: "openai-completions"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := convertAnthropicMessagesWithCache([]Message{AssistantMessage{Provider: test.source.Provider, API: test.source.API, Model: test.source.ID, Content: []ContentBlock{ThinkingContent{Thinking: "plan"}}}}, model, nil, false, nil, nil)
			content := messages[0].Content.([]any)
			if test.allow {
				block, ok := content[0].(anthropicThinkingBlock)
				if !ok || block.Thinking != "plan" || block.Signature != "" {
					t.Fatalf("expected empty signature thinking, got %+v", content)
				}
			} else if block, ok := content[0].(anthropicTextBlock); !ok || block.Text != "plan" {
				t.Fatalf("foreign thinking must be text, got %+v", content)
			}
		})
	}
	model.Compat = nil
	messages := convertAnthropicMessagesWithCache([]Message{AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, Content: []ContentBlock{ThinkingContent{Thinking: "plan"}}}}, model, nil, false, nil, nil)
	if _, ok := messages[0].Content.([]any)[0].(anthropicTextBlock); !ok {
		t.Fatal("unmarked model must convert unsigned thinking to text")
	}
}

func TestZAICNPromptExceedsMaxLengthIsOverflow(t *testing.T) {
	message := createErrorMessage(`{"error":{"code":"1261","message":"Prompt exceeds max length"}}`)
	message.Provider = "zai"
	if !IsContextOverflow(message, 128000) {
		t.Fatal("expected Z.AI CN prompt overflow")
	}
}

func TestAnthropicAdaptiveModelWireCapabilities(t *testing.T) {
	adaptive := true
	model := Model{
		ID: "adaptive", Provider: "anthropic", API: "anthropic-messages", Reasoning: true, MaxTokens: 128000,
		Compat:           &AnthropicMessagesCompat{ForceAdaptiveThinking: &adaptive},
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelLow: "low", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: "max"},
		Capabilities:     ModelCapabilities{Temperature: CapabilityUnsupported},
	}
	temperature := .5
	for _, test := range []struct {
		level  ThinkingLevel
		effort string
	}{
		{level: ""}, {level: ThinkingLevel("off")},
		{level: ThinkingLevelLow, effort: "low"}, {level: ThinkingLevelXHigh, effort: "xhigh"}, {level: ThinkingLevelMax, effort: "max"},
	} {
		for _, options := range []ProviderStreamOptions{
			BuildProviderStreamOptions(model, SimpleStreamOptions{Reasoning: test.level, Temperature: &temperature}),
			{Reasoning: test.level, Temperature: &temperature},
		} {
			request := buildAnthropicRequest(model, Context{}, resolveAnthropicMessagesProviderOptions(model, options), false, true)
			if request.Temperature != nil {
				t.Fatalf("unsupported temperature serialized: %+v", request)
			}
			if test.effort == "" {
				if request.Thinking != nil || request.OutputConfig != nil {
					t.Fatalf("unsupported off must omit thinking, got %+v", request)
				}
			} else {
				thinking, ok := request.Thinking.(map[string]any)
				if !ok || thinking["type"] != "adaptive" || request.OutputConfig == nil || request.OutputConfig.Effort != test.effort {
					t.Fatalf("mapped adaptive effort %q not serialized: %+v", test.effort, request)
				}
			}
		}
	}
	model.ThinkingLevelMap = nil
	model.Capabilities.Temperature = CapabilitySupported
	request := buildAnthropicRequest(model, Context{}, AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{Temperature: &temperature}}, false, true)
	if request.Temperature == nil || *request.Temperature != temperature {
		t.Fatalf("temperature missing with disabled thinking: %+v", request)
	}
}
