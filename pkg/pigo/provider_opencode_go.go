package pigo

const (
	openCodeGoBaseURL          = "https://opencode.ai/zen/go/v1"
	openCodeGoAnthropicBaseURL = "https://opencode.ai/zen/go"
)

type openCodeGoModelSpec struct {
	ID               string
	Name             string
	API              API
	Reasoning        bool
	ThinkingLevelMap ThinkingLevelMap
	ImageInput       bool
	Cost             UsageCost
	CostTiers        []ModelCostTier
	ContextWindow    int
	MaxTokens        int
	Compat           ProviderCompat
}

// This is the 2026-08-08 models.dev snapshot used by Pi's generated
// OpenCode Go catalog: active, non-deprecated models with tool calling.
var openCodeGoModelSpecs = []openCodeGoModelSpec{
	{
		ID:        "deepseek-v4-flash",
		Name:      "DeepSeek V4 Flash (2x usage)",
		API:       "openai-completions",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "max",
		},
		Cost:          UsageCost{Input: 0.07, Output: 0.14, CacheRead: 0.0014},
		ContextWindow: 1_000_000,
		MaxTokens:     384_000,
		Compat:        newOpenCodeGoDeepSeekV4Compat(),
	},
	{
		ID:        "deepseek-v4-pro",
		Name:      "DeepSeek V4 Pro",
		API:       "openai-completions",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "max",
		},
		Cost:          UsageCost{Input: 0.435, Output: 0.87, CacheRead: 0.003625},
		ContextWindow: 1_000_000,
		MaxTokens:     384_000,
		Compat:        newOpenCodeGoDeepSeekV4Compat(),
	},
	{
		ID:            "glm-5.1",
		Name:          "GLM-5.1",
		API:           "openai-completions",
		Reasoning:     true,
		Cost:          UsageCost{Input: 1.4, Output: 4.4, CacheRead: 0.26},
		ContextWindow: 202_752,
		MaxTokens:     32_768,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:        "glm-5.2",
		Name:      "GLM-5.2",
		API:       "openai-completions",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "max",
		},
		Cost:          UsageCost{Input: 1.4, Output: 4.4, CacheRead: 0.26},
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:         "gpt-5.6-luna",
		Name:       "GPT-5.6 Luna (2x usage)",
		API:        "openai-responses",
		Reasoning:  true,
		ImageInput: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "none",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "xhigh",
		},
		Cost: UsageCost{Input: 0.1, Output: 0.6, CacheRead: 0.01, CacheWrite: 0.125},
		CostTiers: []ModelCostTier{{
			InputTokensAbove: 272_000,
			Rates:            UsageCost{Input: 0.2, Output: 0.9, CacheRead: 0.02, CacheWrite: 0.25},
		}},
		ContextWindow: 1_050_000,
		MaxTokens:     128_000,
	},
	{
		ID:         "grok-4.5",
		Name:       "Grok 4.5",
		API:        "openai-responses",
		Reasoning:  true,
		ImageInput: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "",
		},
		Cost: UsageCost{Input: 2, Output: 6, CacheRead: 0.5},
		CostTiers: []ModelCostTier{{
			InputTokensAbove: 200_000,
			Rates:            UsageCost{Input: 4, Output: 12, CacheRead: 1},
		}},
		ContextWindow: 500_000,
		MaxTokens:     500_000,
	},
	{
		ID:        "hy3",
		Name:      "Hy3",
		API:       "openai-completions",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "none",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "",
		},
		Cost:          UsageCost{Input: 0.14, Output: 0.58, CacheRead: 0.035},
		ContextWindow: 256_000,
		MaxTokens:     64_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:         "kimi-k2.6",
		Name:       "Kimi K2.6",
		API:        "openai-completions",
		Reasoning:  true,
		ImageInput: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "",
		},
		Cost:          UsageCost{Input: 0.95, Output: 4, CacheRead: 0.16},
		ContextWindow: 262_144,
		MaxTokens:     65_536,
		Compat:        newOpenCodeGoCompletionsCompat("deepseek", false, false),
	},
	{
		ID:            "kimi-k2.7-code",
		Name:          "Kimi K2.7 Code",
		API:           "openai-completions",
		Reasoning:     true,
		ImageInput:    true,
		Cost:          UsageCost{Input: 0.95, Output: 4, CacheRead: 0.19},
		ContextWindow: 262_144,
		MaxTokens:     262_144,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:         "kimi-k3",
		Name:       "Kimi K3",
		API:        "openai-completions",
		Reasoning:  true,
		ImageInput: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "",
			ModelThinkingLevelXHigh:   "max",
		},
		Cost:          UsageCost{Input: 3, Output: 15, CacheRead: 0.3},
		ContextWindow: 1_048_576,
		MaxTokens:     131_072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "mimo-v2.5",
		Name:          "MiMo V2.5",
		API:           "openai-completions",
		Reasoning:     true,
		ImageInput:    true,
		Cost:          UsageCost{Input: 0.14, Output: 0.28, CacheRead: 0.0028},
		ContextWindow: 1_000_000,
		MaxTokens:     128_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "mimo-v2.5-pro",
		Name:          "MiMo V2.5 Pro",
		API:           "openai-completions",
		Reasoning:     true,
		Cost:          UsageCost{Input: 0.435, Output: 0.87, CacheRead: 0.003625},
		ContextWindow: 1_048_576,
		MaxTokens:     128_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "minimax-m2.7",
		Name:          "MiniMax-M2.7",
		API:           "openai-completions",
		Reasoning:     true,
		Cost:          UsageCost{Input: 0.3, Output: 1.2, CacheRead: 0.06},
		ContextWindow: 204_800,
		MaxTokens:     131_072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:         "minimax-m3",
		Name:       "MiniMax-M3",
		API:        "anthropic-messages",
		Reasoning:  true,
		ImageInput: true,
		Cost:       UsageCost{Input: 0.3, Output: 1.2, CacheRead: 0.06},
		CostTiers: []ModelCostTier{{
			InputTokensAbove: 512_000,
			Rates:            UsageCost{Input: 0.6, Output: 2.4, CacheRead: 0.12},
		}},
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:         "qwen3.6-plus",
		Name:       "Qwen3.6 Plus",
		API:        "openai-completions",
		Reasoning:  true,
		ImageInput: true,
		Cost:       UsageCost{Input: 0.5, Output: 3, CacheRead: 0.05, CacheWrite: 0.625},
		CostTiers: []ModelCostTier{{
			InputTokensAbove: 256_000,
			Rates:            UsageCost{Input: 2, Output: 6, CacheRead: 0.2, CacheWrite: 2.5},
		}},
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
		Compat:        newOpenCodeGoCompletionsCompat("qwen", true, true),
	},
	{
		ID:            "qwen3.7-max",
		Name:          "Qwen3.7 Max",
		API:           "anthropic-messages",
		Reasoning:     true,
		Cost:          UsageCost{Input: 2.5, Output: 7.5, CacheRead: 0.5, CacheWrite: 3.125},
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:         "qwen3.7-plus",
		Name:       "Qwen3.7 Plus",
		API:        "anthropic-messages",
		Reasoning:  true,
		ImageInput: true,
		Cost:       UsageCost{Input: 0.4, Output: 1.6, CacheRead: 0.04, CacheWrite: 0.5},
		CostTiers: []ModelCostTier{{
			InputTokensAbove: 256_000,
			Rates:            UsageCost{Input: 1.2, Output: 4.8, CacheRead: 0.12, CacheWrite: 1.5},
		}},
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen3.8-max",
		Name:          "Qwen3.8 Max",
		API:           "anthropic-messages",
		Reasoning:     true,
		ImageInput:    true,
		Cost:          UsageCost{Input: 2, Output: 6, CacheRead: 0.25, CacheWrite: 2.5},
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
}

func newOpenCodeGoProviderModule() ProviderModule {
	models := make(map[string]Model, len(openCodeGoModelSpecs))
	for _, spec := range openCodeGoModelSpecs {
		baseURL := openCodeGoBaseURL
		if spec.API == "anthropic-messages" {
			baseURL = openCodeGoAnthropicBaseURL
		}
		input := []InputType{InputText}
		if spec.ImageInput {
			input = append(input, InputImage)
		}
		models[spec.ID] = Model{
			ID:               spec.ID,
			Name:             spec.Name,
			API:              spec.API,
			BaseURL:          baseURL,
			Reasoning:        spec.Reasoning,
			ThinkingLevelMap: spec.ThinkingLevelMap,
			Input:            input,
			Cost:             spec.Cost,
			CostTiers:        spec.CostTiers,
			ContextWindow:    spec.ContextWindow,
			MaxTokens:        spec.MaxTokens,
			Compat:           spec.Compat,
		}
	}
	return ProviderModule{
		Provider: "opencode-go",
		Auth: ProviderAuth{
			EnvAPIKeyName: "OPENCODE_API_KEY",
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:  true,
			SupportsToolChoice: true,
		},
		BuildOptions:     buildOpenCodeGoProviderStreamOptions,
		NormalizeOptions: normalizeOpenCodeGoProviderStreamOptions,
		Models:           models,
	}
}

func buildOpenCodeGoProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	switch model.API {
	case "anthropic-messages":
		return buildAnthropicMessagesProviderStreamOptions(model, options)
	case "openai-responses":
		return buildOpenAIResponsesProviderStreamOptions(model, options)
	case "openai-completions":
		return buildOpenAICompletionsProviderStreamOptions(model, options)
	default:
		return buildBaseProviderStreamOptions(model, options)
	}
}

func normalizeOpenCodeGoProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	switch model.API {
	case "anthropic-messages":
		return resolveAnthropicMessagesProviderOptions(model, options).toProviderStreamOptions(model)
	case "openai-responses":
		return normalizeOpenAIResponsesProviderStreamOptions(model, options)
	case "openai-completions":
		return normalizeOpenAICompletionsProviderStreamOptions(model, options)
	default:
		return streamOptionsFromProvider(model, options).providerStreamOptions(model)
	}
}

func newOpenCodeGoCompletionsCompat(thinkingFormat string, supportsReasoningEffort bool, supportsLongCacheRetention bool) *OpenAICompletionsCompat {
	supportsStore := false
	supportsDeveloperRole := false
	supportsUsageInStreaming := true
	supportsStrictMode := true
	return &OpenAICompletionsCompat{
		SupportsStore:              &supportsStore,
		SupportsDeveloperRole:      &supportsDeveloperRole,
		SupportsReasoningEffort:    &supportsReasoningEffort,
		SupportsUsageInStreaming:   &supportsUsageInStreaming,
		MaxTokensField:             "max_tokens",
		ThinkingFormat:             thinkingFormat,
		SupportsStrictMode:         &supportsStrictMode,
		SupportsLongCacheRetention: &supportsLongCacheRetention,
	}
}

func newOpenCodeGoDeepSeekV4Compat() *OpenAICompletionsCompat {
	requiresReasoningContent := true
	compat := newOpenCodeGoCompletionsCompat("deepseek", true, true)
	compat.RequiresReasoningContentOnAssistantMessages = &requiresReasoningContent
	return compat
}
