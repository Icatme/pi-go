package pigo

import "strings"

func NormalizeProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	module := resolveProviderModule(model.Provider)
	if module == nil || module.NormalizeOptions == nil {
		return options
	}
	return module.NormalizeOptions(model, options)
}

func BuildProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	switch model.API {
	case "openai-codex-responses":
		return buildOpenAICodexProviderOptions(model, options)
	case "anthropic-messages":
		return buildAnthropicProviderOptions(model, options)
	default:
		return buildBaseProviderStreamOptions(model, options)
	}
}

func buildBaseProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = minInt(model.MaxTokens, 32000)
	}
	return ProviderStreamOptions{
		APIKey:         options.APIKey,
		Auth:           options.Auth,
		HTTPClient:     options.HTTPClient,
		Headers:        cloneStringMap(options.Headers),
		MaxTokens:      maxTokens,
		Temperature:    options.Temperature,
		Transport:      options.Transport,
		CacheRetention: options.CacheRetention,
		SessionID:      options.SessionID,
		OnPayload:      options.OnPayload,
		MaxRetryDelay:  options.MaxRetryDelay,
		Metadata:       cloneMap(options.Metadata),
		RequestContext: options.RequestContext,
		Reasoning:      options.Reasoning,
	}
}

func buildOpenAICodexProviderOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	providerOptions := buildBaseProviderStreamOptions(model, options)
	if SupportsXHigh(model) {
		providerOptions.Reasoning = options.Reasoning
	} else {
		providerOptions.Reasoning = clampReasoning(options.Reasoning)
	}
	providerOptions.TextVerbosity = defaultTextVerbosity("")
	if providerOptions.Reasoning != "" {
		providerOptions.ReasoningSummary = defaultReasoningSummary("")
	}
	return NormalizeProviderStreamOptions(model, providerOptions)
}

func buildAnthropicProviderOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	providerOptions := buildBaseProviderStreamOptions(model, options)
	if providerOptions.Reasoning == "" {
		return NormalizeProviderStreamOptions(model, providerOptions)
	}

	maxTokens, thinkingBudget := adjustMaxTokensForThinking(
		providerOptions.MaxTokens,
		model.MaxTokens,
		providerOptions.Reasoning,
		options.ThinkingBudgets,
	)
	providerOptions.MaxTokens = maxTokens
	providerOptions.ThinkingBudgetTokens = thinkingBudget
	return NormalizeProviderStreamOptions(model, providerOptions)
}

func normalizeOpenAICodexOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	if options.Reasoning != "" {
		options.Reasoning = ThinkingLevel(clampOpenAICodexReasoningEffort(model, options.Reasoning))
	}
	if strings.TrimSpace(options.TextVerbosity) == "" {
		options.TextVerbosity = defaultTextVerbosity("")
	}
	if strings.TrimSpace(options.ReasoningSummary) == "" && options.Reasoning != "" {
		options.ReasoningSummary = defaultReasoningSummary("")
	}
	if options.CacheRetention != "" {
		options.CacheRetention = CacheRetentionNone
	}
	return options
}

func normalizeKimiCodingOptions(_ Model, options ProviderStreamOptions) ProviderStreamOptions {
	options.ToolChoice = ""
	if options.TextVerbosity != "" {
		options.TextVerbosity = ""
	}
	if options.ReasoningSummary != "" {
		options.ReasoningSummary = ""
	}
	if options.SessionID != "" {
		options.SessionID = ""
	}
	return options
}

func clampReasoning(level ThinkingLevel) ThinkingLevel {
	if level == ThinkingLevelXHigh {
		return ThinkingLevelHigh
	}
	return level
}

func adjustMaxTokensForThinking(
	baseMaxTokens int,
	modelMaxTokens int,
	reasoningLevel ThinkingLevel,
	customBudgets ThinkingBudgets,
) (int, int) {
	budgets := ThinkingBudgets{
		Minimal: 1024,
		Low:     2048,
		Medium:  8192,
		High:    16384,
	}
	if customBudgets.Minimal > 0 {
		budgets.Minimal = customBudgets.Minimal
	}
	if customBudgets.Low > 0 {
		budgets.Low = customBudgets.Low
	}
	if customBudgets.Medium > 0 {
		budgets.Medium = customBudgets.Medium
	}
	if customBudgets.High > 0 {
		budgets.High = customBudgets.High
	}

	level := clampReasoning(reasoningLevel)
	thinkingBudget := budgets.Medium
	switch level {
	case ThinkingLevelMinimal:
		thinkingBudget = budgets.Minimal
	case ThinkingLevelLow:
		thinkingBudget = budgets.Low
	case ThinkingLevelMedium:
		thinkingBudget = budgets.Medium
	case ThinkingLevelHigh:
		thinkingBudget = budgets.High
	}

	maxTokens := minInt(baseMaxTokens+thinkingBudget, modelMaxTokens)
	minOutputTokens := 1024
	if maxTokens <= thinkingBudget {
		thinkingBudget = maxInt(0, maxTokens-minOutputTokens)
	}
	return maxTokens, thinkingBudget
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func minInt(left int, right int) int {
	if left <= 0 {
		return right
	}
	if right <= 0 {
		return left
	}
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
