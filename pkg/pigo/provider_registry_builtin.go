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
	RegisterLazyAPIModule("deepseek-chat-completions", newDeepSeekChatCompletionsAPIModule)
	RegisterLazyAPIModule("google-generative-ai", newGoogleGenerativeAIAPIModule)
	RegisterLazyAPIModule("mistral-conversations", newMistralConversationsAPIModule)
	RegisterLazyAPIModule("openai-codex-responses", newOpenAICodexResponsesAPIModule)
	RegisterLazyAPIModule("openai-responses", newOpenAIResponsesAPIModule)
}

func registerBuiltInProviderModules() {
	RegisterLazyProviderModule("anthropic", newAnthropicProviderModule)
	RegisterLazyProviderModule("deepseek", newDeepSeekProviderModule)
	RegisterLazyProviderModule("google", newGoogleProviderModule)
	RegisterLazyProviderModule("mistral", newMistralProviderModule)
	RegisterLazyProviderModule("openai-codex", newOpenAICodexProviderModule)
	RegisterLazyProviderModule("openai", newOpenAIResponsesProviderModule)
	RegisterLazyProviderModule("kimi-coding", newKimiCodingProviderModule)
}

func newAnthropicMessagesAPIModule() APIModule {
	return APIModule{
		API:          "anthropic-messages",
		Stream:       streamAnthropicMessages,
		StreamSimple: streamSimpleAnthropicMessages,
	}
}

func newDeepSeekChatCompletionsAPIModule() APIModule {
	return APIModule{
		API:          "deepseek-chat-completions",
		Stream:       streamDeepSeekChatCompletions,
		StreamSimple: streamSimpleDeepSeekChatCompletions,
	}
}

func newGoogleGenerativeAIAPIModule() APIModule {
	return APIModule{
		API:          "google-generative-ai",
		Stream:       streamGoogle,
		StreamSimple: streamSimpleGoogle,
	}
}

func newMistralConversationsAPIModule() APIModule {
	return APIModule{
		API:          "mistral-conversations",
		Stream:       streamMistral,
		StreamSimple: streamSimpleMistral,
	}
}

func newOpenAICodexResponsesAPIModule() APIModule {
	return APIModule{
		API:          "openai-codex-responses",
		Stream:       streamOpenAICodex,
		StreamSimple: streamSimpleOpenAICodex,
	}
}

func newOpenAIResponsesAPIModule() APIModule {
	return APIModule{
		API:          "openai-responses",
		Stream:       streamOpenAIResponses,
		StreamSimple: streamSimpleOpenAIResponses,
	}
}

func newAnthropicProviderModule() ProviderModule {
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
			SupportsToolChoice: true,
		},
		BuildOptions:     buildDeepSeekProviderStreamOptions,
		NormalizeOptions: normalizeDeepSeekProviderStreamOptions,
		Models: map[string]Model{
			"deepseek-v4-flash": {
				ID:        "deepseek-v4-flash",
				Name:      "DeepSeek V4 Flash",
				API:       "deepseek-chat-completions",
				BaseURL:   "https://api.deepseek.com",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ModelThinkingLevelMinimal: "high",
					ModelThinkingLevelLow:     "high",
					ModelThinkingLevelMedium:  "high",
					ModelThinkingLevelHigh:    "high",
					ModelThinkingLevelXHigh:   "max",
				},
				Input:         []InputType{InputText},
				Cost:          UsageCost{},
				ContextWindow: 128000,
				MaxTokens:     64000,
			},
			"deepseek-v4-pro": {
				ID:        "deepseek-v4-pro",
				Name:      "DeepSeek V4 Pro",
				API:       "deepseek-chat-completions",
				BaseURL:   "https://api.deepseek.com",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ModelThinkingLevelMinimal: "high",
					ModelThinkingLevelLow:     "high",
					ModelThinkingLevelMedium:  "high",
					ModelThinkingLevelHigh:    "high",
					ModelThinkingLevelXHigh:   "max",
				},
				Input:         []InputType{InputText},
				Cost:          UsageCost{},
				ContextWindow: 128000,
				MaxTokens:     64000,
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
				ID:            "gemini-2.5-pro",
				Name:          "Gemini 2.5 Pro",
				API:           "google-generative-ai",
				BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.25, Output: 10, CacheRead: 0.3125},
				ContextWindow: 1048576,
				MaxTokens:     65536,
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
				ID:            "mistral-small-latest",
				Name:          "Mistral Small",
				API:           "mistral-conversations",
				BaseURL:       "https://api.mistral.ai",
				Reasoning:     true,
				Input:         []InputType{InputText},
				Cost:          UsageCost{Input: 0.2, Output: 0.6},
				ContextWindow: 32000,
				MaxTokens:     4096,
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
			"gpt-5.4": {
				ID:            "gpt-5.4",
				Name:          "GPT-5.4",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 2.5, Output: 15, CacheRead: 0.25},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.4-mini": {
				ID:            "gpt-5.4-mini",
				Name:          "GPT-5.4 Mini",
				API:           "openai-codex-responses",
				BaseURL:       "https://chatgpt.com/backend-api",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 0.75, Output: 4.5, CacheRead: 0.075},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
		},
	}
}

func newOpenAIResponsesProviderModule() ProviderModule {
	return ProviderModule{
		Provider: "openai",
		Auth: ProviderAuth{
			EnvAPIKeyName: "OPENAI_API_KEY",
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming:        true,
			SupportsSession:          true,
			SupportsPromptCacheKey:   true,
			SupportsReasoningSummary: true,
			SupportsToolChoice:       true,
		},
		BuildOptions:     buildOpenAIResponsesProviderStreamOptions,
		NormalizeOptions: normalizeOpenAIResponsesProviderStreamOptions,
		Models: map[string]Model{
			"gpt-5.1": {
				ID:            "gpt-5.1",
				Name:          "GPT-5.1",
				API:           "openai-responses",
				BaseURL:       "https://api.openai.com",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.25, Output: 10, CacheRead: 0.125},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.2": {
				ID:            "gpt-5.2",
				Name:          "GPT-5.2",
				API:           "openai-responses",
				BaseURL:       "https://api.openai.com",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 1.75, Output: 14, CacheRead: 0.175},
				ContextWindow: 272000,
				MaxTokens:     128000,
			},
			"gpt-5.4": {
				ID:            "gpt-5.4",
				Name:          "GPT-5.4",
				API:           "openai-responses",
				BaseURL:       "https://api.openai.com",
				Reasoning:     true,
				Input:         []InputType{InputText, InputImage},
				Cost:          UsageCost{Input: 2.5, Output: 15, CacheRead: 0.25},
				ContextWindow: 272000,
				MaxTokens:     128000,
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
			},
		},
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
		BuildOptions:     buildKimiCodingProviderStreamOptions,
		NormalizeOptions: normalizeKimiCodingProviderStreamOptions,
		Models: map[string]Model{
			"k2p5": {
				ID:            "k2p5",
				Name:          "Kimi K2.5",
				API:           "anthropic-messages",
				BaseURL:       "https://api.kimi.com/coding",
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
