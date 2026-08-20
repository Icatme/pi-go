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
	ContextWindow    int
	MaxTokens        int
	Compat           ProviderCompat
}

// This is the 2026-08-20 intersection of OpenCode Go's documented current
// model list and models.dev's active, non-deprecated tool-calling catalog.
var openCodeGoModelSpecs = []openCodeGoModelSpec{
	{
		ID:        "deepseek-v4-flash",
		Name:      "DeepSeek V4 Flash",
		API:       "openai-completions",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "",
			ModelThinkingLevelMax:     "max",
		},
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
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "",
			ModelThinkingLevelMax:     "max",
		},
		ContextWindow: 1_000_000,
		MaxTokens:     384_000,
		Compat:        newOpenCodeGoDeepSeekV4Compat(),
	},
	{
		ID:            "glm-5.1",
		Name:          "GLM-5.1",
		API:           "openai-completions",
		Reasoning:     true,
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
			ModelThinkingLevelXHigh:   "",
			ModelThinkingLevelMax:     "max",
		},
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:        "glm-5.3",
		Name:      "GLM-5.3",
		API:       "openai-completions",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "",
			ModelThinkingLevelMax:     "max",
		},
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:         "gpt-5.6-luna",
		Name:       "GPT-5.6 Luna",
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
			ModelThinkingLevelMax:     "max",
		},
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
			ModelThinkingLevelXHigh:   "",
			ModelThinkingLevelMax:     "max",
		},
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
		ContextWindow: 1_000_000,
		MaxTokens:     128_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "mimo-v2.5-pro",
		Name:          "MiMo V2.5 Pro",
		API:           "openai-completions",
		Reasoning:     true,
		ContextWindow: 1_048_576,
		MaxTokens:     128_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "minimax-m2.7",
		Name:          "MiniMax-M2.7",
		API:           "anthropic-messages",
		Reasoning:     true,
		ContextWindow: 204_800,
		MaxTokens:     131_072,
	},
	{
		ID:            "minimax-m3",
		Name:          "MiniMax-M3",
		API:           "anthropic-messages",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:         "muse-spark-1.2-contributor",
		Name:       "Muse Spark 1.2 Contributor",
		API:        "openai-responses",
		Reasoning:  true,
		ImageInput: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "",
			ModelThinkingLevelMinimal: "minimal",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "xhigh",
		},
		ContextWindow: 1_048_576,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen3.6-plus",
		Name:          "Qwen3.6 Plus",
		API:           "anthropic-messages",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen3.7-max",
		Name:          "Qwen3.7 Max",
		API:           "anthropic-messages",
		Reasoning:     true,
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen3.7-plus",
		Name:          "Qwen3.7 Plus",
		API:           "anthropic-messages",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen3.8-max",
		Name:          "Qwen3.8 Max",
		API:           "anthropic-messages",
		Reasoning:     true,
		ImageInput:    true,
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
