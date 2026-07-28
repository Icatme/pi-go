package pigo

import "strings"

// GoogleProviderOptions captures provider-specific options for the Google
// Generative AI (Gemini) provider.
type GoogleProviderOptions struct {
	StreamOptions
	ToolChoice string
	Thinking   *GoogleThinkingConfig
}

// GoogleThinkingConfig controls Gemini reasoning/thinking behavior.
type GoogleThinkingConfig struct {
	Enabled      bool
	BudgetTokens int // -1 for dynamic, 0 to disable
	Level        string
}

func buildGoogleProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildGoogleProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeGoogleProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveGoogleProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildGoogleProviderOptions(model Model, options SimpleStreamOptions) GoogleProviderOptions {
	streamOptions := streamOptionsFromSimple(model, options)
	streamOptions = streamOptions.withCommonSnapshot(model)
	return GoogleProviderOptions{
		StreamOptions: streamOptions,
		ToolChoice:    streamOptions.ToolChoice,
		Thinking:      buildGoogleThinkingFromSimple(model, streamOptions),
	}
}

func resolveGoogleProviderOptions(model Model, options ProviderStreamOptions) GoogleProviderOptions {
	streamOptions := streamOptionsFromProvider(model, options)
	streamOptions = streamOptions.withCommonSnapshot(model)
	return GoogleProviderOptions{
		StreamOptions: streamOptions,
		ToolChoice:    streamOptions.ToolChoice,
		Thinking:      buildGoogleThinkingFromSimple(model, streamOptions),
	}
}

func (options GoogleProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}

func buildGoogleThinkingFromSimple(model Model, options StreamOptions) *GoogleThinkingConfig {
	if !model.Reasoning {
		return nil
	}

	level := options.Reasoning
	if level == "" || level == ThinkingLevelXHigh {
		level = ThinkingLevelHigh
	}

	if string(level) == "off" {
		return &GoogleThinkingConfig{Enabled: false}
	}

	if usesGoogleThinkingLevel(model) {
		return &GoogleThinkingConfig{
			Enabled: true,
			Level:   mapGoogleThinkingLevel(level),
		}
	}

	budgets := options.ThinkingBudgets
	if budgets.IsEmpty() {
		budgets = defaultGoogleThinkingBudgets(model)
	}

	budget := budgets.ForLevel(level)
	if budget < 0 {
		budget = -1
	}

	return &GoogleThinkingConfig{
		Enabled:      true,
		BudgetTokens: budget,
	}
}

func (b ThinkingBudgets) IsEmpty() bool {
	return b.Minimal == 0 && b.Low == 0 && b.Medium == 0 && b.High == 0
}

func (b ThinkingBudgets) ForLevel(level ThinkingLevel) int {
	switch level {
	case ThinkingLevelMinimal:
		return b.Minimal
	case ThinkingLevelLow:
		return b.Low
	case ThinkingLevelMedium:
		return b.Medium
	case ThinkingLevelHigh, ThinkingLevelXHigh:
		return b.High
	default:
		return -1
	}
}

func defaultGoogleThinkingBudgets(model Model) ThinkingBudgets {
	id := strings.ToLower(model.ID)
	switch {
	case strings.Contains(id, "2.5-pro"):
		return ThinkingBudgets{Minimal: 128, Low: 2048, Medium: 8192, High: 32768}
	case strings.Contains(id, "2.5-flash-lite"):
		return ThinkingBudgets{Minimal: 512, Low: 2048, Medium: 8192, High: 24576}
	case strings.Contains(id, "2.5-flash"):
		return ThinkingBudgets{Minimal: 128, Low: 2048, Medium: 8192, High: 24576}
	default:
		return ThinkingBudgets{}
	}
}

func usesGoogleThinkingLevel(model Model) bool {
	id := strings.ToLower(model.ID)
	return strings.Contains(id, "gemini-3") ||
		strings.Contains(id, "gemini-3.1") ||
		strings.Contains(id, "gemma-4")
}

func mapGoogleThinkingLevel(level ThinkingLevel) string {
	switch level {
	case ThinkingLevelMinimal:
		return "MINIMAL"
	case ThinkingLevelLow:
		return "LOW"
	case ThinkingLevelMedium:
		return "MEDIUM"
	case ThinkingLevelHigh, ThinkingLevelXHigh:
		return "HIGH"
	default:
		return "LOW"
	}
}

func isGemini3ProModel(id string) bool {
	return strings.Contains(strings.ToLower(id), "gemini-3") && strings.Contains(strings.ToLower(id), "-pro")
}

func isGemini3FlashModel(id string) bool {
	return strings.Contains(strings.ToLower(id), "gemini-3") && strings.Contains(strings.ToLower(id), "-flash")
}

func isGemma4Model(id string) bool {
	return strings.Contains(strings.ToLower(id), "gemma-4")
}
