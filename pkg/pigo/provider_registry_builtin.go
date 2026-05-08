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
	RegisterLazyAPIModule("openai-codex-responses", newOpenAICodexResponsesAPIModule)
}

func registerBuiltInProviderModules() {
	RegisterLazyProviderModule("anthropic", newAnthropicProviderModule)
	RegisterLazyProviderModule("openai-codex", newOpenAICodexProviderModule)
	RegisterLazyProviderModule("kimi-coding", newKimiCodingProviderModule)
}

func newAnthropicMessagesAPIModule() APIModule {
	return APIModule{
		API:          "anthropic-messages",
		Stream:       streamAnthropicMessages,
		StreamSimple: streamSimpleAnthropicMessages,
	}
}

func newOpenAICodexResponsesAPIModule() APIModule {
	return APIModule{
		API:          "openai-codex-responses",
		Stream:       streamOpenAICodex,
		StreamSimple: streamSimpleOpenAICodex,
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
			HostedTools:                HostedToolCapabilities{WebSearch: true},
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
				HostedTools:   HostedToolCapabilities{WebSearch: true},
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
				HostedTools:   HostedToolCapabilities{WebSearch: true},
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
				HostedTools:   HostedToolCapabilities{WebSearch: true},
				Cost:          UsageCost{},
				ContextWindow: 262144,
				MaxTokens:     32768,
			},
		},
	}
}
