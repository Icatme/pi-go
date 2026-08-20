package pigo

import "sort"

func GetModel(provider Provider, modelID string) *Model {
	module := resolveProviderModule(provider)
	if module == nil {
		return nil
	}

	model, ok := module.Models[modelID]
	if !ok {
		return nil
	}

	cloned := cloneModel(model)
	return &cloned
}

func GetProviders() []Provider {
	providers := listRegisteredProviders()
	sort.Slice(providers, func(i int, j int) bool {
		return string(providers[i]) < string(providers[j])
	})
	return providers
}

func GetModels(provider Provider) []Model {
	module := resolveProviderModule(provider)
	if module == nil {
		return nil
	}

	modelIDs := make([]string, 0, len(module.Models))
	for modelID := range module.Models {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)

	models := make([]Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, cloneModel(module.Models[modelID]))
	}
	return models
}

func CalculateCost(model Model, usage Usage) UsageCost {
	rates := model.Cost
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	matchedThreshold := -1
	for _, tier := range model.CostTiers {
		if inputTokens > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
			rates = tier.Rates
			matchedThreshold = tier.InputTokensAbove
		}
	}

	return UsageCost{
		Input:      (rates.Input / 1_000_000) * float64(usage.Input),
		Output:     (rates.Output / 1_000_000) * float64(usage.Output),
		CacheRead:  (rates.CacheRead / 1_000_000) * float64(usage.CacheRead),
		CacheWrite: (rates.CacheWrite / 1_000_000) * float64(usage.CacheWrite),
		Total: ((rates.Input / 1_000_000) * float64(usage.Input)) +
			((rates.Output / 1_000_000) * float64(usage.Output)) +
			((rates.CacheRead / 1_000_000) * float64(usage.CacheRead)) +
			((rates.CacheWrite / 1_000_000) * float64(usage.CacheWrite)),
	}
}

func calculateProviderUsageCost(model Model, usage Usage) UsageCost {
	if model.Provider == "opencode-go" {
		return UsageCost{}
	}
	return CalculateCost(model, usage)
}

func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

func SupportsXHigh(model Model) bool {
	if mapped, ok := model.ThinkingLevelMap[ModelThinkingLevelXHigh]; ok {
		return mapped != ""
	}
	if contains(model.ID, "gpt-5.2") || contains(model.ID, "gpt-5.3") || contains(model.ID, "gpt-5.4") || contains(model.ID, "gpt-5.5") || contains(model.ID, "gpt-5.6") {
		return true
	}
	if contains(model.ID, "opus-4-6") || contains(model.ID, "opus-4.6") || contains(model.ID, "sonnet-4-6") || contains(model.ID, "sonnet-4.6") {
		return true
	}
	return false
}

func SupportsMax(model Model) bool {
	mapped, ok := model.ThinkingLevelMap[ModelThinkingLevelMax]
	return ok && mapped != ""
}

var extendedThinkingLevels = []ModelThinkingLevel{
	ModelThinkingLevelOff,
	ModelThinkingLevelMinimal,
	ModelThinkingLevelLow,
	ModelThinkingLevelMedium,
	ModelThinkingLevelHigh,
	ModelThinkingLevelXHigh,
	ModelThinkingLevelMax,
}

func GetSupportedThinkingLevels(model Model) []ModelThinkingLevel {
	if !model.Reasoning {
		return []ModelThinkingLevel{ModelThinkingLevelOff}
	}

	var result []ModelThinkingLevel
	for _, level := range extendedThinkingLevels {
		mapped, hasMapping := model.ThinkingLevelMap[level]
		if mapped == "" && hasMapping {
			continue
		}
		if (level == ModelThinkingLevelXHigh || level == ModelThinkingLevelMax) && !hasMapping {
			continue
		}
		result = append(result, level)
	}
	return result
}

func ClampThinkingLevel(model Model, level ModelThinkingLevel) ModelThinkingLevel {
	available := GetSupportedThinkingLevels(model)
	for _, candidate := range available {
		if candidate == level {
			return level
		}
	}

	requestedIndex := -1
	for i, candidate := range extendedThinkingLevels {
		if candidate == level {
			requestedIndex = i
			break
		}
	}
	if requestedIndex == -1 {
		if len(available) > 0 {
			return available[0]
		}
		return ModelThinkingLevelOff
	}

	for i := requestedIndex; i < len(extendedThinkingLevels); i++ {
		for _, candidate := range available {
			if candidate == extendedThinkingLevels[i] {
				return candidate
			}
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		for _, candidate := range available {
			if candidate == extendedThinkingLevels[i] {
				return candidate
			}
		}
	}
	if len(available) > 0 {
		return available[0]
	}
	return ModelThinkingLevelOff
}

func SupportsHostedTool(model Model, toolType HostedToolType) bool {
	if model.HostedTools.Supports(toolType) {
		return true
	}
	return GetProviderCapabilities(model.Provider).HostedTools.Supports(toolType)
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
