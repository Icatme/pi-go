package pigo

var modelRegistry = map[Provider]map[string]Model{
	"anthropic": {
		"claude-opus-4-6": {
			ID:            "claude-opus-4-6",
			Name:          "Claude Opus 4.6",
			API:           "anthropic-messages",
			Provider:      "anthropic",
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
			Provider:      "anthropic",
			BaseURL:       "https://api.anthropic.com",
			Reasoning:     true,
			Input:         []InputType{InputText, InputImage},
			ContextWindow: 200000,
			MaxTokens:     32000,
		},
	},
	"openai-codex": {
		"gpt-5.1": {
			ID:            "gpt-5.1",
			Name:          "GPT-5.1",
			API:           "openai-codex-responses",
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
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
			Provider:      "openai-codex",
			BaseURL:       "https://chatgpt.com/backend-api",
			Reasoning:     true,
			Input:         []InputType{InputText, InputImage},
			Cost:          UsageCost{Input: 0.75, Output: 4.5, CacheRead: 0.075},
			ContextWindow: 272000,
			MaxTokens:     128000,
		},
	},
	"kimi-coding": {
		"k2p5": {
			ID:            "k2p5",
			Name:          "Kimi K2.5",
			API:           "anthropic-messages",
			Provider:      "kimi-coding",
			BaseURL:       "https://api.kimi.com/coding",
			Reasoning:     true,
			Input:         []InputType{InputText, InputImage},
			Cost:          UsageCost{},
			ContextWindow: 262144,
			MaxTokens:     32768,
		},
		"kimi-k2-thinking": {
			ID:            "kimi-k2-thinking",
			Name:          "Kimi K2 Thinking",
			API:           "anthropic-messages",
			Provider:      "kimi-coding",
			BaseURL:       "https://api.kimi.com/coding",
			Reasoning:     true,
			Input:         []InputType{InputText},
			Cost:          UsageCost{},
			ContextWindow: 262144,
			MaxTokens:     32768,
		},
	},
	"openrouter": {
		"anthropic/claude-opus-4.6": {
			ID:            "anthropic/claude-opus-4.6",
			Name:          "Claude Opus 4.6 via OpenRouter",
			API:           "openai-completions",
			Provider:      "openrouter",
			BaseURL:       "https://openrouter.ai/api/v1",
			Reasoning:     true,
			Input:         []InputType{InputText, InputImage},
			ContextWindow: 200000,
			MaxTokens:     32000,
		},
	},
}

func GetModel(provider Provider, modelID string) *Model {
	providerModels := modelRegistry[provider]
	if providerModels == nil {
		return nil
	}
	model, ok := providerModels[modelID]
	if !ok {
		return nil
	}
	cloned := model
	if len(model.Input) > 0 {
		cloned.Input = append([]InputType(nil), model.Input...)
	}
	return &cloned
}

func GetProviders() []Provider {
	providers := make([]Provider, 0, len(modelRegistry))
	for provider := range modelRegistry {
		providers = append(providers, provider)
	}
	return providers
}

func GetModels(provider Provider) []Model {
	providerModels := modelRegistry[provider]
	if providerModels == nil {
		return nil
	}
	models := make([]Model, 0, len(providerModels))
	for _, model := range providerModels {
		cloned := model
		if len(model.Input) > 0 {
			cloned.Input = append([]InputType(nil), model.Input...)
		}
		models = append(models, cloned)
	}
	return models
}

func CalculateCost(model Model, usage Usage) UsageCost {
	return UsageCost{
		Input:      (model.Cost.Input / 1_000_000) * float64(usage.Input),
		Output:     (model.Cost.Output / 1_000_000) * float64(usage.Output),
		CacheRead:  (model.Cost.CacheRead / 1_000_000) * float64(usage.CacheRead),
		CacheWrite: (model.Cost.CacheWrite / 1_000_000) * float64(usage.CacheWrite),
		Total: ((model.Cost.Input / 1_000_000) * float64(usage.Input)) +
			((model.Cost.Output / 1_000_000) * float64(usage.Output)) +
			((model.Cost.CacheRead / 1_000_000) * float64(usage.CacheRead)) +
			((model.Cost.CacheWrite / 1_000_000) * float64(usage.CacheWrite)),
	}
}

func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

func SupportsXHigh(model Model) bool {
	if contains(model.ID, "gpt-5.2") || contains(model.ID, "gpt-5.3") || contains(model.ID, "gpt-5.4") {
		return true
	}
	if contains(model.ID, "opus-4-6") || contains(model.ID, "opus-4.6") {
		return true
	}
	return false
}

func contains(value, needle string) bool {
	return len(needle) > 0 && len(value) >= len(needle) && indexOf(value, needle) >= 0
}

func indexOf(value, needle string) int {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
