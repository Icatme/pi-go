package pigo

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
)

func TestOctoberCatalogTranscriptCapabilities(t *testing.T) {
	for _, test := range []struct {
		provider Provider
		ids      []string
	}{
		{"openai", []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol"}},
		{"openai-codex", []string{"gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol"}},
		{"opencode-go", []string{"gpt-5.6-luna", "gpt-6-luna", "kimi-k3"}},
	} {
		for _, id := range test.ids {
			model := GetModel(test.provider, id)
			if model == nil || !supportsTranscriptSystemMessages(*model) {
				t.Fatalf("%s/%s must preserve system changes in the transcript", test.provider, id)
			}
		}
	}
	for _, test := range []struct {
		provider Provider
		id       string
	}{
		{"openai", "gpt-5.1"},
		{"openai-codex", "gpt-5.1"},
		{"opencode-go", "kimi-k2.7-code"},
		{"opencode-go", "deepseek-v4-pro"},
		{"opencode-go", "grok-4.7"},
	} {
		model := GetModel(test.provider, test.id)
		if model == nil || supportsTranscriptSystemMessages(*model) {
			t.Fatalf("%s/%s has no verified native transcript support", test.provider, test.id)
		}
	}
	if GetModel("openai", "gpt-5.6") != nil {
		t.Fatal("unsupported GPT-5.6 alias must be absent from the native catalog")
	}
	for _, id := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if model := GetModel("openai", id); model.ContextWindow != 272000 || model.MaxTokens != 128000 {
			t.Fatalf("native %s must use the short context cap: %+v", id, model)
		}
		model := GetModel("openai-codex", id)
		if model.ThinkingLevelMap[ModelThinkingLevelMinimal] != "low" {
			t.Fatalf("Codex %s must map minimal thinking to low: %+v", id, model.ThinkingLevelMap)
		}
	}
	if model := GetModel("openai-codex", "gpt-5.6-sol"); model.Cost != (UsageCost{Input: 4, Output: 20, CacheRead: 0.4, CacheWrite: 5}) || model.CostTiers[0].Rates != (UsageCost{Input: 8, Output: 30, CacheRead: 0.8, CacheWrite: 10}) {
		t.Fatalf("Codex GPT-5.6 Sol price differs from upstream standard costs: %+v", model)
	}
}

func TestOctoberOpenAIThinkingAndCostCatalog(t *testing.T) {
	for _, provider := range []Provider{"openai", "openai-codex"} {
		for _, test := range []struct {
			id   string
			cost UsageCost
			long UsageCost
			off  string
		}{
			{"gpt-6.1-sol", UsageCost{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5}, UsageCost{Input: 4, Output: 15, CacheRead: 0.2, CacheWrite: 5}, ""},
			{"gpt-6-sol", UsageCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}, UsageCost{Input: 4, Output: 15, CacheRead: 0.4, CacheWrite: 5}, "none"},
			{"gpt-6-luna", UsageCost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125}, UsageCost{Input: 0.2, Output: 0.75, CacheRead: 0.02, CacheWrite: 0.25}, "none"},
		} {
			t.Run(string(provider)+"/"+test.id, func(t *testing.T) {
				model := GetModel(provider, test.id)
				if model == nil || model.ContextWindow != 272000 || model.MaxTokens != 128000 || !slices.Contains(model.Input, InputImage) {
					t.Fatalf("missing or incorrect model: %+v", model)
				}
				if model.ThinkingLevelMap[ModelThinkingLevelOff] != test.off || model.ThinkingLevelMap[ModelThinkingLevelMax] != "max" {
					t.Fatalf("incorrect reasoning map: %+v", model.ThinkingLevelMap)
				}
				if model.Cost != test.cost || len(model.CostTiers) != 1 || model.CostTiers[0] != (ModelCostTier{InputTokensAbove: 272000, Rates: test.long}) {
					t.Fatalf("incorrect cost metadata: %+v", model)
				}
				atThreshold := CalculateCost(*model, Usage{Input: 272000})
				aboveThreshold := CalculateCost(*model, Usage{Input: 272000, CacheRead: 1})
				if math.Abs(atThreshold.Input-test.cost.Input*0.272) > 1e-12 || math.Abs(aboveThreshold.Input-test.long.Input*0.272) > 1e-12 {
					t.Fatalf("cost tier must include cached input in its threshold: at=%+v above=%+v", atThreshold, aboveThreshold)
				}
				options := BuildProviderStreamOptions(*model, SimpleStreamOptions{Reasoning: ThinkingLevelMax})
				var request openAIResponsesRequest
				if provider == "openai-codex" {
					request = buildOpenAICodexRequest(nil, *model, Context{}, options)
				} else {
					request = mustBuildOpenAIResponsesRequest(t, nil, *model, Context{}, options)
					compat := model.Compat.(*OpenAIResponsesCompat)
					if compat.SupportsExplicitPromptCacheMode == nil || !*compat.SupportsExplicitPromptCacheMode {
						t.Fatal("missing explicit prompt cache capability")
					}
				}
				if request.Reasoning == nil || request.Reasoning.Effort != "max" {
					t.Fatalf("max thinking did not reach wire: %+v", request.Reasoning)
				}
			})
		}
	}
}

func TestClaude55AdaptiveCatalogAndWire(t *testing.T) {
	for _, test := range []struct {
		id           string
		cost         UsageCost
		defaultLevel ModelThinkingLevel
	}{
		{"claude-opus-5-5", UsageCost{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5}, ModelThinkingLevelMedium},
		{"claude-sonnet-5-5", UsageCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}, ModelThinkingLevelHigh},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := GetModel("anthropic", test.id)
			if model == nil || model.ContextWindow != 1000000 || model.MaxTokens != 128000 || model.Cost != test.cost || model.Capabilities.DefaultReasoningLevel != test.defaultLevel {
				t.Fatalf("incorrect Claude 5.5 model: %+v", model)
			}
			wantLevels := []ModelThinkingLevel{ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax}
			if got := GetSupportedThinkingLevels(*model); !slices.Equal(got, wantLevels) {
				t.Fatalf("levels = %v, want %v", got, wantLevels)
			}
			temperature := 0.5
			options := buildAnthropicMessagesProviderOptions(*model, SimpleStreamOptions{Reasoning: ThinkingLevelXHigh, Temperature: &temperature})
			request := buildAnthropicRequest(*model, Context{}, options, false, true)
			if request.Thinking.(map[string]any)["type"] != "adaptive" || request.OutputConfig == nil || request.OutputConfig.Effort != "xhigh" || request.Temperature != nil {
				t.Fatalf("Claude 5.5 must use adaptive effort without temperature: %+v", request)
			}
		})
	}
}

func TestMistralBuiltInCatalogReasoningEfforts(t *testing.T) {
	for _, test := range []struct {
		id     string
		levels []ModelThinkingLevel
	}{
		{"mistral-small-latest", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh}},
		{"mistral-medium-latest", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh}},
		{"mistral-medium-3.5", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh}},
		{"zai-glm-5-2", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh, ModelThinkingLevelMax}},
		{"zai-glm-5-3", []ModelThinkingLevel{ModelThinkingLevelLow, ModelThinkingLevelHigh, ModelThinkingLevelMax}},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := GetModel("mistral", test.id)
			if model == nil || !slices.Equal(GetSupportedThinkingLevels(*model), test.levels) {
				t.Fatalf("incorrect Mistral thinking metadata: %+v", model)
			}
			for _, requested := range test.levels {
				options := BuildProviderStreamOptions(*model, SimpleStreamOptions{Reasoning: ThinkingLevel(requested)})
				request := buildMistralChatRequest(*model, Context{}, options)
				if request.PromptMode != "" || request.ReasoningEffort != model.ThinkingLevelMap[requested] {
					t.Fatalf("%s sent incorrect reasoning controls: %+v", requested, request)
				}
			}
		})
	}
}

func TestSeptemberUpstreamCatalog(t *testing.T) {
	for _, provider := range []Provider{"openai", "openai-codex"} {
		model := GetModel(provider, "gpt-6-astra")
		if model == nil {
			t.Fatalf("%s Astra missing", provider)
		}
		if model.ContextWindow != 272000 || model.MaxTokens != 128000 || !slices.Contains(model.Input, InputImage) {
			t.Fatalf("unexpected Astra catalog: %+v", model)
		}
		wantLevels := []ModelThinkingLevel{ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax}
		if provider == "openai-codex" {
			wantLevels = append([]ModelThinkingLevel{ModelThinkingLevelMinimal}, wantLevels...)
		}
		if got := GetSupportedThinkingLevels(*model); !slices.Equal(got, wantLevels) {
			t.Fatalf("Astra levels: %v", got)
		}
		if got := ClampThinkingLevel(*model, ModelThinkingLevelOff); got != wantLevels[0] {
			t.Fatalf("Astra off clamp: %s", got)
		}
		if model.Cost.Input != 10 || model.Cost.Output != 50 || len(model.CostTiers) != 1 {
			t.Fatalf("Astra cost snapshot: %+v", model)
		}
	}
	for _, id := range []string{"gpt-5.4", "gpt-5.4-mini"} {
		if GetModel("openai-codex", id) != nil {
			t.Fatalf("retired Codex model remains: %s", id)
		}
		if GetModel("openai", id) == nil {
			t.Fatalf("OpenAI model incorrectly removed: %s", id)
		}
	}
	if GetModel("deepseek", "deepseek-v4-flash") != nil {
		t.Fatal("retired native Flash remains")
	}
	flash := GetModel("deepseek", "deepseek-flash")
	if flash == nil || flash.ContextWindow != 1000000 || flash.MaxTokens != 384000 || !slices.Contains(flash.Input, InputImage) {
		t.Fatalf("unexpected Flash: %+v", flash)
	}
	if GetModel("opencode-go", "deepseek-v4-flash") == nil {
		t.Fatal("OpenCode provider id was incorrectly renamed")
	}
}

func TestDeepSeekFlashImageAndReasoningWire(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	request := buildDeepSeekChatRequest(model, Context{Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "describe"}, ImageContent{Data: "aW1n", MIMEType: "image/png"}}}}}, ProviderStreamOptions{Reasoning: ThinkingLevelLow})
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "low" {
		t.Fatalf("effort must be top-level: %s", wire)
	}
	if _, present := body["thinking"].(map[string]any)["reasoning_effort"]; present {
		t.Fatalf("effort nested inside thinking: %s", wire)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if got := content[1].(map[string]any)["image_url"].(map[string]any)["url"]; got != "data:image/png;base64,aW1n" {
		t.Fatalf("image lost: %s", wire)
	}
	off := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: ThinkingLevel("off")})
	if off.Thinking.Type != "disabled" || off.ReasoningEffort != "" {
		t.Fatalf("off enables thinking: %+v", off)
	}
}

func TestNewCompatFactsAreIndependentCopies(t *testing.T) {
	model := GetModel("openai", "gpt-6-astra")
	*model.Compat.(*OpenAIResponsesCompat).SupportsExplicitPromptCacheMode = false
	if !*GetModel("openai", "gpt-6-astra").Compat.(*OpenAIResponsesCompat).SupportsExplicitPromptCacheMode {
		t.Fatal("catalog mutated through returned model")
	}
	flag := true
	original := Model{Compat: &AnthropicMessagesCompat{ForceAdaptiveThinking: &flag}}
	copied := cloneModel(original)
	*copied.Compat.(*AnthropicMessagesCompat).ForceAdaptiveThinking = false
	if !flag {
		t.Fatal("adaptive thinking fact aliases input")
	}
}

func TestAnthropicAdaptiveThinkingCatalogOverride(t *testing.T) {
	enabled := true
	model := Model{ID: "claude-fable-5", Reasoning: true, Compat: &AnthropicMessagesCompat{ForceAdaptiveThinking: &enabled}, ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelXHigh: "xhigh"}}
	request := buildAnthropicRequest(model, Context{Tools: []Tool{{Name: "lookup"}}}, AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{Reasoning: ThinkingLevelXHigh}}, false, true)
	if request.Thinking.(map[string]any)["type"] != "adaptive" || request.OutputConfig == nil || request.OutputConfig.Effort != "xhigh" {
		t.Fatalf("missing catalog effort: %+v", request)
	}
	encoded, err := json.Marshal(request.Tools)
	if err != nil {
		t.Fatal(err)
	}
	var tools []map[string]any
	if err := json.Unmarshal(encoded, &tools); err != nil {
		t.Fatal(err)
	}
	if _, present := tools[0]["cache_control"]; present {
		t.Fatalf("unexpected tool cache control: %s", encoded)
	}
}

func TestDeepSeekFlashToolImagesAndMinimalReasoning(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	messages := []Message{
		AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, Content: []ContentBlock{ToolCall{ID: "shot", Name: "screenshot", Arguments: map[string]any{}}}, StopReason: StopReasonToolUse},
		ToolResultMessage{ToolCallID: "shot", ToolName: "screenshot", Content: []ContentBlock{ImageContent{Data: "aW1n", MIMEType: "image/png"}}},
	}
	request := buildDeepSeekChatRequest(model, Context{Messages: messages}, ProviderStreamOptions{Reasoning: ThinkingLevelMinimal})
	if request.ReasoningEffort != "low" {
		t.Fatalf("minimal effort = %q, want low", request.ReasoningEffort)
	}
	wire, err := json.Marshal(request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var sent []map[string]any
	if err := json.Unmarshal(wire, &sent); err != nil {
		t.Fatal(err)
	}
	parts, ok := sent[1]["content"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("tool image omitted: %s", wire)
	}
	if got := parts[0].(map[string]any)["image_url"].(map[string]any)["url"]; got != "data:image/png;base64,aW1n" {
		t.Fatalf("tool image lost: %s", wire)
	}
}

func TestResponsesCompatSwitchCopiesAreIndependent(t *testing.T) {
	for name, selectFlag := range map[string]func(*OpenAIResponsesCompat) **bool{
		"session header": func(c *OpenAIResponsesCompat) **bool { return &c.SendSessionIdHeader },
		"long cache":     func(c *OpenAIResponsesCompat) **bool { return &c.SupportsLongCacheRetention },
		"explicit cache": func(c *OpenAIResponsesCompat) **bool { return &c.SupportsExplicitPromptCacheMode },
		"output limit":   func(c *OpenAIResponsesCompat) **bool { return &c.SupportsMaxOutputTokens },
	} {
		t.Run(name, func(t *testing.T) {
			original := &OpenAIResponsesCompat{}
			if *selectFlag(cloneCompat(original).(*OpenAIResponsesCompat)) != nil {
				t.Fatal("unset capability became known")
			}
			enabled := true
			*selectFlag(original) = &enabled
			copied := cloneCompat(original).(*OpenAIResponsesCompat)
			**selectFlag(copied) = false
			if !**selectFlag(original) {
				t.Fatal("returned model aliases the original capability")
			}
		})
	}
}

func TestNativeDeepSeekThinkingCapabilitiesAndWire(t *testing.T) {
	for _, test := range []struct {
		id      string
		levels  []ModelThinkingLevel
		minimal string
	}{
		{"deepseek-flash", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelLow, ModelThinkingLevelHigh, ModelThinkingLevelMax}, "low"},
		{"deepseek-v4-pro", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh, ModelThinkingLevelMax}, "high"},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := *GetModel("deepseek", test.id)
			if got := GetSupportedThinkingLevels(model); !slices.Equal(got, test.levels) {
				t.Fatalf("supported levels = %v, want %v", got, test.levels)
			}
			for _, requested := range []ThinkingLevel{ThinkingLevelMinimal, ThinkingLevelLow} {
				request := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: requested})
				if request.ReasoningEffort != test.minimal {
					t.Fatalf("requested %s emits %s, want %s", requested, request.ReasoningEffort, test.minimal)
				}
			}
			request := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: ThinkingLevelXHigh})
			if request.ReasoningEffort != "max" {
				t.Fatalf("xhigh should clamp to max, got %s", request.ReasoningEffort)
			}
		})
	}
}
