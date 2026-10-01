package pigo

func init() {
	registerBuiltInModules()
}

func registerBuiltInModules() {
	registerBuiltInAPIModules()
	registerBuiltInProviderModules()
}

func registerBuiltInAPIModules() {
	RegisterLazyAPIModule("anthropic-messages", newAnthropicMessagesAPIModule)
	RegisterLazyAPIModule("commandcode-custom", newCommandCodeAPIModule)
	RegisterLazyAPIModule("deepseek-chat-completions", newDeepSeekChatCompletionsAPIModule)
	RegisterLazyAPIModule("google-generative-ai", newGoogleGenerativeAIAPIModule)
	RegisterLazyAPIModule("mistral-conversations", newMistralConversationsAPIModule)
	RegisterLazyAPIModule("openai-codex-responses", newOpenAICodexResponsesAPIModule)
	RegisterLazyAPIModule("openai-completions", newOpenAICompletionsAPIModule)
	RegisterLazyAPIModule("openai-responses", newOpenAIResponsesAPIModule)
}

func registerBuiltInProviderModules() {
	RegisterLazyProviderModule("anthropic", newAnthropicProviderModule)
	RegisterLazyProviderModule("commandcode", newCommandCodeProviderModule)
	RegisterLazyProviderModule("deepseek", newDeepSeekProviderModule)
	RegisterLazyProviderModule("google", newGoogleProviderModule)
	RegisterLazyProviderModule("mistral", newMistralProviderModule)
	RegisterLazyProviderModule("openai-codex", newOpenAICodexProviderModule)
	RegisterLazyProviderModule("opencode-go", newOpenCodeGoProviderModule)
	RegisterLazyProviderModule("openai", newOpenAIResponsesProviderModule)
	RegisterLazyProviderModule("kimi-coding", newKimiCodingProviderModule)
	RegisterLazyProviderModule("tokenharbor", newTokenHarborProviderModule)
}

func newAnthropicMessagesAPIModule() APIModule {
	return APIModule{
		API:          "anthropic-messages",
		Stream:       streamAnthropicMessages,
		StreamSimple: streamSimpleAnthropicMessages,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilityUnknown,
			TopP:              CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
	}
}

func newDeepSeekChatCompletionsAPIModule() APIModule {
	return APIModule{
		API:          "deepseek-chat-completions",
		Stream:       streamDeepSeekChatCompletions,
		StreamSimple: streamSimpleDeepSeekChatCompletions,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilitySupported,
			TopP:              CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
	}
}

func newGoogleGenerativeAIAPIModule() APIModule {
	return APIModule{
		API:          "google-generative-ai",
		Stream:       streamGoogle,
		StreamSimple: streamSimpleGoogle,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilitySupported,
			TopP:              CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
	}
}

func newMistralConversationsAPIModule() APIModule {
	return APIModule{
		API:          "mistral-conversations",
		Stream:       streamMistral,
		StreamSimple: streamSimpleMistral,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilitySupported,
			TopP:              CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
	}
}

func newOpenAICodexResponsesAPIModule() APIModule {
	return APIModule{
		API:          "openai-codex-responses",
		Stream:       streamOpenAICodex,
		StreamSimple: streamSimpleOpenAICodex,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilitySupported,
			TopP:              CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
	}
}

func newOpenAICompletionsAPIModule() APIModule {
	return APIModule{
		API:          "openai-completions",
		Stream:       streamOpenAICompletions,
		StreamSimple: streamSimpleOpenAICompletions,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilitySupported,
			TopP:              CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
	}
}

func newOpenAIResponsesAPIModule() APIModule {
	return APIModule{
		API:          "openai-responses",
		Stream:       streamOpenAIResponses,
		StreamSimple: streamSimpleOpenAIResponses,
		Capabilities: ModelCapabilities{
			Tools:             CapabilitySupported,
			StrictTools:       CapabilityUnsupported,
			ToolChoice:        CapabilitySupported,
			Temperature:       CapabilityUnknown,
			TopP:              CapabilityUnknown,
			ParallelToolCalls: CapabilitySupported,
		},
	}
}

func newAnthropicProviderModule() ProviderModule {
	forceAdaptiveThinking := true
	return ProviderModule{
		Provider: "anthropic",
		Auth: ProviderAuth{
			EnvAPIKeyNames: []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:          true,
			SupportsPromptCacheControl: true,
			SupportsThinkingBudget:     true,
			SupportsToolChoice:         true,
		},
		BuildOptions: buildAnthropicMessagesProviderStreamOptions,
		Models: map[string]Model{
			"claude-opus-5-5": {
				ID:               "claude-opus-5-5",
				Name:             "Claude Opus 5.5",
				API:              "anthropic-messages",
				BaseURL:          "https://api.anthropic.com",
				Reasoning:        true,
				ThinkingLevelMap: claude55ThinkingLevelMap(),
				Compat:           &AnthropicMessagesCompat{ForceAdaptiveThinking: &forceAdaptiveThinking},
				Capabilities:     ModelCapabilities{DefaultReasoningLevel: ModelThinkingLevelMedium, Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported},
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5},
				ContextWindow:    1000000,
				MaxTokens:        128000,
			},
			"claude-sonnet-5-5": {
				ID:               "claude-sonnet-5-5",
				Name:             "Claude Sonnet 5.5",
				API:              "anthropic-messages",
				BaseURL:          "https://api.anthropic.com",
				Reasoning:        true,
				ThinkingLevelMap: claude55ThinkingLevelMap(),
				Compat:           &AnthropicMessagesCompat{ForceAdaptiveThinking: &forceAdaptiveThinking},
				Capabilities:     ModelCapabilities{DefaultReasoningLevel: ModelThinkingLevelHigh, Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported},
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5},
				ContextWindow:    1000000,
				MaxTokens:        128000,
			},
			"claude-opus-4-6": {
				ID:            "claude-opus-4-6",
				Name:          "Claude Opus 4.6",
				API:           "anthropic-messages",
				BaseURL:       "https://api.anthropic.com",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				ContextWindow: 200000,
				MaxTokens:     32000,
			},
			"claude-sonnet-4-5": {
				ID:            "claude-sonnet-4-5",
				Name:          "Claude Sonnet 4.5",
				API:           "anthropic-messages",
				BaseURL:       "https://api.anthropic.com",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				ContextWindow: 200000,
				MaxTokens:     32000,
			},
		},
	}
}

func newDeepSeekProviderModule() ProviderModule {
	return ProviderModule{
		Provider: "deepseek",
		Auth: ProviderAuth{
			EnvAPIKeyName: "DEEPSEEK_KEY",
			EnvAPIKeyNames: []string{
				"DEEPSEEK_KEY",
				"DEEPSEEK_API_KEY",
			},
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:  true,
			SupportsJSONOutput: true,
			SupportsToolChoice: true,
		},
		BuildOptions:     buildDeepSeekProviderStreamOptions,
		NormalizeOptions: normalizeDeepSeekProviderStreamOptions,
		Models: map[string]Model{
			"deepseek-flash": {
				ID:        "deepseek-flash",
				Name:      "DeepSeek V4.1 Flash",
				API:       "deepseek-chat-completions",
				BaseURL:   "https://api.deepseek.com",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ModelThinkingLevelMinimal: "",
					ModelThinkingLevelLow:     "low",
					ModelThinkingLevelMedium:  "",
					ModelThinkingLevelHigh:    "high",
					ModelThinkingLevelXHigh:   "",
					ModelThinkingLevelMax:     "max",
				},
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.3, Output: 1.2, CacheRead: 0.006},
				ContextWindow: 1000000,
				MaxTokens:     384000,
			},
			"deepseek-v4-pro": {
				ID:        "deepseek-v4-pro",
				Name:      "DeepSeek V4 Pro",
				API:       "deepseek-chat-completions",
				BaseURL:   "https://api.deepseek.com",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ModelThinkingLevelMinimal: "",
					ModelThinkingLevelLow:     "",
					ModelThinkingLevelMedium:  "",
					ModelThinkingLevelHigh:    "high",
					ModelThinkingLevelXHigh:   "",
					ModelThinkingLevelMax:     "max",
				},
				Input:         []InputType{InputText},
				Cost:          UsageCost{Input: 1.32, Output: 3.96, CacheRead: 0.044},
				ContextWindow: 1000000,
				MaxTokens:     384000,
			},
		},
	}
}

func newGoogleProviderModule() ProviderModule {
	return ProviderModule{
		Provider: "google",
		Auth: ProviderAuth{
			EnvAPIKeyName: "GOOGLE_API_KEY",
			EnvAPIKeyNames: []string{
				"GOOGLE_API_KEY",
				"GEMINI_API_KEY",
			},
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:  true,
			SupportsToolChoice: true,
		},
		BuildOptions:     buildGoogleProviderStreamOptions,
		NormalizeOptions: normalizeGoogleProviderStreamOptions,
		Models: map[string]Model{
			"gemini-2.5-pro": {
				ID:               "gemini-2.5-pro",
				Name:             "Gemini 2.5 Pro",
				API:              "google-generative-ai",
				BaseURL:          "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: ""},
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 1.25, Output: 10, CacheRead: 0.3125},
				ContextWindow:    1048576,
				MaxTokens:        65536,
			},
			"gemini-2.5-flash": {
				ID:            "gemini-2.5-flash",
				Name:          "Gemini 2.5 Flash",
				API:           "google-generative-ai",
				BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.3, Output: 2.5, CacheRead: 0.03},
				ContextWindow: 1048576,
				MaxTokens:     65536,
			},
			"gemini-2.5-flash-lite": {
				ID:            "gemini-2.5-flash-lite",
				Name:          "Gemini 2.5 Flash Lite",
				API:           "google-generative-ai",
				BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.1, Output: 0.4, CacheRead: 0.025},
				ContextWindow: 1048576,
				MaxTokens:     65536,
			},
			"gemini-2.0-flash": {
				ID:            "gemini-2.0-flash",
				Name:          "Gemini 2.0 Flash",
				API:           "google-generative-ai",
				BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:     false,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.1, Output: 0.4, CacheRead: 0.025},
				ContextWindow: 1048576,
				MaxTokens:     8192,
			},
			"gemini-2.0-flash-lite": {
				ID:            "gemini-2.0-flash-lite",
				Name:          "Gemini 2.0 Flash Lite",
				API:           "google-generative-ai",
				BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:     false,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.075, Output: 0.3},
				ContextWindow: 1048576,
				MaxTokens:     8192,
			},
		},
	}
}

func newMistralProviderModule() ProviderModule {
	return ProviderModule{
		Provider: "mistral",
		Auth: ProviderAuth{
			EnvAPIKeyName: "MISTRAL_API_KEY",
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:  true,
			SupportsToolChoice: true,
		},
		BuildOptions:     buildMistralProviderStreamOptions,
		NormalizeOptions: normalizeMistralProviderStreamOptions,
		Models: map[string]Model{
			"mistral-medium-latest": {
				ID:               "mistral-medium-latest",
				Name:             "Mistral Medium (latest)",
				API:              "mistral-conversations",
				BaseURL:          "https://api.mistral.ai",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: ""},
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 1.5, Output: 7.5, CacheRead: 0.15},
				ContextWindow:    262144,
				MaxTokens:        262144,
			},
			"mistral-medium-3.5": {
				ID:               "mistral-medium-3.5",
				Name:             "Mistral Medium 3.5",
				API:              "mistral-conversations",
				BaseURL:          "https://api.mistral.ai",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: ""},
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 1.5, Output: 7.5, CacheRead: 0},
				ContextWindow:    262144,
				MaxTokens:        262144,
			},
			"zai-glm-5-2": {
				ID:               "zai-glm-5-2",
				Name:             "GLM-5.2",
				API:              "mistral-conversations",
				BaseURL:          "https://api.mistral.ai",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: "max"},
				Input:            []InputType{InputText},
				Cost:             UsageCost{Input: 1.4, Output: 4.4, CacheRead: 0.14},
				ContextWindow:    1000000,
				MaxTokens:        131072,
			},
			"zai-glm-5-3": {
				ID:               "zai-glm-5-3",
				Name:             "GLM-5.3",
				API:              "mistral-conversations",
				BaseURL:          "https://api.mistral.ai",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: "max"},
				Input:            []InputType{InputText},
				Cost:             UsageCost{Input: 1.4, Output: 4.4, CacheRead: 0.14},
				ContextWindow:    1000000,
				MaxTokens:        131072,
			},
			"mistral-large-latest": {
				ID:            "mistral-large-latest",
				Name:          "Mistral Large",
				API:           "mistral-conversations",
				BaseURL:       "https://api.mistral.ai",
				Reasoning:     false,
				Input:         []InputType{InputText},
				Cost:          UsageCost{Input: 2, Output: 6},
				ContextWindow: 128000,
				MaxTokens:     4096,
			},
			"mistral-small-latest": {
				ID:               "mistral-small-latest",
				Name:             "Mistral Small (latest)",
				API:              "mistral-conversations",
				BaseURL:          "https://api.mistral.ai",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "", ModelThinkingLevelMedium: "", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "", ModelThinkingLevelMax: ""},
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 0.15, Output: 0.6, CacheRead: 0.015},
				ContextWindow:    256000,
				MaxTokens:        256000,
			},
			"pixtral-large-latest": {
				ID:            "pixtral-large-latest",
				Name:          "Pixtral Large",
				API:           "mistral-conversations",
				BaseURL:       "https://api.mistral.ai",
				Reasoning:     false,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 2, Output: 6},
				ContextWindow: 128000,
				MaxTokens:     4096,
			},
			"codestral-latest": {
				ID:            "codestral-latest",
				Name:          "Codestral",
				API:           "mistral-conversations",
				BaseURL:       "https://api.mistral.ai",
				Reasoning:     false,
				Input:         []InputType{InputText},
				Cost:          UsageCost{Input: 0.3, Output: 0.9},
				ContextWindow: 256000,
				MaxTokens:     4096,
			},
			"ministral-3-8b-instruct": {
				ID:            "ministral-3-8b-instruct",
				Name:          "Ministral 3 8B",
				API:           "mistral-conversations",
				BaseURL:       "https://api.mistral.ai",
				Reasoning:     false,
				Input:         []InputType{InputText},
				Cost:          UsageCost{Input: 0.1, Output: 0.1},
				ContextWindow: 128000,
				MaxTokens:     4096,
			},
		},
	}
}

func newOpenAICodexProviderModule() ProviderModule {
	supportsMidConvoSystemMessages := true
	return ProviderModule{
		Provider: "openai-codex",
		Auth: ProviderAuth{
			RequiresOAuth:        true,
			ResolveAuthorization: resolveOpenAICodexAuthorization,
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:        true,
			SupportsSession:          true,
			SupportsPromptCacheKey:   true,
			SupportsReasoningSummary: true,
			SupportsTextVerbosity:    true,
			SupportsToolChoice:       true,
		},
		BuildOptions:     buildOpenAICodexProviderStreamOptions,
		NormalizeOptions: normalizeOpenAICodexProviderStreamOptions,
		Models: map[string]Model{
			"gpt-6.1-sol": {
				ID:               "gpt-6.1-sol",
				Name:             "GPT-6.1 Sol",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6.1-sol", true),
				Capabilities:     ModelCapabilities{DefaultReasoningLevel: ModelThinkingLevelMedium, Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported},
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 4, Output: 15, CacheRead: 0.2, CacheWrite: 5}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-6-sol": {
				ID:               "gpt-6-sol",
				Name:             "GPT-6 Sol",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6-sol", true),
				Capabilities:     openAIResponsesSamplingCapabilities("", []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 4, Output: 15, CacheRead: 0.4, CacheWrite: 5}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-6-luna": {
				ID:               "gpt-6-luna",
				Name:             "GPT-6 Luna",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6-luna", true),
				Capabilities:     openAIResponsesSamplingCapabilities("", []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 0.2, Output: 0.75, CacheRead: 0.02, CacheWrite: 0.25}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-5.5": {
				ID:               "gpt-5.5",
				Name:             "GPT-5.5",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none", ModelThinkingLevelMinimal: "low", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: ""},
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 5, Output: 30, CacheRead: 0.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 10, Output: 45, CacheRead: 1}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-5.6-sol": {
				ID:               "gpt-5.6-sol",
				Name:             "GPT-5.6 Sol",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAICodexGPT56ThinkingLevelMap(),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 4, Output: 20, CacheRead: 0.4, CacheWrite: 5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 8, Output: 30, CacheRead: 0.8, CacheWrite: 10}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-5.6-terra": {
				ID:               "gpt-5.6-terra",
				Name:             "GPT-5.6 Terra",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAICodexGPT56ThinkingLevelMap(),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 2, Output: 12, CacheRead: 0.2, CacheWrite: 2.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 4, Output: 18, CacheRead: 0.4, CacheWrite: 5}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-5.6-luna": {
				ID:               "gpt-5.6-luna",
				Name:             "GPT-5.6 Luna",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAICodexGPT56ThinkingLevelMap(),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 0.2, Output: 1.2, CacheRead: 0.02, CacheWrite: 0.25},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 0.4, Output: 1.8, CacheRead: 0.04, CacheWrite: 0.5}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-6-astra": {
				ID:               "gpt-6-astra",
				Name:             "GPT-6 Astra",
				API:              "openai-codex-responses",
				BaseURL:          "https://chatgpt.com/backend-api",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6-astra", true),
				Capabilities:     ModelCapabilities{Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported},
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 20, Output: 75, CacheRead: 2, CacheWrite: 25}}},
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-5.1": {
				ID:            "gpt-5.1",
				Name:          "GPT-5.1",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.25, Output: 10, CacheRead: 0.125},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.1-codex-max": {
				ID:            "gpt-5.1-codex-max",
				Name:          "GPT-5.1 Codex Max",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.25, Output: 10, CacheRead: 0.125},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.1-codex-mini": {
				ID:            "gpt-5.1-codex-mini",
				Name:          "GPT-5.1 Codex Mini",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.25, Output: 2, CacheRead: 0.025},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.2": {
				ID:            "gpt-5.2",
				Name:          "GPT-5.2",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.75, Output: 14, CacheRead: 0.175},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.2-codex": {
				ID:            "gpt-5.2-codex",
				Name:          "GPT-5.2 Codex",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.75, Output: 14, CacheRead: 0.175},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.3-codex": {
				ID:            "gpt-5.3-codex",
				Name:          "GPT-5.3 Codex",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.75, Output: 14, CacheRead: 0.175},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.3-codex-spark": {
				ID:            "gpt-5.3-codex-spark",
				Name:          "GPT-5.3 Codex Spark",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText},
				Cost:          UsageCost{},
				ContextWindow: 128000,
				MaxTokens:     128000,
			},
		},
	}
}

func newOpenAIResponsesProviderModule() ProviderModule {
	supportsMidConvoSystemMessages := true
	supportsExplicitPromptCacheMode := true
	return ProviderModule{
		Provider: "openai",
		Auth: ProviderAuth{
			EnvAPIKeyName: "OPENAI_API_KEY",
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:        true,
			SupportsJSONOutput:       true,
			SupportsJSONSchema:       true,
			SupportsSession:          true,
			SupportsPromptCacheKey:   true,
			SupportsReasoningSummary: true,
			SupportsToolChoice:       true,
		},
		BuildOptions:     buildOpenAIResponsesProviderStreamOptions,
		NormalizeOptions: normalizeOpenAIResponsesProviderStreamOptions,
		Models: map[string]Model{
			"gpt-6.1-sol": {
				ID:               "gpt-6.1-sol",
				Name:             "GPT-6.1 Sol",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6.1-sol", false),
				Capabilities:     ModelCapabilities{DefaultReasoningLevel: ModelThinkingLevelMedium, Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported},
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 4, Output: 15, CacheRead: 0.2, CacheWrite: 5}}},
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-6-sol": {
				ID:               "gpt-6-sol",
				Name:             "GPT-6 Sol",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6-sol", false),
				Capabilities:     openAIResponsesSamplingCapabilities("", []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 4, Output: 15, CacheRead: 0.4, CacheWrite: 5}}},
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-6-luna": {
				ID:               "gpt-6-luna",
				Name:             "GPT-6 Luna",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6-luna", false),
				Capabilities:     openAIResponsesSamplingCapabilities("", []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 0.2, Output: 0.75, CacheRead: 0.02, CacheWrite: 0.25}}},
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-6-astra": {
				ID:               "gpt-6-astra",
				Name:             "GPT-6 Astra",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT6ThinkingLevelMap("gpt-6-astra", false),
				Capabilities:     ModelCapabilities{Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported},
				Input:            []InputType{InputText, InputImage},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Cost:             UsageCost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 20, Output: 75, CacheRead: 2, CacheWrite: 25}}},
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},

			"gpt-5.6-sol": {
				ID:               "gpt-5.6-sol",
				Name:             "GPT-5.6 Sol",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT56ThinkingLevelMap(),
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
				Capabilities:     openAIResponsesSamplingCapabilities(ModelThinkingLevelMedium, openAIGPT56ReasoningLevels()),
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 4, Output: 20, CacheRead: 0.4, CacheWrite: 5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 8, Output: 30, CacheRead: 0.8, CacheWrite: 10}}},
				ContextWindow:    272000,
				MaxTokens:        128000,
			},
			"gpt-5.6-terra": {
				ID:               "gpt-5.6-terra",
				Name:             "GPT-5.6 Terra",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT56ThinkingLevelMap(),
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
				Capabilities:     openAIResponsesSamplingCapabilities(ModelThinkingLevelMedium, openAIGPT56ReasoningLevels()),
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 2, Output: 12, CacheRead: 0.2, CacheWrite: 2.5},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 4, Output: 18, CacheRead: 0.4, CacheWrite: 5}}},
				ContextWindow:    272000,
				MaxTokens:        128000,
			},
			"gpt-5.6-luna": {
				ID:               "gpt-5.6-luna",
				Name:             "GPT-5.6 Luna",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT56ThinkingLevelMap(),
				Compat:           &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &supportsExplicitPromptCacheMode, SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
				Capabilities:     openAIResponsesSamplingCapabilities(ModelThinkingLevelMedium, openAIGPT56ReasoningLevels()),
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 0.2, Output: 1.2, CacheRead: 0.02, CacheWrite: 0.25},
				CostTiers:        []ModelCostTier{{InputTokensAbove: 272000, Rates: UsageCost{Input: 0.4, Output: 1.8, CacheRead: 0.04, CacheWrite: 0.5}}},
				ContextWindow:    272000,
				MaxTokens:        128000,
			},
			"gpt-5.1": {
				ID:               "gpt-5.1",
				Name:             "GPT-5.1",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT51ThinkingLevelMap(),
				Capabilities:     openAIResponsesSamplingCapabilities(ModelThinkingLevelOff, []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 1.25, Output: 10, CacheRead: 0.125},
				ContextWindow:    272000,
				MaxTokens:        128000,
			},
			"gpt-5.2": {
				ID:               "gpt-5.2",
				Name:             "GPT-5.2",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT52And54ThinkingLevelMap(),
				Capabilities:     openAIResponsesSamplingCapabilities(ModelThinkingLevelOff, []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 1.75, Output: 14, CacheRead: 0.175},
				ContextWindow:    272000,
				MaxTokens:        128000,
			},
			"gpt-5.4": {
				ID:               "gpt-5.4",
				Name:             "GPT-5.4",
				API:              "openai-responses",
				BaseURL:          "https://api.openai.com",
				Reasoning:        true,
				ThinkingLevelMap: openAIGPT52And54ThinkingLevelMap(),
				Capabilities:     openAIResponsesSamplingCapabilities(ModelThinkingLevelOff, []ModelThinkingLevel{ModelThinkingLevelOff}),
				Input:            []InputType{InputText, InputImage},
				Cost:             UsageCost{Input: 2.5, Output: 15, CacheRead: 0.25},
				ContextWindow:    272000,
				MaxTokens:        128000,
				Compat:           &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
			"gpt-5.4-mini": {
				ID:            "gpt-5.4-mini",
				Name:          "GPT-5.4 Mini",
				API:           "openai-responses",
				BaseURL:       "https://api.openai.com",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.75, Output: 4.5, CacheRead: 0.075},
				ContextWindow: 272000,
				MaxTokens:     128000,
				Compat:        &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &supportsMidConvoSystemMessages},
			},
		},
	}
}

func openAIGPT56ThinkingLevelMap() ThinkingLevelMap {
	return ThinkingLevelMap{
		ModelThinkingLevelOff:     "none",
		ModelThinkingLevelMinimal: "",
		ModelThinkingLevelLow:     "low",
		ModelThinkingLevelMedium:  "medium",
		ModelThinkingLevelHigh:    "high",
		ModelThinkingLevelXHigh:   "xhigh",
		ModelThinkingLevelMax:     "max",
	}
}

func openAICodexGPT56ThinkingLevelMap() ThinkingLevelMap {
	levels := openAIGPT56ThinkingLevelMap()
	levels[ModelThinkingLevelMinimal] = "low"
	return levels
}

func openAIGPT51ThinkingLevelMap() ThinkingLevelMap {
	return ThinkingLevelMap{
		ModelThinkingLevelOff:     "none",
		ModelThinkingLevelMinimal: "",
		ModelThinkingLevelLow:     "low",
		ModelThinkingLevelMedium:  "medium",
		ModelThinkingLevelHigh:    "high",
		ModelThinkingLevelXHigh:   "",
		ModelThinkingLevelMax:     "",
	}
}

func openAIGPT52And54ThinkingLevelMap() ThinkingLevelMap {
	return ThinkingLevelMap{
		ModelThinkingLevelOff:     "none",
		ModelThinkingLevelMinimal: "",
		ModelThinkingLevelLow:     "low",
		ModelThinkingLevelMedium:  "medium",
		ModelThinkingLevelHigh:    "high",
		ModelThinkingLevelXHigh:   "xhigh",
		ModelThinkingLevelMax:     "",
	}
}

func openAIGPT56ReasoningLevels() []ModelThinkingLevel {
	return []ModelThinkingLevel{
		ModelThinkingLevelOff,
		ModelThinkingLevelLow,
		ModelThinkingLevelMedium,
		ModelThinkingLevelHigh,
		ModelThinkingLevelXHigh,
		ModelThinkingLevelMax,
	}
}

func openAIResponsesSamplingCapabilities(defaultLevel ModelThinkingLevel, reasoningLevels []ModelThinkingLevel) ModelCapabilities {
	return ModelCapabilities{
		DefaultReasoningLevel:      defaultLevel,
		Temperature:                CapabilitySupported,
		TemperatureReasoningLevels: append([]ModelThinkingLevel(nil), reasoningLevels...),
		TopP:                       CapabilitySupported,
		TopPReasoningLevels:        append([]ModelThinkingLevel(nil), reasoningLevels...),
	}
}

func newKimiCodingProviderModule() ProviderModule {
	return ProviderModule{
		Provider: "kimi-coding",
		Auth: ProviderAuth{
			EnvAPIKeyName: "KIMI_API_KEY",
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:          true,
			SupportsPromptCacheControl: true,
			SupportsThinkingBudget:     true,
			HostedTools:                HostedToolCapabilities{WebSearch: true, Fetch: true, CodeRunner: true, Excel: true},
		},
		ModelCapabilities: ModelCapabilities{
			ToolChoice: CapabilityUnsupported,
		},
		BuildOptions:     buildKimiCodingProviderStreamOptions,
		NormalizeOptions: normalizeKimiCodingProviderStreamOptions,
		Models: map[string]Model{
			"k2p5": {
				ID:            "k2p5",
				Name:          "Kimi K2.5",
				API:           "anthropic-messages",
				BaseURL:       "https://api.kimi.com/coding",
				Headers:       map[string]string{"User-Agent": "KimiCLI/1.5"},
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				HostedTools:   HostedToolCapabilities{WebSearch: true, Fetch: true, CodeRunner: true, Excel: true},
				Cost:          UsageCost{},
				ContextWindow: 262144,
				MaxTokens:     32768,
			},
			"k2p6": {
				ID:            "k2p6",
				Name:          "Kimi K2.6",
				API:           "anthropic-messages",
				BaseURL:       "https://api.kimi.com/coding",
				Headers:       map[string]string{"User-Agent": "KimiCLI/1.5"},
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				HostedTools:   HostedToolCapabilities{WebSearch: true, Fetch: true, CodeRunner: true, Excel: true},
				Cost:          UsageCost{},
				ContextWindow: 262144,
				MaxTokens:     32768,
			},
			"kimi-k2-thinking": {
				ID:            "kimi-k2-thinking",
				Name:          "Kimi K2 Thinking",
				API:           "anthropic-messages",
				BaseURL:       "https://api.kimi.com/coding",
				Headers:       map[string]string{"User-Agent": "KimiCLI/1.5"},
				Reasoning:     true,
				Input:         []InputType{InputText},
				HostedTools:   HostedToolCapabilities{WebSearch: true, Fetch: true, CodeRunner: true, Excel: true},
				Cost:          UsageCost{},
				ContextWindow: 262144,
				MaxTokens:     32768,
			},
		},
	}
}

// Snapshot from pi-mono 8ce69e9d2 (2026-10-01); Astra and 6.1 Sol reject Off.
func openAIGPT6ThinkingLevelMap(id string, codex bool) ThinkingLevelMap {
	levels := openAIGPT56ThinkingLevelMap()
	if id == "gpt-6-astra" || id == "gpt-6.1-sol" {
		levels[ModelThinkingLevelOff] = ""
	}
	if codex {
		levels[ModelThinkingLevelMinimal] = "low"
	}
	return levels
}

// Snapshot from pi-mono 8ce69e9d2 (2026-10-01).
func claude55ThinkingLevelMap() ThinkingLevelMap {
	return ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low", ModelThinkingLevelMedium: "medium", ModelThinkingLevelHigh: "high", ModelThinkingLevelXHigh: "xhigh", ModelThinkingLevelMax: "max"}
}
