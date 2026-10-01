package pigo

import "strings"

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

// Snapshot of pi-mono 8ce69e9d2 and models.dev active tool-calling metadata
// (2026-10-01). OpenCode Go reports tokens without estimated API costs.
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
		ID:               "deepseek-v4.1-flash",
		Name:             "DeepSeek V4.1 Flash",
		API:              "openai-completions",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: "max"},
		ImageInput:       true,
		ContextWindow:    1000000,
		MaxTokens:        384000,
		Compat:           newOpenCodeGoCompletionsCompat("openai", true, true),
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
		ID:               "glm-5.3-flash",
		Name:             "GLM-5.3-Flash",
		API:              "openai-completions",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: "max"},
		ImageInput:       true,
		ContextWindow:    1000000,
		MaxTokens:        131072,
		Compat:           newOpenCodeGoCompletionsCompat("openai", true, true),
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
		Compat:        newOpenCodeGoLunaCompat(),
	},
	{
		ID:               "gpt-6-luna",
		Name:             "GPT-6 Luna",
		API:              "openai-responses",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: "max"},
		ImageInput:       true,
		ContextWindow:    1050000,
		MaxTokens:        128000,
		Compat:           newOpenCodeGoLunaCompat(),
	},
	{
		ID:               "grok-4.6",
		Name:             "Grok 4.6",
		API:              "openai-responses",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: ""},
		ImageInput:       true,
		ContextWindow:    500000,
		MaxTokens:        500000,
	},
	{
		ID:               "grok-4.7",
		Name:             "Grok 4.7",
		API:              "openai-responses",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: ""},
		ImageInput:       true,
		ContextWindow:    500000,
		MaxTokens:        500000,
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
		MaxTokens:     128_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
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
		Compat:        newOpenCodeGoKimiK3Compat(),
	},
	{
		ID:            "longcat-2.0",
		Name:          "LongCat-2.0",
		API:           "openai-completions",
		Reasoning:     true,
		ContextWindow: 1000000,
		MaxTokens:     131072,
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
		ID:            "mimo-v2.6-flash",
		Name:          "MiMo-V2.6-Flash",
		API:           "openai-completions",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1048576,
		MaxTokens:     131072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "mimo-v2.6-pro",
		Name:          "MiMo-V2.6-Pro",
		API:           "openai-completions",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1048576,
		MaxTokens:     131072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
	},
	{
		ID:            "minimax-m2.7",
		Name:          "MiniMax-M2.7",
		API:           "openai-completions",
		Reasoning:     true,
		ContextWindow: 204_800,
		MaxTokens:     131_072,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
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
		ID:               "muse-spark-1.3-contributor",
		Name:             "Muse Spark 1.3 Contributor",
		API:              "openai-responses",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "minimal", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: ""},
		ImageInput:       true,
		ContextWindow:    1048576,
		MaxTokens:        131072,
	},
	{
		ID:            "qwen3.7-plus",
		Name:          "Qwen3.7 Plus",
		API:           "openai-completions",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1_000_000,
		Compat:        newOpenCodeGoCompletionsCompat("openai", true, true),
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen3.8-flash",
		Name:          "Qwen3.8 Flash",
		API:           "anthropic-messages",
		Reasoning:     true,
		ImageInput:    true,
		ContextWindow: 1000000,
		MaxTokens:     131072,
		Compat:        newOpenCodeGoQwenFlashCompat(),
	},
	{
		ID:               "qwen3.8-max",
		Name:             "Qwen3.8 Max",
		API:              "openai-completions",
		Reasoning:        true,
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: ""},
		ImageInput:       true,
		ContextWindow:    1_000_000,
		Compat:           newOpenCodeGoCompletionsCompat("openai", true, true),
		MaxTokens:        131_072,
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
			SupportsSession:    true,
		},
		BuildOptions:     buildOpenCodeGoProviderStreamOptions,
		NormalizeOptions: normalizeOpenCodeGoProviderStreamOptions,
		Models:           models,
	}
}

func buildOpenCodeGoProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	var result ProviderStreamOptions
	switch model.API {
	case "anthropic-messages":
		result = buildAnthropicMessagesProviderStreamOptions(model, options)
	case "openai-responses":
		result = buildOpenAIResponsesProviderStreamOptions(model, options)
	case "openai-completions":
		result = buildOpenAICompletionsProviderStreamOptions(model, options)
	default:
		result = buildBaseProviderStreamOptions(model, options)
	}
	return withOpenCodeGoSessionHeader(result)
}

func normalizeOpenCodeGoProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	var result ProviderStreamOptions
	switch model.API {
	case "anthropic-messages":
		result = resolveAnthropicMessagesProviderOptions(model, options).toProviderStreamOptions(model)
	case "openai-responses":
		result = normalizeOpenAIResponsesProviderStreamOptions(model, options)
	case "openai-completions":
		result = normalizeOpenAICompletionsProviderStreamOptions(model, options)
	default:
		result = streamOptionsFromProvider(model, options).providerStreamOptions(model)
	}
	return withOpenCodeGoSessionHeader(result)
}

func withOpenCodeGoSessionHeader(options ProviderStreamOptions) ProviderStreamOptions {
	if options.SessionID == "" {
		return options
	}
	for name := range options.Headers {
		if strings.EqualFold(name, "x-opencode-session") {
			return options
		}
	}
	options.Headers = mergeRequestHeaders(options.Headers, map[string]string{"x-opencode-session": options.SessionID})
	return options
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

func newOpenCodeGoQwenFlashCompat() *AnthropicMessagesCompat {
	allowEmptySignature := true
	return &AnthropicMessagesCompat{AllowEmptySignature: &allowEmptySignature}
}

func newOpenCodeGoLunaCompat() *OpenAIResponsesCompat {
	supportsMidConvoSystemMessages := true
	return &OpenAIResponsesCompat{
		SupportsJSONOutput:             true,
		SupportsJSONSchema:             true,
		SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages,
	}
}

func newOpenCodeGoKimiK3Compat() *OpenAICompletionsCompat {
	supportsMidConvoSystemMessages := true
	compat := newOpenCodeGoCompletionsCompat("openai", true, true)
	compat.SupportsMidConvoSystemMessages = &supportsMidConvoSystemMessages
	return compat
}
