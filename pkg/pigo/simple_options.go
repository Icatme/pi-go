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
	module := resolveProviderModule(model.Provider)
	if module != nil && module.BuildOptions != nil {
		return module.BuildOptions(model, options)
	}
	return buildBaseProviderStreamOptions(model, options)
}

func buildBaseProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return streamOptionsFromSimple(model, options).providerStreamOptions(model)
}

func clampReasoning(level ThinkingLevel) ThinkingLevel {
	if level == ThinkingLevelXHigh || level == ThinkingLevelMax {
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

func mergeRequestHeaders(layers ...map[string]string) map[string]string {
	var merged map[string]string
	keys := map[string]string{}
	for _, layer := range layers {
		for key, value := range layer {
			if merged == nil {
				merged = make(map[string]string)
			}
			normalized := strings.ToLower(key)
			if previous, ok := keys[normalized]; ok {
				delete(merged, previous)
			}
			merged[key] = value
			keys[normalized] = key
		}
	}
	return merged
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
